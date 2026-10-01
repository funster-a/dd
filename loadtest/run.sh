#!/usr/bin/env bash
# Серия прогонов эксперимента (ADR 017): стратегии × уровни нагрузки ×
# повторы × сценарии. Для каждой стратегии api перезапускается с
# BOOKING_STRATEGY, для каждого прогона создаётся свежее событие.
#
# Нужно: make infra-up, make migrate-up, Docker (k6 запускается образом).
# Пример: STRATEGIES="redis" LEVELS="500" REPEATS=1 ./loadtest/run.sh
set -euo pipefail
cd "$(dirname "$0")/.."

STRATEGIES=${STRATEGIES:-"pessimistic optimistic redis"}
LEVELS=${LEVELS:-"500 2000 5000"}
REPEATS=${REPEATS:-3}
SCENARIOS=${SCENARIOS:-"one-seat hall"}
POOL=${POOL:-32}
K6_IMAGE=${K6_IMAGE:-grafana/k6:latest}
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
OUT=${OUT:-loadtest/results/raw/$STAMP}
DATA=loadtest/.data
mkdir -p "$OUT" "$DATA"
export DATABASE_URL="${DATABASE_URL:-postgres://dd:dd@localhost:5432/dd?sslmode=disable}&pool_max_conns=$POOL"


# Порт api должен быть свободен: иначе readyz ответит чужой процесс и серия
# молча пройдёт не на той стратегии (так однажды и случилось).
port_busy() { (exec 3<>/dev/tcp/127.0.0.1/8080) 2>/dev/null; }
if port_busy; then echo "port 8080 is busy: stop the other api first" >&2; exit 1; fi

go build -o "$DATA/api" ./cmd/api
go build -o "$DATA/loadseed" ./cmd/loadseed

max=0
for n in $LEVELS; do (( n > max )) && max=$n; done
if [[ ! -s $DATA/buyers.json ]] || (( $(python3 -c "import json;print(len(json.load(open('$DATA/buyers.json'))['tokens']))") < max )); then
  "$DATA/loadseed" buyers -n "$max" -out "$DATA/buyers.json"
fi

API_PID=""
stop_api() { [[ -n $API_PID ]] && kill "$API_PID" 2>/dev/null && wait "$API_PID" 2>/dev/null || true; API_PID=""; }
trap stop_api EXIT

start_api() {
  stop_api
  BOOKING_STRATEGY=$1 LOG_LEVEL=warn "$DATA/api" >"$OUT/api-$1.log" 2>&1 &
  API_PID=$!
  for _ in $(seq 1 50); do
    if curl -fs localhost:8080/readyz >/dev/null; then
      kill -0 "$API_PID" 2>/dev/null && return
      break
    fi
    sleep 0.2
  done
  echo "api did not start, see $OUT/api-$1.log" >&2; exit 1
}

cat >"$OUT/env.json" <<JSON
{"started_at":"$STAMP","cpus":$(nproc),"mem_gb":$(free -g | awk '/Mem:/{print $2}'),"pool_max_conns":$POOL,
 "go":"$(go env GOVERSION)","postgres":"$(docker exec dd-postgres-1 psql -U dd -tAc 'show server_version' | tr -d ' ')",
 "redis":"$(docker exec dd-redis-1 redis-server --version | awk '{print $3}' | cut -d= -f2)","k6":"$(docker run --rm "$K6_IMAGE" version | awk '{print $2}')"}
JSON

for strategy in $STRATEGIES; do
  start_api "$strategy"
  for scenario in $SCENARIOS; do
    for n in $LEVELS; do
      for rep in $(seq 1 "$REPEATS"); do
        dir="$OUT/${scenario}__${strategy}__${n}__${rep}"
        mkdir -p "$dir"
        "$DATA/loadseed" event -out "$dir/event.json"
        curl -fs localhost:8080/metrics >"$dir/metrics-before.txt"
        docker run --rm --user 0 --network host -v "$PWD:/work" -w /work \
          -e VUS="$n" -e SCENARIO="$scenario" -e EVENT="/work/$dir/event.json" -e BUYERS="/work/$DATA/buyers.json" \
          -e SUMMARY="/work/$dir/summary.json" "$K6_IMAGE" run --quiet loadtest/booking.js >"$dir/k6.log" 2>&1 || true
        curl -fs localhost:8080/metrics >"$dir/metrics-after.txt"
        # Попытки должны быть записаны именно этой стратегией.
        if ! grep -q "^dd_booking_attempts_total{.*strategy=\"$strategy\"" "$dir/metrics-after.txt"; then
          echo "run $dir was not served by strategy $strategy" >&2; exit 1
        fi
        event=$(python3 -c "import json;print(json.load(open('$dir/event.json'))['event_id'])")
        if "$DATA/loadseed" check -event "$event" -out "$dir/check.json"; then verdict=ok; else verdict=VIOLATED; fi
        printf '%-9s %-12s %5s #%s  %s  %s\n' "$scenario" "$strategy" "$n" "$rep" "$verdict" "$(tail -1 "$dir/k6.log" | cut -c1-110)"
        sleep 2
      done
    done
  done
done
echo "results: $OUT"
