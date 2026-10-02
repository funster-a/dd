// Нагрузочный эксперимент: стратегии захвата мест (spec.md, ADR 017).
//
// Сценарии (SCENARIO):
//   one-seat — N покупателей одновременно берут одно и то же место в момент
//              старта продаж. Ровно один должен победить.
//   hall     — N покупателей одновременно штурмуют зал на 1000 мест: каждый
//              берёт случайное место, при отказе пробует другое (до 3 раз).
//              Меряет пропускную способность — успешных захватов в секунду.
//   queue    — тот же штурм зала, но у события объявлен старт продаж
//              (START_AT, мс): покупатели встают в очередь ожидания в
//              последние 15 секунд до старта, опрашивают своё место, как
//              сайт, и идут покупать, когда их пропустят (ADR 020). С
//              QUEUE=0 очередь не используется — сравнение «без очереди».
//
// Все VU ждут общий момент старта из setup(): так запросы приходят
// одновременно, а не по мере инициализации VU.
import http from 'k6/http';
import { sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';

const N = Number(__ENV.VUS || 500);
const SCENARIO = __ENV.SCENARIO || 'one-seat';
// WARMUP=1: перед стартом покупатель открывает свой профиль — как реальный
// покупатель, который заходит на страницу события до начала продаж.
const WARMUP = __ENV.WARMUP === '1';
const BASE = __ENV.API || 'http://localhost:8080';
const START_AT = Number(__ENV.START_AT || 0);
const USE_QUEUE = __ENV.QUEUE !== '0';
const buyers = JSON.parse(open(__ENV.BUYERS || '.data/buyers.json')).tokens;
const ev = JSON.parse(open(__ENV.EVENT || '.data/event.json'));

export const options = {
  scenarios: {
    rush: { executor: 'per-vu-iterations', vus: N, iterations: 1, maxDuration: '5m' },
  },
  setupTimeout: '2m',
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  discardResponseBodies: false,
};

const won = new Counter('booking_won');
const taken = new Counter('booking_taken');
const failed = new Counter('booking_failed');
const failed5xx = new Counter('booking_failed_5xx');
const failedNet = new Counter('booking_failed_network'); // таймаут или обрыв, статуса нет
const failed503 = new Counter('booking_failed_503'); // вход временно недоступен
const attempts = new Counter('booking_attempts');
const attemptTime = new Trend('booking_attempt_ms', true);
const wonTime = new Trend('booking_won_at_ms', true); // когда от старта получен успех
const queueWait = new Trend('queue_wait_ms', true); // от старта продаж до пропуска
const queuePolls = new Counter('queue_polls');
const queuePollTime = new Trend('queue_poll_ms', true);
const queueFailed = new Counter('queue_failed');

export function setup() {
  if (buyers.length < N) throw new Error(`need ${N} buyer tokens, have ${buyers.length}`);
  if (SCENARIO === 'queue') {
    if (!START_AT) throw new Error('START_AT is required for the queue scenario');
    return { startAt: START_AT };
  }
  // Запас на то, чтобы все VU дошли до ожидания.
  return { startAt: Date.now() + 2000 + N };
}

function order(token, seat) {
  const res = http.post(`${BASE}/v1/events/${ev.event_id}/orders`, JSON.stringify({ seats: [seat], general: [], email: 'load@example.com' }), {
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
      'Idempotency-Key': `${__VU}-${__ITER}-${seat.row}-${seat.seat}-${Math.random()}`,
    },
    timeout: '60s',
    tags: { name: 'create_order' },
  });
  attempts.add(1);
  attemptTime.add(res.timings.duration);
  return res;
}

function randomSeat() {
  const row = 1 + Math.floor(Math.random() * ev.rows);
  const seat = 1 + Math.floor(Math.random() * ev.seats_per_row);
  return { section: 'Партер', row: String(row), seat: String(seat) };
}

// waitTurn — очередь ожидания глазами сайта: встать, опрашивать место с
// интервалом, который советует сервер, и вернуться, когда пропустят.
function waitTurn(token, startAt) {
  const joinAt = startAt - Math.random() * 15000;
  if (joinAt > Date.now()) sleep((joinAt - Date.now()) / 1000);
  for (;;) {
    const res = http.post(`${BASE}/v1/events/${ev.event_id}/queue`, null, {
      headers: { Authorization: `Bearer ${token}` },
      timeout: '30s',
      tags: { name: 'queue' },
    });
    queuePolls.add(1);
    queuePollTime.add(res.timings.duration);
    if (res.status !== 200) {
      queueFailed.add(1);
      sleep(2);
      continue;
    }
    const st = res.json();
    if (st.state === 'admitted' || st.state === 'not_required') {
      queueWait.add(Math.max(0, Date.now() - startAt));
      return;
    }
    sleep(st.poll_after_seconds || 3);
  }
}

export default function (data) {
  if (SCENARIO === 'queue' && USE_QUEUE) {
    waitTurn(buyers[__VU - 1], data.startAt);
  }
  if (WARMUP) {
    http.get(`${BASE}/v1/me`, { headers: { Authorization: `Bearer ${buyers[__VU - 1]}` }, tags: { name: 'warmup' } });
  }
  const wait = data.startAt - Date.now();
  if (wait > 0) sleep(wait / 1000);
  const token = buyers[__VU - 1];
  const inHall = SCENARIO === 'hall' || SCENARIO === 'queue';
  const tries = inHall ? 3 : 1;
  for (let i = 0; i < tries; i++) {
    const res = order(token, inHall ? randomSeat() : ev.hot_seat);
    if (res.status === 201) {
      won.add(1);
      wonTime.add(Date.now() - data.startAt);
      return;
    }
    if (res.status === 409) {
      taken.add(1);
      continue;
    }
    failed.add(1);
    if (res.status >= 500) failed5xx.add(1);
    if (res.status === 0) failedNet.add(1);
    if (res.status === 503) failed503.add(1);
    return;
  }
}

export function handleSummary(data) {
  const out = __ENV.SUMMARY || 'summary.json';
  const m = (name) => data.metrics[name]?.values ?? {};
  const summary = {
    scenario: SCENARIO,
    vus: N,
    won: m('booking_won').count ?? 0,
    taken: m('booking_taken').count ?? 0,
    failed: m('booking_failed').count ?? 0,
    failed_5xx: m('booking_failed_5xx').count ?? 0,
    failed_network: m('booking_failed_network').count ?? 0,
    failed_503: m('booking_failed_503').count ?? 0,
    attempts: m('booking_attempts').count ?? 0,
    attempt_ms: m('booking_attempt_ms'),
    won_at_ms: m('booking_won_at_ms'),
    queue: USE_QUEUE && SCENARIO === 'queue',
    queue_wait_ms: m('queue_wait_ms'),
    queue_polls: m('queue_polls').count ?? 0,
    queue_poll_ms: m('queue_poll_ms'),
    queue_failed: m('queue_failed').count ?? 0,
    http_failed_rate: m('http_req_failed').rate ?? 0,
    duration_ms: data.state.testRunDurationMs,
  };
  return { [out]: JSON.stringify(summary, null, 1), stdout: `${JSON.stringify(summary)}\n` };
}
