#!/usr/bin/env bash
# Отказ экземпляра api посреди продажи (этап 4, ADR 027). Два экземпляра api
# за nginx с той же настройкой, что api-lb в Compose; постоянный поток
# заказов (loadtest/chaos.js). На KILL_AT-й секунде один экземпляр
# останавливается — kill (SIGKILL, как падение процесса или узла) или stop
# (SIGTERM, как выкатка новой версии), на BACK_AT-й поднимается снова.
#
#   VARIANTS="none kill stop" RATE=100 DURATION=60s REPEATS=2 loadtest/chaos.sh
#
# После прогона: инвариант (ни одно место дважды) и двойное выполнение
# (у покупателя больше одного заказа — один запрос выполнился дважды).
set -euo pipefail
cd "$(dirname "$0")/.."

VARIANTS=${VARIANTS:-"none kill stop"}
RATE=${RATE:-100}
DURATION=${DURATION:-60s}
KILL_AT=${KILL_AT:-20}
BACK_AT=${BACK_AT:-40}
REPEATS=${REPEATS:-2}
POOL=${POOL:-20}
K6_IMAGE=${K6_IMAGE:-grafana/k6:latest}
RUNTIME_IMAGE=${RUNTIME_IMAGE:-gcr.io/distroless/static-debian13:nonroot}
LB_IMAGE=${LB_IMAGE:-nginx:1.29-alpine}
OUT=${OUT:-loadtest/results/raw/chaos}
DATA=loadtest/.data
mkdir -p "$OUT" "$DATA"

port_busy() { (exec 3<>/dev/tcp/127.0.0.1/8080) 2>/dev/null; }
if port_busy; then echo "port 8080 is busy: stop the other api or api-lb first" >&2; exit 1; fi

# API_BIN — готовый статический бинарь api (например, сборка до исправления
# для сравнения «до/после»); по умолчанию — из рабочей копии.
API_BIN=${API_BIN:-$DATA/api-static}
[[ $API_BIN == "$DATA/api-static" ]] && CGO_ENABLED=0 go build -o "$API_BIN" ./cmd/api
go build -o "$DATA/loadseed" ./cmd/loadseed
dur_s=${DURATION%s}
[[ -s $DATA/buyers-chaos-$RATE-$dur_s.json ]] || "$DATA/loadseed" buyers -n $((RATE * dur_s + 100)) -out "$DATA/buyers-chaos-$RATE-$dur_s.json"

cleanup() { docker rm -f $(docker ps -aq --filter name=dd-chaos-) >/dev/null 2>&1 || true; }
trap cleanup EXIT

start_api() {
  local i=$1
  docker rm -f "dd-chaos-api-$i" >/dev/null 2>&1 || true
  docker run -d --name "dd-chaos-api-$i" --network host --stop-timeout 20 \
    -v "$(realpath "$API_BIN"):/api:ro" --entrypoint /api \
    -e HTTP_ADDR=":$((8080 + i))" -e LOG_LEVEL=warn -e BOOKING_STRATEGY=redis \
    -e DATABASE_URL="postgres://dd:dd@localhost:5432/dd?sslmode=disable&pool_max_conns=$POOL" \
    -e REDIS_ADDR=localhost:6379 -e RABBITMQ_URL=amqp://dd:dd@localhost:5672/ \
    -e IP_TICKET_LIMIT=0 -e QUEUE_ADMIT_PER_SECOND=0 "$RUNTIME_IMAGE" >/dev/null
  for _ in $(seq 1 50); do curl -fs "localhost:$((8080 + i))/readyz" >/dev/null && return; sleep 0.2; done
  docker logs "dd-chaos-api-$i" >&2; return 1
}

# Тот же балансировщик, что deploy/nginx/api-lb.conf: повтор на другой
# экземпляр только при ошибке соединения, POST после отправки не повторяется.
cat >"$OUT/lb.conf" <<'NGINX'
upstream api { server 127.0.0.1:8081 max_fails=1 fail_timeout=5s; server 127.0.0.1:8082 max_fails=1 fail_timeout=5s; keepalive 64; }
server {
  listen 8080;
  location / {
    proxy_pass http://api;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_next_upstream error;
    proxy_connect_timeout 2s;
    proxy_read_timeout 70s;
  }
}
NGINX

for v in $VARIANTS; do
  for rep in ${REPS:-$(seq 1 "$REPEATS")}; do
    cleanup
    start_api 1
    start_api 2
    docker run -d --name dd-chaos-lb --network host -v "$PWD/$OUT/lb.conf:/etc/nginx/conf.d/default.conf:ro" "$LB_IMAGE" >/dev/null
    for _ in $(seq 1 50); do curl -fs localhost:8080/readyz >/dev/null && break; sleep 0.2; done

    dir="$OUT/chaos__${v}__${RATE}__${rep}"
    mkdir -p "$dir"
    "$DATA/loadseed" event -rows 100 -seats 100 -out "$dir/event.json"
    event=$(python3 -c "import json;print(json.load(open('$dir/event.json'))['event_id'])")

    docker run -d --name dd-chaos-k6 --user 0 --network host -v "$PWD:/work" -w /work \
      -e RATE="$RATE" -e DURATION="$DURATION" -e EVENT="/work/$dir/event.json" -e BUYERS="/work/$DATA/buyers-chaos-$RATE-$dur_s.json" \
      -e SUMMARY="/work/$dir/summary.json" "$K6_IMAGE" run --quiet --out "csv=/work/$dir/points.csv" loadtest/chaos.js >/dev/null
    t0=$(date +%s.%N)
    echo "{\"k6_started_at\": $t0, \"kill_at\": $KILL_AT, \"back_at\": $BACK_AT, \"variant\": \"$v\"}" >"$dir/timeline.json"
    if [[ $v != none ]]; then
      sleep "$KILL_AT"
      if [[ $v == kill ]]; then docker kill dd-chaos-api-1 >/dev/null; else docker stop dd-chaos-api-1 >/dev/null; fi
      echo "$(date +%s.%N) $v" >"$dir/events.txt"
      sleep $((BACK_AT - KILL_AT))
      docker start dd-chaos-api-1 >/dev/null
      for _ in $(seq 1 50); do curl -fs localhost:8081/readyz >/dev/null && break; sleep 0.2; done
      echo "$(date +%s.%N) back" >>"$dir/events.txt"
    fi
    docker wait dd-chaos-k6 >/dev/null
    docker logs dd-chaos-k6 >"$dir/k6.log" 2>&1
    docker logs dd-chaos-api-1 >"$dir/api-1.log" 2>&1 || true

    if "$DATA/loadseed" check -event "$event" -out "$dir/check.json"; then verdict=ok; else verdict=VIOLATED; fi
    # Двойное выполнение: у покупателя больше одного заказа на событие.
    dups=$(docker exec dd-postgres-1 psql -U dd -d dd -qAtc \
      "SELECT count(*) FROM (SELECT buyer_id FROM orders WHERE event_id = '$event' GROUP BY 1 HAVING count(*) > 1) d")
    orders=$(docker exec dd-postgres-1 psql -U dd -d dd -qAtc "SELECT count(*) FROM orders WHERE event_id = '$event'")
    echo "{\"orders\": $orders, \"buyers_with_two_orders\": $dups}" >"$dir/dups.json"
    echo "chaos $v #$rep invariant=$verdict orders=$orders dups=$dups $(tail -1 "$dir/k6.log" | cut -c1-200)"
    sleep 3
  done
done
