#!/usr/bin/env bash
# Автомасштабирование при штурме и обратное сжатие (этап 6, ADR 032).
# Локальный кластер kind, приложение из deploy/k8s/overlays/local, штурм
# концерта на стадионе (loadtest/stadium.js) изнутри кластера — k6 ходит в
# edge так же, как ходил бы трафик из Ingress. Каждые 5 секунд пишется
# состояние HPA и подов: сколько экземпляров хотел HPA, сколько запущено и
# готово, загрузка CPU. После штурма запись идёт ещё COOLDOWN_S секунд —
# видно обратное сжатие.
#
#   N=5000 RATE=150 loadtest/k8s-storm.sh
#
# В облаке — workflow «Kubernetes storm» (.github/workflows/k8s-storm.yml) на
# раннере GitHub. API_CPU — запрос процессора пода api вместо 500m из base:
# на машине в 4 ядра с запросом 500m места хватает только трём подам, и
# потолок HPA был бы виден раньше, чем само масштабирование.
#
# Нужны docker, kind и kubectl. Кластер создаётся, если его нет, и остаётся
# после прогона: kind delete cluster --name dd.
#
# Результат в $OUT/run-<время>:
#   hpa.csv     — время от старта продаж, по api, edge и worker: желаемые,
#                 текущие и готовые экземпляры, CPU в процентах от запроса;
#                 ожидающие планирования поды;
#   summary.json — путь покупателя (k6); check.json — инвариант.
set -euo pipefail
cd "$(dirname "$0")/.."

CLUSTER=${CLUSTER:-dd}
N=${N:-5000} # k6 — около 1 МБ памяти на покупателя
RATE=${RATE:-150}
LEAD_S=${LEAD_S:-120}
COOLDOWN_S=${COOLDOWN_S:-720} # стабилизация сжатия — 5 минут, затем по экземпляру в минуту
API_CPU=${API_CPU:-}
K6_IMAGE=${K6_IMAGE:-grafana/k6:2.3.0}
METRICS_SERVER=${METRICS_SERVER:-https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.8.0/components.yaml}
OUT=${OUT:-loadtest/results/raw/k8s-storm}
NS=dd
PG_PORT=${PG_PORT:-15432}
REDIS_PORT=${REDIS_PORT:-16379}
DATA=loadtest/.data
dir="$OUT/run-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$dir" "$DATA"
k() { kubectl --context "kind-$CLUSTER" "$@"; }

# Кластер и metrics-server: HPA берёт загрузку CPU из metrics API.
kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --wait 120s
k apply -f "$METRICS_SERVER" >/dev/null
# В kind у kubelet самоподписанный сертификат.
k -n kube-system patch deployment metrics-server --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]' >/dev/null 2>&1 || true
k -n kube-system rollout status deployment/metrics-server --timeout=180s

# Образы приложения собираются локально и загружаются в узлы kind.
make k8s-images
for img in dd-api dd-worker dd-migrate dd-fakepsp dd-web; do kind load docker-image --name "$CLUSTER" "$img:local"; done

k apply -k deploy/k8s/overlays/local
k -n $NS rollout status statefulset/postgres --timeout=300s
k -n $NS wait --for=condition=complete job/migrate --timeout=300s
# Все покупатели k6 приходят с одного адреса (ADR 020); скорость очереди — как в ADR 026.
k -n $NS set env deployment/api IP_TICKET_LIMIT=0 QUEUE_ADMIT_PER_SECOND="$RATE"
if [[ -n $API_CPU ]]; then k -n $NS set resources deployment/api --requests=cpu="$API_CPU"; fi
for d in api edge worker web; do k -n $NS rollout status deployment/$d --timeout=300s; done

# Покупатели и событие создаются через проброшенные порты PostgreSQL и
# Redis: сессии покупателей живут в обоих (ADR 018).
k -n $NS port-forward svc/postgres "$PG_PORT:5432" >/dev/null 2>&1 &
PF=$!
k -n $NS port-forward svc/redis "$REDIS_PORT:6379" >/dev/null 2>&1 &
PF_REDIS=$!
REC=
cleanup() {
  kill "$PF" "$PF_REDIS" 2>/dev/null || true
  [[ -n $REC ]] && kill "$REC" 2>/dev/null || true
}
trap cleanup EXIT
export DATABASE_URL="postgres://dd:dd@localhost:$PG_PORT/dd?sslmode=disable"
export REDIS_ADDR="localhost:$REDIS_PORT"
for port in "$PG_PORT" "$REDIS_PORT"; do
  for _ in $(seq 1 30); do (exec 3<>/dev/tcp/127.0.0.1/"$port") 2>/dev/null && break; sleep 1; done
done
go build -o "$DATA/loadseed" ./cmd/loadseed
timeout 600 "$DATA/loadseed" buyers -n "$N" -out "$dir/buyers.json"
timeout 600 "$DATA/loadseed" stadium -template concert -out "$dir/event.json"
event=$(python3 -c "import json;print(json.load(open('$dir/event.json'))['event_id'])")
start_ms=$(( ($(date +%s) + LEAD_S) * 1000 ))
k -n $NS exec statefulset/postgres -- psql -U dd -d dd -qc \
  "UPDATE events SET sales_start_at = to_timestamp($start_ms / 1000.0), waiting_room = true WHERE id = '$event'"

# Запись состояния HPA и подов каждые 5 секунд.
record() {
  echo "t_s,api_desired,api_current,api_ready,api_cpu,edge_desired,edge_current,edge_ready,edge_cpu,worker_desired,worker_current,worker_ready,worker_cpu,pending"
  while :; do
    {
      k -n $NS get hpa -o json
      echo '---'
      k -n $NS get deployments -o json
      echo '---'
      k -n $NS get pods --field-selector=status.phase=Pending -o name | wc -l
    } | python3 -c "
import json, sys, time
hpa, dep, pending = sys.stdin.read().split('---\n')
h = {i['metadata']['name']: i for i in json.loads(hpa)['items']}
d = {i['metadata']['name']: i for i in json.loads(dep)['items']}
row = [str(round(time.time() - $start_ms / 1000))]
for n in ('api', 'edge', 'worker'):
    st = h[n].get('status', {})
    cpu = next((m['resource']['current'].get('averageUtilization', '') for m in st.get('currentMetrics') or [] if m.get('type') == 'Resource'), '')
    row += [str(st.get('desiredReplicas', '')), str(d[n]['status'].get('replicas', 0)), str(d[n]['status'].get('readyReplicas', 0)), str(cpu)]
row.append(pending.strip())
print(','.join(row), flush=True)
" || true
    sleep 5
  done
}
record >"$dir/hpa.csv" &
REC=$!

# k6 внутри кластера: сценарий и данные — из ConfigMap.
# create, а не apply: apply хранит копию объекта в аннотации, а токены
# покупателей не помещаются в её лимит 256 КБ.
k -n $NS delete job k6 --ignore-not-found >/dev/null
k -n $NS delete configmap k6-storm --ignore-not-found >/dev/null
k -n $NS create configmap k6-storm --from-file=stadium.js=loadtest/stadium.js \
  --from-file=event.json="$dir/event.json" --from-file=buyers.json="$dir/buyers.json" >/dev/null
k -n $NS apply -f - >/dev/null <<YAML
apiVersion: batch/v1
kind: Job
metadata:
  name: k6
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: k6
          image: $K6_IMAGE
          args: [run, --quiet, /data/stadium.js]
          env:
            - {name: API, value: "http://edge:8080"}
            - {name: VUS, value: "$N"}
            - {name: QUEUE, value: "1"}
            - {name: START_AT, value: "$start_ms"}
            - {name: EVENT, value: /data/event.json}
            - {name: BUYERS, value: /data/buyers.json}
            - {name: SUMMARY, value: /tmp/summary.json}
          volumeMounts:
            - {name: data, mountPath: /data}
      volumes:
        - name: data
          configMap: {name: k6-storm}
YAML
k -n $NS wait --for=condition=complete job/k6 --timeout=$((LEAD_S + 900))s || true
# Последняя строка вывода k6 — сводка (handleSummary пишет её в stdout).
k -n $NS logs job/k6 | tail -1 >"$dir/summary.json"
end_s=$(( $(date +%s) - start_ms / 1000 ))
# Инвариант — сразу после штурма: через 10 минут неоплаченные заказы
# истекают (холд, ADR 011), и проверять стало бы нечего.
if "$DATA/loadseed" check -event "$event" -out "$dir/check.json"; then verdict=ok; else verdict=VIOLATED; fi
echo "storm finished at t=${end_s}s, invariant $verdict, recording scale-down for ${COOLDOWN_S}s"
sleep "$COOLDOWN_S"
kill "$REC" 2>/dev/null || true
REC=

python3 - "$dir/hpa.csv" "$end_s" "$verdict" <<'PY'
import csv, sys
rows = list(csv.DictReader(open(sys.argv[1])))
end = int(sys.argv[2])
for n in ('api', 'edge', 'worker'):
    cur = [(int(r['t_s']), int(r[f'{n}_ready'] or 0)) for r in rows]
    if not cur:
        continue
    base = cur[0][1]
    peak = max(c for _, c in cur)
    up = next((t for t, c in cur if c > base), None)
    down = next((t for t, c in cur if t > end and c == base), None)
    print(f'{n}: ready {base} -> {peak}, first scale-up t={up}s, back to {base} t={down}s')
print(f'storm end t={end}s, max pending pods {max(int(r["pending"] or 0) for r in rows)}, invariant {sys.argv[3]}')
PY
