#!/usr/bin/env bash
# Отказ Redis посреди продажи (этап 4, ADR 029). Два экземпляра api за nginx,
# постоянный поток заказов (loadtest/chaos.js). На KILL_AT-й секунде ведущий
# Redis убивается (SIGKILL), на BACK_AT-й поднимается снова.
#
#   VARIANTS="single-rdb:none single-rdb:kill single-aof:kill sentinel:none sentinel:kill" REPEATS=3 loadtest/redischaos.sh
#
# Вариант — стенд:отказ.
#   single-rdb — один Redis с настройками по умолчанию, как в Compose: только
#                снимки (RDB), перезапуск теряет записи со времени снимка;
#   single-aof — один Redis с журналом (AOF, fsync раз в секунду);
#   sentinel   — три узла с журналом и три Sentinel (deploy/ha), api ищет
#                ведущий через Sentinel.
# Стенд и покупатели (сессии в Redis) создаются заново на каждый прогон:
# покупатели входят перед стартом продаж, как на настоящей продаже.
#
# После прогона: инвариант, двойное выполнение, холды Redis против базы
# (loadseed holds-check) и метрики предохранителей api.
set -euo pipefail
cd "$(dirname "$0")/.."

VARIANTS=${VARIANTS:-"single-rdb:none single-rdb:kill single-aof:kill sentinel:none sentinel:kill"}
RATE=${RATE:-100}
DURATION=${DURATION:-60s}
KILL_AT=${KILL_AT:-20}
BACK_AT=${BACK_AT:-40}
REPEATS=${REPEATS:-3}
POOL=${POOL:-20}
K6_IMAGE=${K6_IMAGE:-grafana/k6:2.3.0}
RUNTIME_IMAGE=${RUNTIME_IMAGE:-gcr.io/distroless/static-debian13:nonroot}
LB_IMAGE=${LB_IMAGE:-nginx:1.29-alpine}
REDIS_IMAGE=${REDIS_IMAGE:-redis:8.10.2-alpine}
OUT=${OUT:-loadtest/results/raw/redischaos}
HA=deploy/ha/docker-compose.redis-ha.yml
SENTINELS=localhost:26379,localhost:26380,localhost:26381
SINGLE_ADDR=localhost:6390
DB_URL="postgres://dd:dd@localhost:5432/dd?sslmode=disable"
DATA=loadtest/.data
mkdir -p "$OUT" "$DATA"

port_busy() { (exec 3<>/dev/tcp/127.0.0.1/8080) 2>/dev/null; }
if port_busy; then echo "port 8080 is busy: stop the other api or api-lb first" >&2; exit 1; fi

CGO_ENABLED=0 go build -o "$DATA/api-static" ./cmd/api
go build -o "$DATA/loadseed" ./cmd/loadseed
go tool -modfile=tools.mod goose -dir migrations postgres "$DB_URL" up >/dev/null
dur_s=${DURATION%s}
BUYERS="$DATA/buyers-redischaos.json"

cleanup() {
  docker rm -f $(docker ps -aq --filter name=dd-chaos-) >/dev/null 2>&1 || true
  docker rm -f -v dd-rchaos-redis >/dev/null 2>&1 || true
  docker compose -f "$HA" down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Окружение Redis для api и loadseed в этом стенде.
redis_env() {
  if [[ $1 == sentinel ]]; then echo "REDIS_SENTINELS=$SENTINELS REDIS_MASTER=dd"; else echo "REDIS_ADDR=$SINGLE_ADDR"; fi
}
master_ip() { docker exec dd-redis-ha-sentinel-0-1 redis-cli -p 26379 sentinel get-master-addr-by-name dd 2>/dev/null | head -1; }

setup_redis() {
  case $1 in
    single-rdb) docker run -d --name dd-rchaos-redis -p 127.0.0.1:6390:6379 "$REDIS_IMAGE" >/dev/null ;;
    single-aof) docker run -d --name dd-rchaos-redis -p 127.0.0.1:6390:6379 "$REDIS_IMAGE" \
                  redis-server --appendonly yes --appendfsync everysec >/dev/null ;;
    sentinel)   docker compose -f "$HA" up -d --wait >/dev/null 2>&1 ;;
  esac
  for _ in $(seq 1 100); do
    if [[ $1 == sentinel ]]; then
      [[ -n $(master_ip) ]] && [[ $(docker exec dd-redis-ha-redis-0-1 redis-cli info replication | grep -c 'state=online') == 2 ]] && return
    else
      redis-cli -p 6390 ping 2>/dev/null | grep -q PONG && return
    fi
    sleep 0.2
  done
  echo "redis is not ready" >&2; return 1
}

start_api() {
  local i=$1 stand=$2 renv=()
  read -ra renv <<<"$(redis_env "$stand")"
  local args=()
  for kv in "${renv[@]}"; do args+=(-e "$kv"); done
  docker rm -f "dd-chaos-api-$i" >/dev/null 2>&1 || true
  docker run -d --name "dd-chaos-api-$i" --network host \
    -v "$(realpath "$DATA/api-static"):/api:ro" --entrypoint /api \
    -e HTTP_ADDR=":$((8080 + i))" -e LOG_LEVEL=warn -e BOOKING_STRATEGY=redis \
    -e DATABASE_URL="$DB_URL&pool_max_conns=$POOL" "${args[@]}" \
    -e RABBITMQ_URL=amqp://dd:dd@localhost:5672/ \
    -e IP_TICKET_LIMIT=0 -e QUEUE_ADMIT_PER_SECOND=0 "$RUNTIME_IMAGE" >/dev/null
  for _ in $(seq 1 50); do curl -fs "localhost:$((8080 + i))/readyz" >/dev/null && return; sleep 0.2; done
  docker logs "dd-chaos-api-$i" >&2; return 1
}

# Лимит соединений балансировщика выше стандартного (1024 на процесс): за
# долгий простой копятся тысячи ждущих повторов, и стандартный лимит рвал бы
# соединения стенда, а не приложения.
cat >"$OUT/nginx.conf" <<'NGINX'
worker_processes auto;
events { worker_connections 16384; }
http { include /etc/nginx/conf.d/*.conf; }
NGINX
cat >"$OUT/lb.conf" <<'NGINX'
upstream api { server 127.0.0.1:8081 max_fails=1 fail_timeout=5s; server 127.0.0.1:8082 max_fails=1 fail_timeout=5s; keepalive 64; }
server {
  listen 8080;
  location / {
    proxy_pass http://api;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_next_upstream error;
    proxy_connect_timeout 2s;
    proxy_read_timeout 70s;
  }
}
NGINX

for v in $VARIANTS; do
  IFS=: read -r stand fault <<<"$v"
  for rep in ${REPS:-$(seq 1 "$REPEATS")}; do
    cleanup
    setup_redis "$stand"
    env $(redis_env "$stand") DATABASE_URL="$DB_URL" "$DATA/loadseed" buyers -n $((RATE * dur_s + 100)) -out "$BUYERS"
    start_api 1 "$stand"
    start_api 2 "$stand"
    docker run -d --name dd-chaos-lb --network host -v "$PWD/$OUT/nginx.conf:/etc/nginx/nginx.conf:ro" \
      -v "$PWD/$OUT/lb.conf:/etc/nginx/conf.d/default.conf:ro" "$LB_IMAGE" >/dev/null
    for _ in $(seq 1 50); do curl -fs localhost:8080/readyz >/dev/null && break; sleep 0.2; done

    dir="$OUT/chaos__${stand}-${fault}__${RATE}__${rep}"
    rm -rf "$dir" && mkdir -p "$dir"
    DATABASE_URL="$DB_URL" "$DATA/loadseed" event -rows 100 -seats 100 -out "$dir/event.json"
    event=$(python3 -c "import json;print(json.load(open('$dir/event.json'))['event_id'])")

    docker run -d --name dd-chaos-k6 --user 0 --network host -v "$PWD:/work" -w /work \
      -e RATE="$RATE" -e DURATION="$DURATION" -e EVENT="/work/$dir/event.json" -e BUYERS="/work/$BUYERS" \
      -e SUMMARY="/work/$dir/summary.json" "$K6_IMAGE" run --quiet --out "csv=/work/$dir/points.csv" loadtest/chaos.js >/dev/null
    if [[ $fault == kill ]]; then
      sleep "$KILL_AT"
      if [[ $stand == sentinel ]]; then
        old=$(master_ip)
        docker kill dd-redis-ha-redis-0-1 >/dev/null
        echo "$(date +%s.%N) kill redis-0" >"$dir/events.txt"
        for _ in $(seq 1 200); do n=$(master_ip); [[ -n $n && $n != "$old" ]] && break; sleep 0.1; done
        echo "$(date +%s.%N) promoted $n" >>"$dir/events.txt"
        sleep $((BACK_AT - KILL_AT))
        docker start dd-redis-ha-redis-0-1 >/dev/null
      else
        docker kill dd-rchaos-redis >/dev/null
        echo "$(date +%s.%N) kill redis" >"$dir/events.txt"
        sleep $((BACK_AT - KILL_AT))
        docker start dd-rchaos-redis >/dev/null
        for _ in $(seq 1 100); do redis-cli -p 6390 ping 2>/dev/null | grep -q PONG && break; sleep 0.1; done
        # Для единообразия отчёта: «повышение» здесь — Redis снова отвечает.
        echo "$(date +%s.%N) promoted redis" >>"$dir/events.txt"
      fi
      echo "$(date +%s.%N) back" >>"$dir/events.txt"
    fi
    docker wait dd-chaos-k6 >/dev/null
    docker logs dd-chaos-k6 >"$dir/k6.log" 2>&1
    for i in 1 2; do
      docker logs "dd-chaos-api-$i" >"$dir/api-$i.log" 2>&1 || true
      curl -fs "localhost:$((8080 + i))/metrics" | grep -E '^dd_(identity_session_(gate_opened|stale_hits)|booking_redis_gate_opened|booking_queue_bypassed)' >"$dir/metrics-$i.txt" || true
    done

    env $(redis_env "$stand") DATABASE_URL="$DB_URL" "$DATA/loadseed" holds-check -event "$event" -out "$dir/holds.json"
    if DATABASE_URL="$DB_URL" "$DATA/loadseed" check -event "$event" -out "$dir/check.json"; then verdict=ok; else verdict=VIOLATED; fi
    dups=$(docker exec dd-postgres-1 psql -U dd -d dd -qAtc \
      "SELECT count(*) FROM (SELECT buyer_id FROM orders WHERE event_id = '$event' GROUP BY 1 HAVING count(*) > 1) d")
    orders=$(docker exec dd-postgres-1 psql -U dd -d dd -qAtc "SELECT count(*) FROM orders WHERE event_id = '$event'")
    echo "{\"orders\": $orders, \"buyers_with_two_orders\": $dups}" >"$dir/dups.json"
    echo "redischaos $v #$rep invariant=$verdict orders=$orders dups=$dups holds=$(tr -d '\n ' <"$dir/holds.json") $(tail -1 "$dir/k6.log" | cut -c1-160)"
  done
done
