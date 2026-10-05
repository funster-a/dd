#!/usr/bin/env bash
# Штурм стадиона (этап 3, ADR 026): N покупателей на концерте на Центральном
# стадионе Алматы (33 509 мест, шаблон ADR 025) в момент старта продаж — без
# очереди и с очередью на разной скорости пропуска. Сценарий —
# loadtest/stadium.js, стратегия захвата — redis. Инвариант проверяется
# после каждого прогона.
#
#   VARIANTS="nocache:0:0 off:0:1s q150:150:1s q300:300:1s" N=5000 REPEATS=2 loadtest/storm.sh
#
# Вариант — метка:скорость пропуска очереди (0 — без очереди):время жизни
# сводки по секторам в памяти api (AVAILABILITY_SUMMARY_TTL, 0 — без кэша).
#
# Нужны make infra-up и make migrate-up; порт 8080 должен быть свободен.
set -euo pipefail
cd "$(dirname "$0")/.."

N=${N:-5000} # больше на 16 ГБ не помещается сам k6: около 1 МБ на виртуального покупателя
REPEATS=${REPEATS:-2}
POOL=${POOL:-32}
VARIANTS=${VARIANTS:-"nocache:0:0 off:0:1s q150:150:1s q300:300:1s"}
LEAD_S=${LEAD_S:-120} # от создания события до старта: k6 успевает поднять N VU
K6_IMAGE=${K6_IMAGE:-grafana/k6:latest}
OUT=${OUT:-loadtest/results/raw/storm}
DATA=loadtest/.data
mkdir -p "$OUT" "$DATA"
export DATABASE_URL="${DATABASE_URL:-postgres://dd:dd@localhost:5432/dd?sslmode=disable}&pool_max_conns=$POOL"
# Все покупатели k6 приходят с одного адреса (ADR 020).
export IP_TICKET_LIMIT=0

port_busy() { (exec 3<>/dev/tcp/127.0.0.1/8080) 2>/dev/null; }
if port_busy; then echo "port 8080 is busy: stop the other api first" >&2; exit 1; fi

go build -o "$DATA/api" ./cmd/api
go build -o "$DATA/loadseed" ./cmd/loadseed
"$DATA/loadseed" buyers -n "$N" -out "$DATA/buyers-storm.json"

API_PID=
stop_api() { [[ -n $API_PID ]] && kill "$API_PID" 2>/dev/null && wait "$API_PID" 2>/dev/null || true; API_PID=; }
trap stop_api EXIT

for v in $VARIANTS; do
  IFS=: read -r label rate ttl <<<"$v"
  ttl=${ttl:-1s}
  for rep in $(seq 1 "$REPEATS"); do
    # Свежий api на каждый прогон: кэши и пулы одинаковы для всех вариантов.
    stop_api
    BOOKING_STRATEGY=redis QUEUE_ADMIT_PER_SECOND="$rate" AVAILABILITY_SUMMARY_TTL="$ttl" LOG_LEVEL=warn "$DATA/api" >"$OUT/api-$label-$rep.log" 2>&1 &
    API_PID=$!
    for _ in $(seq 1 50); do curl -fs localhost:8080/readyz >/dev/null && break; sleep 0.2; done
    kill -0 "$API_PID" 2>/dev/null || { echo "api did not start, see $OUT/api-$label-$rep.log" >&2; exit 1; }

    dir="$OUT/storm__${label}__${N}__${rep}"
    mkdir -p "$dir"
    "$DATA/loadseed" stadium -template concert -out "$dir/event.json"
    event=$(python3 -c "import json;print(json.load(open('$dir/event.json'))['event_id'])")
    start_ms=$(( ($(date +%s) + LEAD_S) * 1000 ))
    docker exec dd-postgres-1 psql -U dd -d dd -qAtc \
      "UPDATE events SET sales_start_at = to_timestamp($start_ms / 1000.0), waiting_room = true WHERE id = '$event'" >/dev/null
    use_queue=1; [[ $rate == 0 ]] && use_queue=0
    curl -fs localhost:8080/metrics >"$dir/metrics-before.txt"
    docker run --rm --user 0 --network host -v "$PWD:/work" -w /work \
      -e VUS="$N" -e QUEUE="$use_queue" -e START_AT="$start_ms" \
      -e EVENT="/work/$dir/event.json" -e BUYERS="/work/$DATA/buyers-storm.json" \
      -e SUMMARY="/work/$dir/summary.json" "$K6_IMAGE" run --quiet loadtest/stadium.js >"$dir/k6.log" 2>&1 || true
    curl -fs localhost:8080/metrics >"$dir/metrics-after.txt"
    if "$DATA/loadseed" check -event "$event" -out "$dir/check.json"; then verdict=ok; else verdict=VIOLATED; fi
    echo "storm $label $N #$rep $verdict $(tail -1 "$dir/k6.log" | cut -c1-220)"
    sleep 3
  done
done
