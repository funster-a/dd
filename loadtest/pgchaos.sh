#!/usr/bin/env bash
# Переключение PostgreSQL посреди продажи (этап 4, ADR 028). Кластер из трёх
# узлов repmgr (deploy/ha), два экземпляра api за nginx, постоянный поток
# заказов (loadtest/chaos.js). На KILL_AT-й секунде ведущий узел убивается
# (SIGKILL), одна из реплик повышается; на BACK_AT-й бывший ведущий
# поднимается и встаёт репликой.
#
#   VARIANTS="async:none:new async:kill:old async:kill:new sync:none:new sync:kill:new" REPEATS=3 loadtest/pgchaos.sh
#
# Вариант — репликация (async, sync):отказ (none, kill):сборка api (new —
# рабочая копия, old — API_OLD). После прогона:
#   - подтверждённые клиенту заказы, которых нет в базе (потеря при
#     переключении);
#   - инвариант и двойное выполнение — как в chaos.sh.
set -euo pipefail
cd "$(dirname "$0")/.."

VARIANTS=${VARIANTS:-"async:none:new async:kill:old async:kill:new sync:none:new sync:kill:new"}
RATE=${RATE:-100}
DURATION=${DURATION:-60s}
KILL_AT=${KILL_AT:-20}
BACK_AT=${BACK_AT:-40}
REPEATS=${REPEATS:-3}
POOL=${POOL:-20}
API_OLD=${API_OLD:-loadtest/.data/api-before}
K6_IMAGE=${K6_IMAGE:-grafana/k6:latest}
RUNTIME_IMAGE=${RUNTIME_IMAGE:-gcr.io/distroless/static-debian13:nonroot}
LB_IMAGE=${LB_IMAGE:-nginx:1.29-alpine}
OUT=${OUT:-loadtest/results/raw/pgchaos}
HA=deploy/ha/docker-compose.pg-ha.yml
HA_DSN="postgres://dd:dd@localhost:5433,localhost:5434,localhost:5435/dd?sslmode=disable&target_session_attrs=read-write"
DATA=loadtest/.data
mkdir -p "$OUT" "$DATA"

port_busy() { (exec 3<>/dev/tcp/127.0.0.1/8080) 2>/dev/null; }
if port_busy; then echo "port 8080 is busy: stop the other api or api-lb first" >&2; exit 1; fi

CGO_ENABLED=0 go build -o "$DATA/api-static" ./cmd/api
go build -o "$DATA/loadseed" ./cmd/loadseed
dur_s=${DURATION%s}
BUYERS="$DATA/buyers-pgchaos.json"

cleanup() { docker rm -f $(docker ps -aq --filter name=dd-chaos-) >/dev/null 2>&1 || true; }
trap cleanup EXIT

psql_on() { docker exec -e PGPASSWORD=dd-admin "dd-ha-pg-$1-1" psql -U postgres -d dd -h 127.0.0.1 -qAtc "$2"; }
role() { psql_on "$1" "select pg_is_in_recovery()" 2>/dev/null || echo down; }
primary() { for n in 0 1 2; do [[ $(role $n) == f ]] && { echo $n; return; }; done; echo none; }
# Все узлы живы: один ведущий и две реплики с ним в потоке.
wait_cluster() {
  for _ in $(seq 1 120); do
    local p; p=$(primary)
    if [[ $p != none ]] &&
       [[ $(psql_on "$p" "select count(*) from pg_stat_replication where state = 'streaming'" 2>/dev/null) == 2 ]]; then
      return 0
    fi
    sleep 1
  done
  echo "cluster is not healthy" >&2; return 1
}

cluster_mode=
setup_cluster() {
  local mode=$1
  [[ $cluster_mode == "$mode" ]] && { wait_cluster; return; }
  docker compose -f "$HA" down -v >/dev/null 2>&1 || true
  docker compose -f "$HA" up -d --wait >/dev/null
  wait_cluster
  if [[ $mode == sync ]]; then
    # На каждом узле, а не только на ведущем: повышенная реплика остаётся
    # синхронной (см. deploy/ha).
    for n in 0 1 2; do
      psql_on $n "ALTER SYSTEM SET synchronous_standby_names = 'ANY 1 (\"pg-0\",\"pg-1\",\"pg-2\")'" >/dev/null
      psql_on $n "SELECT pg_reload_conf()" >/dev/null
    done
  fi
  wait_cluster
  go tool -modfile=tools.mod goose -dir migrations postgres "$HA_DSN" up >/dev/null
  DATABASE_URL="$HA_DSN" "$DATA/loadseed" buyers -n $((RATE * dur_s + 100)) -out "$BUYERS"
  cluster_mode=$mode
}

start_api() {
  local i=$1 bin=$2
  docker rm -f "dd-chaos-api-$i" >/dev/null 2>&1 || true
  docker run -d --name "dd-chaos-api-$i" --network host \
    -v "$(realpath "$bin"):/api:ro" --entrypoint /api \
    -e HTTP_ADDR=":$((8080 + i))" -e LOG_LEVEL=warn -e BOOKING_STRATEGY=redis \
    -e DATABASE_URL="$HA_DSN&pool_max_conns=$POOL" \
    -e REDIS_ADDR=localhost:6379 -e RABBITMQ_URL=amqp://dd:dd@localhost:5672/ \
    -e IP_TICKET_LIMIT=0 -e QUEUE_ADMIT_PER_SECOND=0 "$RUNTIME_IMAGE" >/dev/null
  for _ in $(seq 1 50); do curl -fs "localhost:$((8080 + i))/readyz" >/dev/null && return; sleep 0.2; done
  docker logs "dd-chaos-api-$i" >&2; return 1
}

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
  IFS=: read -r mode fault build <<<"$v"
  bin=$DATA/api-static
  [[ $build == old ]] && bin=$API_OLD
  for rep in ${REPS:-$(seq 1 "$REPEATS")}; do
    setup_cluster "$mode"
    cleanup
    start_api 1 "$bin"
    start_api 2 "$bin"
    docker run -d --name dd-chaos-lb --network host -v "$PWD/$OUT/lb.conf:/etc/nginx/conf.d/default.conf:ro" "$LB_IMAGE" >/dev/null
    for _ in $(seq 1 50); do curl -fs localhost:8080/readyz >/dev/null && break; sleep 0.2; done

    dir="$OUT/chaos__${mode}-${fault}-${build}__${RATE}__${rep}"
    mkdir -p "$dir"
    # Режим репликации на старте прогона: sync — реплики в кворуме, а не async.
    psql_on "$(primary)" "SELECT string_agg(application_name || ':' || sync_state, ' ') FROM pg_stat_replication" >"$dir/replication.txt"
    DATABASE_URL="$HA_DSN" "$DATA/loadseed" event -rows 100 -seats 100 -out "$dir/event.json"
    event=$(python3 -c "import json;print(json.load(open('$dir/event.json'))['event_id'])")

    docker run -d --name dd-chaos-k6 --user 0 --network host -v "$PWD:/work" -w /work \
      -e RATE="$RATE" -e DURATION="$DURATION" -e EVENT="/work/$dir/event.json" -e BUYERS="/work/$BUYERS" \
      -e SUMMARY="/work/$dir/summary.json" "$K6_IMAGE" run --quiet --out "csv=/work/$dir/points.csv" loadtest/chaos.js >/dev/null
    if [[ $fault == kill ]]; then
      sleep "$KILL_AT"
      victim=$(primary)
      docker kill "dd-ha-pg-$victim-1" >/dev/null
      echo "$(date +%s.%N) kill pg-$victim" >"$dir/events.txt"
      for _ in $(seq 1 100); do p=$(primary); [[ $p != none && $p != "$victim" ]] && break; sleep 0.2; done
      echo "$(date +%s.%N) promoted pg-$p" >>"$dir/events.txt"
      sleep $((BACK_AT - KILL_AT))
      docker start "dd-ha-pg-$victim-1" >/dev/null
      echo "$(date +%s.%N) back pg-$victim" >>"$dir/events.txt"
    fi
    docker wait dd-chaos-k6 >/dev/null
    docker logs dd-chaos-k6 >"$dir/k6.log" 2>&1
    for i in 1 2; do docker logs "dd-chaos-api-$i" >"$dir/api-$i.log" 2>&1 || true; done
    wait_cluster

    p=$(primary)
    if DATABASE_URL="$HA_DSN" "$DATA/loadseed" check -event "$event" -out "$dir/check.json"; then verdict=ok; else verdict=VIOLATED; fi
    dups=$(psql_on "$p" "SELECT count(*) FROM (SELECT buyer_id FROM orders WHERE event_id = '$event' GROUP BY 1 HAVING count(*) > 1) d")
    orders=$(psql_on "$p" "SELECT count(*) FROM orders WHERE event_id = '$event'")
    # Потерянные подтверждения: id заказов, на которые клиент получил 201.
    python3 - "$dir/points.csv" >"$dir/acked.txt" <<'PY'
import csv, sys
for r in csv.DictReader(open(sys.argv[1])):
    if r['metric_name'] == 'chaos_created':
        for kv in r['extra_tags'].split('&'):
            if kv.startswith('order='):
                print(kv[6:])
PY
    acked=$(wc -l <"$dir/acked.txt")
    present=0
    if [[ $acked -gt 0 ]]; then
      # Тысячи id не помещаются в аргумент: через stdin во временную таблицу.
      present=$({ echo "CREATE TEMP TABLE acked (id uuid); COPY acked FROM STDIN;"; cat "$dir/acked.txt"; echo '\.'
        echo "SELECT count(*) FROM orders JOIN acked USING (id);"; } |
        docker exec -i -e PGPASSWORD=dd-admin "dd-ha-pg-$p-1" psql -U postgres -d dd -h 127.0.0.1 -qAt -f - | tail -1)
    fi
    echo "{\"orders\": $orders, \"buyers_with_two_orders\": $dups, \"acked\": $acked, \"acked_missing\": $((acked - present))}" >"$dir/dups.json"
    echo "pgchaos $v #$rep invariant=$verdict acked=$acked missing=$((acked - present)) dups=$dups $(tail -1 "$dir/k6.log" | cut -c1-160)"
    sleep 3
  done
done
