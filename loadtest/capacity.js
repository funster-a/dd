// Ёмкость api (ADR 023): постоянный поток заказов RATE в секунду в течение
// DURATION. Каждый заказ — случайный покупатель берёт случайное место в
// большом зале; новый заказ покупателя заменяет его прежнюю корзину, поэтому
// места возвращаются в оборот и зал не кончается. «Место занято» (409) —
// нормальный исход, ошибка — 5xx, обрыв или отказ в обслуживании.
//
// Ёмкость конфигурации — самый большой поток, при котором p95 ответа ниже
// SLO и ошибок меньше 1%. Если k6 не успевает выпускать запросы с заданной
// частотой (dropped_iterations), сервер уже не справляется.
import http from 'k6/http';
import { Counter, Trend } from 'k6/metrics';

const RATE = Number(__ENV.RATE || 100);
const DURATION = __ENV.DURATION || '20s';
const BASE = __ENV.API || 'http://localhost:8080';
const buyers = JSON.parse(open(__ENV.BUYERS || '.data/buyers.json')).tokens;
const ev = JSON.parse(open(__ENV.EVENT || '.data/event.json'));

export const options = {
  scenarios: {
    flow: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: Math.min(RATE * 2, 2000),
      maxVUs: 4000,
    },
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

const latency = new Trend('order_ms', true);
const created = new Counter('order_created');
const taken = new Counter('order_taken');
const errors = new Counter('order_errors');

export default function () {
  const token = buyers[Math.floor(Math.random() * buyers.length)];
  const seat = {
    section: 'Партер',
    row: String(1 + Math.floor(Math.random() * ev.rows)),
    seat: String(1 + Math.floor(Math.random() * ev.seats_per_row)),
  };
  const res = http.post(`${BASE}/v1/events/${ev.event_id}/orders`, JSON.stringify({ seats: [seat], general: [], email: 'load@example.com' }), {
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}`, 'Idempotency-Key': `${__VU}-${__ITER}-${Math.random()}` },
    timeout: '30s',
    tags: { name: 'create_order' },
  });
  latency.add(res.timings.duration);
  if (res.status === 201) created.add(1);
  else if (res.status === 409) taken.add(1);
  else errors.add(1);
}

export function handleSummary(data) {
  const m = (name) => data.metrics[name]?.values ?? {};
  const done = (m('order_created').count ?? 0) + (m('order_taken').count ?? 0) + (m('order_errors').count ?? 0);
  const summary = {
    rate: RATE,
    duration_ms: data.state.testRunDurationMs,
    requests: done,
    achieved_rps: done / (data.state.testRunDurationMs / 1000),
    created: m('order_created').count ?? 0,
    taken: m('order_taken').count ?? 0,
    errors: m('order_errors').count ?? 0,
    dropped: m('dropped_iterations').count ?? 0,
    order_ms: m('order_ms'),
  };
  return { [__ENV.SUMMARY || 'summary.json']: JSON.stringify(summary, null, 1), stdout: `${JSON.stringify(summary)}\n` };
}
