// Отказ экземпляра api посреди продажи (этап 4, ADR 027). Постоянный поток
// заказов RATE в секунду через балансировщик на два экземпляра; скрипт
// снаружи убивает один экземпляр и поднимает его снова.
//
// Каждый заказ делает свой покупатель (покупатель = номер итерации), поэтому
// «два заказа у одного покупателя» после прогона — это двойное выполнение
// одного запроса. Клиент ведёт себя как сайт (web/src/lib/retry.ts): при
// обрыве, 502/503/504 и «запрос ещё выполняется» повторяет тот же запрос с
// тем же ключом идемпотентности. Пауза — сколько просит Retry-After (не
// больше 5 с), без него 0,25 с и вдвое больше, не дольше 4 с, умноженная на
// случайное число от 0,5 до 1,5; всего не дольше RETRY_FOR секунд.
import http from 'k6/http';
import exec from 'k6/execution';
import { sleep } from 'k6';
import { SharedArray } from 'k6/data';
import { Counter, Trend } from 'k6/metrics';

const BASE = __ENV.API || 'http://localhost:8080';
const RATE = Number(__ENV.RATE || 100);
const DURATION = __ENV.DURATION || '60s';
const RETRY_FOR = Number(__ENV.RETRY_FOR || 30) * 1000;

const buyers = new SharedArray('buyers', () => JSON.parse(open(__ENV.BUYERS)).tokens);
const ev = JSON.parse(open(__ENV.EVENT));

export const options = {
  scenarios: {
    // VU с запасом: пока база переключается, покупатели ждут повторов, и без
    // запаса k6 пропускает новые итерации.
    flow: { executor: 'constant-arrival-rate', rate: RATE, timeUnit: '1s', duration: DURATION, preAllocatedVUs: RATE * 20, maxVUs: RATE * 40 },
  },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
  systemTags: ['status', 'name'],
};

// Каждая попытка — точка во времени с исходом: из них строится график по
// секундам (вывод k6 в CSV).
const attempt = new Counter('chaos_attempt');
const okFirst = new Counter('chaos_ok_first');
const okRetry = new Counter('chaos_ok_retry');
const taken = new Counter('chaos_taken');
const gaveUp = new Counter('chaos_gave_up');
const retries = new Counter('chaos_retries');
const recovered = new Trend('chaos_recovery_ms', true); // от первой попытки до успеха после повторов
const orderTime = new Trend('chaos_order_ms', true);
// Подтверждённый заказ с его id: после переключения базы проверяется, что
// каждый подтверждённый клиенту заказ в ней есть (ADR 028).
const created = new Counter('chaos_created');

function kind(res) {
  if (res.status === 0) return 'network';
  if (res.status === 201) return 'created';
  if (res.status >= 500) return `http_${res.status}`;
  if (res.status === 409) {
    try {
      return res.json().error.code;
    } catch (_) {
      return 'http_409';
    }
  }
  return `http_${res.status}`;
}

const retryable = new Set(['network', 'http_502', 'http_503', 'http_504', 'request_in_progress', 'order_in_progress']);

export default function () {
  const idx = exec.scenario.iterationInTest;
  const token = buyers[idx % buyers.length];
  const seat = {
    section: 'Партер',
    row: String(1 + Math.floor(Math.random() * ev.rows)),
    seat: String(1 + Math.floor(Math.random() * ev.seats_per_row)),
  };
  const body = JSON.stringify({ seats: [seat], general: [], email: 'load@example.com' });
  const key = `chaos-${idx}-${Math.random()}`;
  const start = Date.now();
  for (let i = 0; ; i++) {
    const res = http.post(`${BASE}/v1/events/${ev.event_id}/orders`, body, {
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}`, 'Idempotency-Key': key },
      timeout: '20s',
      tags: { name: 'create_order' },
    });
    const k = kind(res);
    attempt.add(1, { outcome: k, try: i === 0 ? 'first' : 'retry' });
    orderTime.add(res.timings.duration);
    if (k === 'created') {
      try {
        created.add(1, { order: res.json().id });
      } catch (_) {
        // тело не разобрать — проверка потери его не учтёт
      }
      if (i === 0) okFirst.add(1);
      else {
        okRetry.add(1);
        recovered.add(Date.now() - start);
      }
      return;
    }
    if (k === 'seat_taken') {
      taken.add(1);
      return;
    }
    if (!retryable.has(k)) {
      gaveUp.add(1, { outcome: k });
      return;
    }
    const ra = Number(res.headers['Retry-After']);
    const base = res.headers['Retry-After'] !== undefined && Number.isFinite(ra) ? Math.min(ra, 5) : Math.min(0.25 * 2 ** i, 4);
    const wait = base * (0.5 + Math.random());
    if (Date.now() + wait * 1000 - start > RETRY_FOR) {
      gaveUp.add(1, { outcome: 'tries' });
      return;
    }
    retries.add(1);
    sleep(wait);
  }
}

export function handleSummary(data) {
  const m = (n) => (data.metrics[n] ? data.metrics[n].values : {});
  const c = (n) => m(n).count ?? 0;
  const s = {
    rate: RATE,
    requests: c('iterations'),
    ok_first: c('chaos_ok_first'),
    ok_retry: c('chaos_ok_retry'),
    taken: c('chaos_taken'),
    gave_up: c('chaos_gave_up'),
    retries: c('chaos_retries'),
    attempts: c('chaos_attempt'),
    dropped: c('dropped_iterations'),
    recovery_ms: m('chaos_recovery_ms'),
    order_ms: m('chaos_order_ms'),
  };
  return { [__ENV.SUMMARY || 'summary.json']: JSON.stringify(s, null, 1), stdout: JSON.stringify(s) + '\n' };
}
