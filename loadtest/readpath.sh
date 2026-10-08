#!/usr/bin/env bash
# Читающий контур (этап 5, ADR 031): нагрузка на ведущий узел PostgreSQL при
# старте продаж на стадионе. Кластер из трёх узлов (deploy/ha), два
# экземпляра api за nginx, штурм концерта (loadtest/stadium.js) с очередью.
#
#   VARIANTS="before:old:0:0 queries:new:0:0 replica:new:1:0 edge:new:1:1" N=5000 REPEATS=2 loadtest/readpath.sh
#
# Вариант — метка:сборка:чтения с реплик (DATABASE_REPLICA_URL):кэш на краю
# (micro-cache в nginx перед api, как CDN). Сборка old — api из origin/main
# (API_OLD), и база на это время откатывается до миграции 00018: без индекса
# этапа 5. После прогона:
#   - процессорное время каждого узла PostgreSQL за прогон (cgroup);
#   - самые тяжёлые запросы ведущего и реплик (pg_stat_statements);
#   - путь покупателя (k6) и инвариант.
set -euo pipefail
cd "$(dirname "$0")/.."

N=${N:-5000}
REPEATS=${REPEATS:-2}
POOL=${POOL:-32}
RATE=${RATE:-150}
VARIANTS=${VARIANTS:-"before:old:0:0 queries:new:0:0 replica:new:1:0 edge:new:1:1"}
API_OLD=${API_OLD:-loadtest/.data/api-main-static}
LEAD_S=${LEAD_S:-120}
K6_IMAGE=${K6_IMAGE:-grafana/k6:2.3.0}
RUNTIME_IMAGE=${RUNTIME_IMAGE:-gcr.io/distroless/static-debian13:nonroot}
LB_IMAGE=${LB_IMAGE:-nginx:1.29-alpine}
OUT=${OUT:-loadtest/results/raw/readpath}
HA=deploy/ha/docker-compose.pg-ha.yml
HOSTS=localhost:5433,localhost:5434,localhost:5435
PRIMARY_DSN="postgres://dd:dd@$HOSTS/dd?sslmode=disable&target_session_attrs=read-write"
REPLICA_DSN="postgres://dd:dd@$HOSTS/dd?sslmode=disable&target_session_attrs=standby"
DATA=loadtest/.data
mkdir -p "$OUT" "$DATA"

port_busy() { (exec 3<>/dev/tcp/127.0.0.1/8080) 2>/dev/null; }
if port_busy; then echo "port 8080 is busy: stop the other api or api-lb first" >&2; exit 1; fi

CGO_ENABLED=0 go build -o "$DATA/api-static" ./cmd/api
go build -o "$DATA/loadseed" ./cmd/loadseed

cleanup() { docker rm -f dd-rp-api-1 dd-rp-api-2 dd-rp-lb >/dev/null 2>&1 || true; }
trap cleanup EXIT

psql_on() { docker exec -e PGPASSWORD=dd-admin "dd-ha-pg-$1-1" psql -U postgres -d dd -h 127.0.0.1 -qAtc "$2"; }
role() { psql_on "$1" "select pg_is_in_recovery()" 2>/dev/null || echo down; }
primary() { for n in 0 1 2; do [[ $(role $n) == f ]] && { echo $n; return; }; done; echo none; }
cpu_ns() { docker exec "dd-ha-pg-$1-1" cat /sys/fs/cgroup/cpuacct/cpuacct.usage; }

# Свежий кластер: ведущий pg-0, две реплики в потоке.
docker compose -f "$HA" down -v >/dev/null 2>&1 || true
docker compose -f "$HA" up -d --wait >/dev/null 2>&1
for _ in $(seq 1 120); do
  [[ $(primary) == 0 && $(psql_on 0 "select count(*) from pg_stat_replication where state = 'streaming'" 2>/dev/null) == 2 ]] && break
  sleep 1
done
psql_on 0 "CREATE EXTENSION IF NOT EXISTS pg_stat_statements" >/dev/null
go tool -modfile=tools.mod goose -dir migrations postgres "$PRIMARY_DSN" up >/dev/null
DATABASE_URL="$PRIMARY_DSN" "$DATA/loadseed" buyers -n "$N" -out "$DATA/buyers-readpath.json"

lb_conf() { # $1 — кэш на краю
  {
    echo 'upstream api { server 127.0.0.1:8081; server 127.0.0.1:8082; keepalive 64; }'
    if [[ $1 == 1 ]]; then
      cat <<'NGINX'
# Кэш на краю, как CDN: публичные GET по заголовкам Cache-Control ответа api.
# Одновременные промахи по одному ключу идут в api одним запросом.
proxy_cache_path /var/cache/nginx/edge levels=1:2 keys_zone=edge:20m max_size=200m inactive=10m;
NGINX
    fi
    cat <<'NGINX'
server {
  listen 8080;
  location / {
    proxy_pass http://api;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_next_upstream error;
    proxy_read_timeout 70s;
  }
NGINX
    if [[ $1 == 1 ]]; then
      cat <<'NGINX'
  location ~ ^/v1/(public/|events/[^/]+/availability$) {
    proxy_pass http://api;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_cache edge;
    proxy_cache_key $request_method$request_uri;
    proxy_cache_lock on;
    proxy_cache_lock_timeout 5s;
    proxy_cache_use_stale updating error timeout;
    proxy_cache_background_update on;
    add_header X-Cache $upstream_cache_status always;
  }
NGINX
    fi
    echo '}'
  }
}

start_api() {
  local i=$1 replica=$2 bin=$3 extra=()
  [[ $replica == 1 ]] && extra=(-e "DATABASE_REPLICA_URL=$REPLICA_DSN&pool_max_conns=$POOL")
  docker rm -f "dd-rp-api-$i" >/dev/null 2>&1 || true
  docker run -d --name "dd-rp-api-$i" --network host \
    -v "$(realpath "$bin"):/api:ro" --entrypoint /api \
    -e HTTP_ADDR=":$((8080 + i))" -e LOG_LEVEL=warn -e BOOKING_STRATEGY=redis \
    -e DATABASE_URL="$PRIMARY_DSN&pool_max_conns=$POOL" "${extra[@]}" \
    -e REDIS_ADDR=localhost:6379 -e IP_TICKET_LIMIT=0 -e QUEUE_ADMIT_PER_SECOND="$RATE" \
    "$RUNTIME_IMAGE" >/dev/null
  for _ in $(seq 1 50); do curl -fs "localhost:$((8080 + i))/readyz" >/dev/null && return; sleep 0.2; done
  docker logs "dd-rp-api-$i" >&2; return 1
}

top_queries() { # $1 — узел
  psql_on "$1" "SELECT coalesce(json_agg(q), '[]') FROM (
    SELECT left(regexp_replace(query, '\s+', ' ', 'g'), 160) AS query, calls,
           round(total_exec_time::numeric, 1) AS total_ms, rows
    FROM pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = 'dd')
    ORDER BY total_exec_time DESC LIMIT 15) q"
}

for v in $VARIANTS; do
  IFS=: read -r label build replica edge <<<"$v"
  bin=$DATA/api-static
  if [[ $build == old ]]; then
    bin=$API_OLD
    go tool -modfile=tools.mod goose -dir migrations postgres "$PRIMARY_DSN" down-to 18 >/dev/null
  else
    go tool -modfile=tools.mod goose -dir migrations postgres "$PRIMARY_DSN" up >/dev/null
  fi
  for rep in ${REPS:-$(seq 1 "$REPEATS")}; do
    cleanup
    lb_conf "$edge" >"$OUT/lb-$label.conf"
    start_api 1 "$replica" "$bin"
    start_api 2 "$replica" "$bin"
    docker run -d --name dd-rp-lb --network host -v "$PWD/deploy/nginx/nginx.conf:/etc/nginx/nginx.conf:ro" \
      -v "$PWD/$OUT/lb-$label.conf:/etc/nginx/conf.d/default.conf:ro" "$LB_IMAGE" >/dev/null
    for _ in $(seq 1 50); do curl -fs localhost:8080/readyz >/dev/null && break; sleep 0.2; done

    dir="$OUT/rp__${label}__${N}__${rep}"
    rm -rf "$dir" && mkdir -p "$dir"
    DATABASE_URL="$PRIMARY_DSN" "$DATA/loadseed" stadium -template concert -out "$dir/event.json"
    event=$(python3 -c "import json;print(json.load(open('$dir/event.json'))['event_id'])")
    start_ms=$(( ($(date +%s) + LEAD_S) * 1000 ))
    psql_on 0 "UPDATE events SET sales_start_at = to_timestamp($start_ms / 1000.0), waiting_room = true WHERE id = '$event'" >/dev/null

    for n in 0 1 2; do psql_on $n "SELECT pg_stat_statements_reset()" >/dev/null; done
    declare -A cpu0
    for n in 0 1 2; do cpu0[$n]=$(cpu_ns $n); done
    docker run --rm --user 0 --network host -v "$PWD:/work" -w /work \
      -e VUS="$N" -e QUEUE=1 -e START_AT="$start_ms" \
      -e EVENT="/work/$dir/event.json" -e BUYERS="/work/$DATA/buyers-readpath.json" \
      -e SUMMARY="/work/$dir/summary.json" "$K6_IMAGE" run --quiet loadtest/stadium.js >"$dir/k6.log" 2>&1 || true
    {
      echo "{"
      for n in 0 1 2; do
        sep=,; [[ $n == 2 ]] && sep=
        echo "\"pg-$n\": {\"cpu_s\": $(python3 -c "print(round(($(cpu_ns $n) - ${cpu0[$n]}) / 1e9, 2))"), \"primary\": $([[ $n == 0 ]] && echo true || echo false), \"top\": $(top_queries $n)}$sep"
      done
      echo "}"
    } >"$dir/db.json"
    for i in 1 2; do docker logs "dd-rp-api-$i" >"$dir/api-$i.log" 2>&1 || true; done
    if DATABASE_URL="$PRIMARY_DSN" "$DATA/loadseed" check -event "$event" -out "$dir/check.json"; then verdict=ok; else verdict=VIOLATED; fi
    echo "readpath $label #$rep $verdict cpu=$(python3 -c "import json;d=json.load(open('$dir/db.json'));print({k:v['cpu_s'] for k,v in d.items()})") $(tail -1 "$dir/k6.log" | cut -c1-200)"
  done
done
