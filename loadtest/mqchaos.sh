#!/usr/bin/env bash
# Отказ RabbitMQ посреди продажи (этап 4, ADR 030). Путь от оплаты до билета
# идёт через очередь: payment.succeeded → booking → order.paid → ticket.
# loadseed paystream пишет оплаты в outbox со скоростью RATE в секунду, два
# воркера публикуют и обрабатывают их. На KILL_AT-й секунде потока узел
# RabbitMQ убивается (SIGKILL), на BACK_AT-й поднимается снова.
#
#   VARIANTS="single:none single:kill cluster:none cluster:kill cluster:lose" REPEATS=3 loadtest/mqchaos.sh
#
# Отказ: none — без отказа, kill — узел убит и поднят снова, lose — убит и не
# возвращается (только cluster).
#
# Стенд:
#   single  — один узел, как в Compose;
#   cluster — три узла (deploy/ha), воркеры знают все; убивается rabbit-0, к
#             которому воркеры подключены первыми и на котором поэтому
#             ведущие копии их очередей.
# После прогона: сколько оплаченных заказов получили билеты и через сколько
# (loadseed tickets-check), сколько сообщений ушло в очереди .dead.
set -euo pipefail
cd "$(dirname "$0")/.."

VARIANTS=${VARIANTS:-"single:none single:kill cluster:none cluster:kill cluster:lose"}
RATE=${RATE:-100}
ORDERS=${ORDERS:-6000}
KILL_AT=${KILL_AT:-20}
BACK_AT=${BACK_AT:-40}
REPEATS=${REPEATS:-3}
RUNTIME_IMAGE=${RUNTIME_IMAGE:-gcr.io/distroless/static-debian13:nonroot}
RABBIT_IMAGE=${RABBIT_IMAGE:-rabbitmq:4.3.6-management-alpine}
OUT=${OUT:-loadtest/results/raw/mqchaos}
HA=deploy/ha/docker-compose.rabbitmq-ha.yml
DB_URL="postgres://dd:dd@localhost:5432/dd?sslmode=disable"
CLUSTER_URL="amqp://dd:dd@localhost:5673/,amqp://dd:dd@localhost:5674/,amqp://dd:dd@localhost:5675/"
SINGLE_URL="amqp://dd:dd@localhost:5679/"
DATA=loadtest/.data
mkdir -p "$OUT" "$DATA"

if docker ps --format '{{.Names}}' | grep -qx dd-worker-1; then
  echo "stop the Compose worker first: it would publish this series' outbox to another broker" >&2; exit 1
fi

CGO_ENABLED=0 go build -o "$DATA/worker-static" ./cmd/worker
go build -o "$DATA/loadseed" ./cmd/loadseed
go tool -modfile=tools.mod goose -dir migrations postgres "$DB_URL" up >/dev/null

cleanup() {
  docker rm -f dd-mqchaos-worker-1 dd-mqchaos-worker-2 >/dev/null 2>&1 || true
  docker rm -f -v dd-mqchaos-rabbit >/dev/null 2>&1 || true
  docker compose -f "$HA" down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

rabbit_ctl() { # узел для rabbitmqctl: любой живой
  if [[ $1 == single ]]; then echo dd-mqchaos-rabbit; return; fi
  for n in 0 1 2; do
    docker exec "dd-rabbitmq-ha-rabbit-$n-1" rabbitmq-diagnostics -q ping >/dev/null 2>&1 && { echo "dd-rabbitmq-ha-rabbit-$n-1"; return; }
  done
}

setup_broker() {
  if [[ $1 == single ]]; then
    docker run -d --name dd-mqchaos-rabbit --hostname mqchaos -p 127.0.0.1:5679:5672 \
      -e RABBITMQ_DEFAULT_USER=dd -e RABBITMQ_DEFAULT_PASS=dd \
      -e RABBITMQ_SERVER_ADDITIONAL_ERL_ARGS="-setcookie dd-mqchaos" -e RABBITMQ_CTL_ERL_ARGS="-setcookie dd-mqchaos" \
      "$RABBIT_IMAGE" >/dev/null
    for _ in $(seq 1 120); do
      docker exec dd-mqchaos-rabbit rabbitmq-diagnostics -q ping >/dev/null 2>&1 && (exec 3<>/dev/tcp/127.0.0.1/5679) 2>/dev/null && return
      sleep 1
    done
  else
    docker compose -f "$HA" up -d --wait >/dev/null 2>&1
    for _ in $(seq 1 120); do
      [[ $(docker exec dd-rabbitmq-ha-rabbit-0-1 rabbitmqctl -q cluster_status --formatter json 2>/dev/null |
        python3 -c 'import json,sys; print(len(json.load(sys.stdin)["running_nodes"]))' 2>/dev/null) == 3 ]] && return
      sleep 1
    done
  fi
  echo "rabbitmq is not ready" >&2; return 1
}

start_worker() {
  local i=$1 url=$2
  docker run -d --name "dd-mqchaos-worker-$i" --network host \
    -v "$(realpath "$DATA/worker-static"):/worker:ro" --entrypoint /worker \
    -e LOG_LEVEL=warn -e METRICS_ADDR=":$((9100 + i))" -e DATABASE_URL="$DB_URL" \
    -e RABBITMQ_URL="$url" -e REDIS_ADDR=localhost:6379 "$RUNTIME_IMAGE" >/dev/null
}

for v in $VARIANTS; do
  IFS=: read -r stand fault <<<"$v"
  url=$SINGLE_URL
  [[ $stand == cluster ]] && url=$CLUSTER_URL
  for rep in ${REPS:-$(seq 1 "$REPEATS")}; do
    cleanup
    setup_broker "$stand"
    dir="$OUT/mq__${stand}-${fault}__${RATE}__${rep}"
    rm -rf "$dir" && mkdir -p "$dir"
    DATABASE_URL="$DB_URL" "$DATA/loadseed" event -rows 100 -seats 100 -out "$dir/event.json"
    start_worker 1 "$url"
    start_worker 2 "$url"
    sleep 3 # воркеры объявляют очереди

    DATABASE_URL="$DB_URL" "$DATA/loadseed" paystream -event "$dir/event.json" -n "$ORDERS" -rate "$RATE" \
      -out "$dir/stream.json" -started "$dir/started" &
    stream=$!
    # Сначала генератор создаёт заказы (около минуты), отказ отсчитывается
    # от начала самих оплат.
    while [[ ! -f $dir/started ]]; do
      kill -0 "$stream" 2>/dev/null || { echo "paystream exited before the stream started" >&2; exit 1; }
      sleep 0.1
    done
    if [[ $stand == cluster ]]; then
      docker exec dd-rabbitmq-ha-rabbit-0-1 rabbitmqctl -q list_queues name leader --formatter json >"$dir/leaders.json" 2>/dev/null || true
    fi
    if [[ $fault == kill || $fault == lose ]]; then
      sleep "$KILL_AT"
      victim=dd-mqchaos-rabbit
      [[ $stand == cluster ]] && victim=dd-rabbitmq-ha-rabbit-0-1
      docker kill "$victim" >/dev/null
      echo "$(date +%s.%N) kill $victim" >"$dir/events.txt"
      if [[ $fault == kill ]]; then
        sleep $((BACK_AT - KILL_AT))
        docker start "$victim" >/dev/null
        echo "$(date +%s.%N) back" >>"$dir/events.txt"
      fi
    fi
    wait "$stream"

    DATABASE_URL="$DB_URL" "$DATA/loadseed" tickets-check -stream "$dir/stream.json" -wait 3m -out "$dir/tickets.json"
    ctl=$(rabbit_ctl "$stand")
    docker exec "$ctl" rabbitmqctl -q list_queues name messages --formatter json >"$dir/queues.json" 2>/dev/null || echo '[]' >"$dir/queues.json"
    for i in 1 2; do docker logs "dd-mqchaos-worker-$i" >"$dir/worker-$i.log" 2>&1 || true; done
    echo "mqchaos $v #$rep $(tr -d '\n ' <"$dir/tickets.json" | sed 's/,"max_latency_by_s":{[^}]*}//') dead=$(python3 -c "import json;print(sum(q['messages'] for q in json.load(open('$dir/queues.json')) if q['name'].endswith('.dead')))")"
  done
done
