#!/usr/bin/env bash
# Очередь ожидания при старте продаж (ADR 020): штурм зала на 1000 мест в
# момент объявленного старта, без очереди и с очередью на разной скорости
# пропуска. Стратегия захвата — redis (продуктовая). Инвариант проверяется
# после каждого прогона, как в основной серии (ADR 017).
#
#   VARIANTS="off:0 q100:100 q200:200" N=5000 REPEATS=3 loadtest/queue.sh
#
# WARMUP=1 — покупатели до старта открывают свой профиль, и их сессии
# попадают в кэш api (ADR 018). Без этого вариант off на только что
# запущенном api проверяет «холодный» старт: см. отчёт.
set -euo pipefail
cd "$(dirname "$0")/.."

N=${N:-5000}
REPEATS=${REPEATS:-3}
POOL=${POOL:-32}
VARIANTS=${VARIANTS:-"off:0 q100:100 q200:200"}
LEAD_S=${LEAD_S:-40} # от создания события до старта продаж: k6 успевает поднять VU
K6_IMAGE=${K6_IMAGE:-grafana/k6:latest}
OUT=${OUT:-loadtest/results/raw/queue}
DATA=loadtest/.data
mkdir -p "$OUT" "$DATA"
export DATABASE_URL="${DATABASE_URL:-postgres://dd:dd@localhost:5432/dd?sslmode=disable}&pool_max_conns=$POOL"
# Все виртуальные покупатели k6 приходят с одного адреса: лимит билетов на IP
# (ADR 020) на стенде выключен, иначе после 40 билетов остальные получат отказ.
export IP_TICKET_LIMIT=0

# Порт api должен быть свободен: иначе ответит чужой процесс (ADR 017).
port_busy() { (exec 3<>/dev/tcp/127.0.0.1/8080) 2>/dev/null; }
if port_busy; then echo "port 8080 is busy: stop the other api first" >&2; exit 1; fi

go build -o "$DATA/api" ./cmd/api
go build -o "$DATA/loadseed" ./cmd/loadseed
"$DATA/loadseed" buyers -n "$N" -out "$DATA/buyers.json"

API_PID=
stop_api() { [[ -n $API_PID ]] && kill "$API_PID" 2>/dev/null && wait "$API_PID" 2>/dev/null || true; API_PID=; }
trap stop_api EXIT

for v in $VARIANTS; do
  label=${v%%:*}
  rate=${v##*:}
  stop_api
  BOOKING_STRATEGY=redis QUEUE_ADMIT_PER_SECOND="$rate" LOG_LEVEL=warn "$DATA/api" >"$OUT/api-$label.log" 2>&1 &
  API_PID=$!
  for _ in $(seq 1 50); do curl -fs localhost:8080/readyz >/dev/null && break; sleep 0.2; done
  kill -0 "$API_PID" 2>/dev/null || { echo "api did not start, see $OUT/api-$label.log" >&2; exit 1; }

  for rep in $(seq 1 "$REPEATS"); do
    dir="$OUT/queue__${label}__${N}__${rep}"
    mkdir -p "$dir"
    "$DATA/loadseed" event -out "$dir/event.json"
    event=$(python3 -c "import json;print(json.load(open('$dir/event.json'))['event_id'])")
    start_ms=$(( ($(date +%s) + LEAD_S) * 1000 ))
    docker exec dd-postgres-1 psql -U dd -d dd -qAtc \
      "UPDATE events SET sales_start_at = to_timestamp($start_ms / 1000.0), waiting_room = true WHERE id = '$event'" >/dev/null
    use_queue=1; [[ $rate == 0 ]] && use_queue=0
    curl -fs localhost:8080/metrics >"$dir/metrics-before.txt"
    docker run --rm --user 0 --network host -v "$PWD:/work" -w /work \
      -e VUS="$N" -e SCENARIO=queue -e QUEUE="$use_queue" -e START_AT="$start_ms" -e WARMUP="${WARMUP:-0}" \
      -e EVENT="/work/$dir/event.json" -e BUYERS="/work/$DATA/buyers.json" \
      -e SUMMARY="/work/$dir/summary.json" "$K6_IMAGE" run --quiet loadtest/booking.js >"$dir/k6.log" 2>&1 || true
    curl -fs localhost:8080/metrics >"$dir/metrics-after.txt"
    if "$DATA/loadseed" check -event "$event" -out "$dir/check.json"; then verdict=ok; else verdict=VIOLATED; fi
    echo "queue $label $N #$rep $verdict $(tail -1 "$dir/k6.log" | cut -c1-200)"
    sleep 3
  done
done
