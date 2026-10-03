#!/usr/bin/env bash
# Горизонтальное масштабирование api (ADR 023): ёмкость при 1, 2 и 4
# экземплярах api за балансировщиком. Каждый экземпляр — контейнер с лимитом
# процессора CPUS (по умолчанию пол-ядра): так экземпляр ведёт себя как
# отдельный узел, а не делит все ядра машины с соседями.
#
# Для каждого числа экземпляров — ступени постоянного потока заказов RATES
# (сценарий loadtest/capacity.js) по DURATION. Ёмкость — самая высокая
# ступень, где p95 ниже SLO и ошибок меньше 1%. Стратегия захвата — redis.
# Инвариант проверяется после каждого числа экземпляров.
#
#   LEVELS="1 2 4" RATES="100 200 300 400 600 800" DURATION=20s loadtest/scale.sh
#
# Нужны make infra-up и make migrate-up; порт 8080 должен быть свободен
# (полный Compose со своим api-lb — остановить).
set -euo pipefail
cd "$(dirname "$0")/.."

N=${N:-5000}          # покупателей с сессиями
LEVELS=${LEVELS:-"1 2 4"}
RATES=${RATES:-"100 200 300 400 600 800"}
DURATION=${DURATION:-20s}
ROWS=${ROWS:-100}      # зал ROWS × SEATS мест: больше, чем покупателей, чтобы
SEATS=${SEATS:-60}     # корзины покупателей не съели зал целиком
CPUS=${CPUS:-0.5}
POOL=${POOL:-20} # соединений с базой у каждого экземпляра; 4 × 20 < max_connections 100
K6_IMAGE=${K6_IMAGE:-grafana/k6:latest}
RUNTIME_IMAGE=${RUNTIME_IMAGE:-gcr.io/distroless/static-debian13:nonroot}
LB_IMAGE=${LB_IMAGE:-nginx:1.29-alpine}
OUT=${OUT:-loadtest/results/raw/scale}
DATA=loadtest/.data
mkdir -p "$OUT" "$DATA"

port_busy() { (exec 3<>/dev/tcp/127.0.0.1/8080) 2>/dev/null; }
if port_busy; then echo "port 8080 is busy: stop the other api or api-lb first" >&2; exit 1; fi

CGO_ENABLED=0 go build -o "$DATA/api-static" ./cmd/api
go build -o "$DATA/loadseed" ./cmd/loadseed
"$DATA/loadseed" buyers -n "$N" -out "$DATA/buyers.json"

cleanup() { docker rm -f $(docker ps -aq --filter name=dd-scale-) >/dev/null 2>&1 || true; }
trap cleanup EXIT

start_level() {
  local n=$1 servers=""
  cleanup
  for i in $(seq 1 "$n"); do
    local port=$((8080 + i))
    docker run -d --name "dd-scale-api-$i" --network host --cpus "$CPUS" \
      -v "$PWD/$DATA/api-static:/api:ro" --entrypoint /api \
      -e HTTP_ADDR=":$port" -e LOG_LEVEL=warn -e BOOKING_STRATEGY=redis \
      -e DATABASE_URL="postgres://dd:dd@localhost:5432/dd?sslmode=disable&pool_max_conns=$POOL" \
      -e REDIS_ADDR=localhost:6379 -e RABBITMQ_URL=amqp://dd:dd@localhost:5672/ \
      -e IP_TICKET_LIMIT=0 -e QUEUE_ADMIT_PER_SECOND=0 \
      "$RUNTIME_IMAGE" >/dev/null
    servers+="server 127.0.0.1:$port max_fails=0;"
  done
  # Балансировщик — тот же nginx, что в Compose, но с явным списком экземпляров.
  printf 'upstream api { %s keepalive 64; }\nserver { listen 8080; location / { proxy_pass http://api; proxy_http_version 1.1; proxy_set_header Connection ""; proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for; proxy_next_upstream error; proxy_read_timeout 70s; } }\n' "$servers" >"$OUT/lb.conf"
  docker run -d --name dd-scale-lb --network host -v "$PWD/$OUT/lb.conf:/etc/nginx/conf.d/default.conf:ro" "$LB_IMAGE" >/dev/null
  for i in $(seq 1 "$n"); do
    for _ in $(seq 1 50); do curl -fs "localhost:$((8080 + i))/readyz" >/dev/null && break; sleep 0.2; done
    curl -fs "localhost:$((8080 + i))/readyz" >/dev/null || { docker logs "dd-scale-api-$i" >&2; exit 1; }
  done
  for _ in $(seq 1 50); do curl -fs localhost:8080/readyz >/dev/null && break; sleep 0.2; done
}

for n in $LEVELS; do
  start_level "$n"
  lvl="$OUT/api${n}"
  mkdir -p "$lvl"
  "$DATA/loadseed" event -rows "$ROWS" -seats "$SEATS" -out "$lvl/event.json"
  event=$(python3 -c "import json;print(json.load(open('$lvl/event.json'))['event_id'])")
  for rate in $RATES; do
    dir="$OUT/scale__api${n}__${rate}__1"
    mkdir -p "$dir"
    docker run --rm --user 0 --network host -v "$PWD:/work" -w /work \
      -e RATE="$rate" -e DURATION="$DURATION" \
      -e EVENT="/work/$lvl/event.json" -e BUYERS="/work/$DATA/buyers.json" \
      -e SUMMARY="/work/$dir/summary.json" "$K6_IMAGE" run --quiet loadtest/capacity.js >"$dir/k6.log" 2>&1 || true
    echo "scale api=$n rate=$rate $(tail -1 "$dir/k6.log" | cut -c1-220)"
    sleep 5
  done
  if "$DATA/loadseed" check -event "$event" -out "$lvl/check.json"; then verdict=ok; else verdict=VIOLATED; fi
  echo "scale api=$n invariant $verdict"
done
