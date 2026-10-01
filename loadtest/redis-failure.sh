#!/usr/bin/env bash
# Отказ Redis в середине прогона (spec.md, «Что измеряем»; ADR 017).
# Стратегия redis, сценарий hall: когда первые попытки дошли до api,
# контейнер Redis ставится на паузу, после прогона — снова запускается.
# Инвариант проверяется так же, как в основной серии.
set -euo pipefail
cd "$(dirname "$0")/.."

N=${N:-2000}
REPEATS=${REPEATS:-3}
POOL=${POOL:-32}
DOWN_AFTER_MS=${DOWN_AFTER_MS:-300}
K6_IMAGE=${K6_IMAGE:-grafana/k6:latest}
OUT=${OUT:-loadtest/results/raw/redis-failure}
DATA=loadtest/.data
mkdir -p "$OUT"
export DATABASE_URL="${DATABASE_URL:-postgres://dd:dd@localhost:5432/dd?sslmode=disable}&pool_max_conns=$POOL"


# Порт api должен быть свободен: иначе readyz ответит чужой процесс и серия
# молча пройдёт не на той стратегии (так однажды и случилось).
port_busy() { (exec 3<>/dev/tcp/127.0.0.1/8080) 2>/dev/null; }
if port_busy; then echo "port 8080 is busy: stop the other api first" >&2; exit 1; fi

go build -o "$DATA/api" ./cmd/api
go build -o "$DATA/loadseed" ./cmd/loadseed

BOOKING_STRATEGY=redis LOG_LEVEL=warn "$DATA/api" >"$OUT/api.log" 2>&1 &
API_PID=$!
trap 'docker unpause dd-redis-1 >/dev/null 2>&1 || true; kill $API_PID 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do curl -fs localhost:8080/readyz >/dev/null && break; sleep 0.2; done
kill -0 $API_PID 2>/dev/null || { echo "api did not start, see $OUT/api.log" >&2; exit 1; }

attempts() { curl -fs localhost:8080/metrics | awk '/^dd_booking_attempts_total/{s+=$2} END{print s+0}'; }

for rep in $(seq 1 "$REPEATS"); do
  dir="$OUT/hall__redis-down__${N}__${rep}"
  mkdir -p "$dir"
  "$DATA/loadseed" event -out "$dir/event.json"
  curl -fs localhost:8080/metrics >"$dir/metrics-before.txt"
  base=$(attempts)
  docker run --rm --user 0 --network host -v "$PWD:/work" -w /work \
    -e VUS="$N" -e SCENARIO=hall -e EVENT="/work/$dir/event.json" -e BUYERS="/work/$DATA/buyers.json" \
    -e SUMMARY="/work/$dir/summary.json" "$K6_IMAGE" run --quiet loadtest/booking.js >"$dir/k6.log" 2>&1 &
  K6=$!
  # Ждём первые попытки на сервере, затем «роняем» Redis.
  while kill -0 $K6 2>/dev/null && (( $(attempts) <= base )); do sleep 0.05; done
  sleep "$(awk "BEGIN{print $DOWN_AFTER_MS/1000}")"
  docker pause dd-redis-1 >/dev/null
  date -u +%FT%T.%3NZ >"$dir/redis-down-at.txt"
  wait $K6 || true
  docker unpause dd-redis-1 >/dev/null
  sleep 1
  curl -fs localhost:8080/metrics >"$dir/metrics-after.txt"
  grep -q '^dd_booking_attempts_total{.*strategy="redis"' "$dir/metrics-after.txt" || { echo "run $dir was not served by strategy redis" >&2; exit 1; }
  event=$(python3 -c "import json;print(json.load(open('$dir/event.json'))['event_id'])")
  if "$DATA/loadseed" check -event "$event" -out "$dir/check.json"; then verdict=ok; else verdict=VIOLATED; fi
  echo "redis-down $N #$rep $verdict $(tail -1 "$dir/k6.log" | cut -c1-160)"
  sleep 3
done
