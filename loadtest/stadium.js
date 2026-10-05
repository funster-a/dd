// Штурм стадиона (этап 3, ADR 026): концерт на Центральном стадионе Алматы
// (шаблон ADR 025, 33 509 мест — трибуны и фан-зоны на поле). N покупателей
// приходят к старту продаж и идут путём сайта:
//
//   до старта — открывают страницу события (схема ~400 КБ, в gzip ~11 КБ)
//   и профиль; с очередью — встают в неё и ждут пропуска;
//   на старте — сводка занятости по секторам (план стадиона), выбор фан-зоны
//   или сектора с учётом свободных мест, занятость сектора, 1–4 места рядом
//   в одном ряду и заказ. Место заняли — новая попытка, до четырёх.
//
// QUEUE=0 — без очереди. Сводка по каждой попытке, время успеха от старта и
// объём ответов — в SUMMARY.
import http from 'k6/http';
import { sleep } from 'k6';
import { SharedArray } from 'k6/data';
import { Counter, Trend } from 'k6/metrics';

const BASE = __ENV.API || 'http://localhost:8080';
const N = Number(__ENV.VUS || 1000);
const START_AT = Number(__ENV.START_AT || 0);
const USE_QUEUE = __ENV.QUEUE !== '0';
const MAX_TRIES = 4;

const buyers = new SharedArray('buyers', () => JSON.parse(open(__ENV.BUYERS)).tokens);
const ev = JSON.parse(open(__ENV.EVENT));
// Ряды каждого сектора: сколько мест; места подписаны 1..n.
const sectors = new SharedArray('sectors', () => JSON.parse(open(__ENV.EVENT)).sectors);
const rowsOf = {};
for (const s of sectors) rowsOf[s.name] = s.rows;
const eventPath = ev.path.replace(/^\/e\//, '/v1/public/events/');

export const options = {
  scenarios: {
    storm: { executor: 'per-vu-iterations', vus: N, iterations: 1, maxDuration: '10m' },
  },
  setupTimeout: '3m',
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

const won = new Counter('storm_won');
const ticketsWon = new Counter('storm_tickets');
const zoneTickets = new Counter('storm_zone_tickets');
const conflicts = new Counter('storm_conflicts');
// Причины отказа заказа: место заняли, в фан-зоне не хватило, другое.
const conflictSeat = new Counter('storm_conflict_seat_taken');
const conflictZone = new Counter('storm_conflict_not_enough');
const conflictStale = new Counter('storm_conflict_no_seats_in_view'); // в снимке сектора нет k мест рядом
const conflictOther = new Counter('storm_conflict_other');
const soldOut = new Counter('storm_gave_up_sold_out');
const triesOut = new Counter('storm_gave_up_tries');
const failed = new Counter('storm_failed');
const failed5xx = new Counter('storm_failed_5xx');
const failedNet = new Counter('storm_failed_network');
const attempts = new Counter('storm_attempts');
const wonAt = new Trend('storm_won_at_ms', true); // когда от старта продаж
const eventTime = new Trend('storm_event_ms', true);
const summaryTime = new Trend('storm_summary_ms', true);
const sectionTime = new Trend('storm_section_ms', true);
const orderTime = new Trend('storm_order_ms', true);
const summaryBytes = new Trend('storm_summary_bytes');
const sectionBytes = new Trend('storm_section_bytes');
const queueWait = new Trend('queue_wait_ms', true);
const queuePolls = new Counter('queue_polls');
const queueFailed = new Counter('queue_failed');

export function setup() {
  if (buyers.length < N) throw new Error(`need ${N} buyer tokens, have ${buyers.length}`);
  if (!START_AT) throw new Error('START_AT is required');
  return { startAt: START_AT };
}

const auth = (token) => ({ Authorization: `Bearer ${token}` });
// Браузер просит сжатие; k6 сам этого не делает и скачивал бы схему
// стадиона целиком — 400 КБ вместо 11.
const GZIP = { 'Accept-Encoding': 'gzip' };

// Сколько билетов хочет покупатель: чаще всего компанией по 2–4.
function wantTickets() {
  const r = Math.random();
  if (r < 0.1) return 1;
  if (r < 0.45) return 2;
  if (r < 0.65) return 3;
  return 4;
}

// pick — сектор или фан-зона с вероятностью по числу свободных мест;
// null — места на k человек не осталось нигде.
function pick(summary, k) {
  const cands = [];
  let total = 0;
  for (const s of summary.sections) if (s.available >= k) cands.push([s.section, s.available, 'seat']);
  for (const g of summary.general) if (g.available >= k) cands.push([g.section, g.available, 'general']);
  for (const c of cands) total += c[1];
  if (!total) return null;
  let x = Math.random() * total;
  for (const c of cands) {
    x -= c[1];
    if (x <= 0) return c;
  }
  return cands[cands.length - 1];
}

// seatsInSector — k свободных мест рядом в случайном ряду, где они есть.
function seatsInSector(name, taken, k) {
  const busy = new Set(taken.map((t) => `${t.row}|${t.seat}`));
  const rows = rowsOf[name] || [];
  const order = rows.map((_, i) => i).sort(() => Math.random() - 0.5);
  for (const ri of order) {
    const row = String(ri + 1);
    let run = [];
    const start = Math.floor(Math.random() * rows[ri]);
    for (let j = 0; j < rows[ri]; j++) {
      const seat = String(((start + j) % rows[ri]) + 1);
      if (busy.has(`${row}|${seat}`) || (run.length && Number(seat) !== Number(run[run.length - 1]) + 1)) run = [];
      if (!busy.has(`${row}|${seat}`)) run.push(seat);
      if (run.length === k) return run.map((s) => ({ section: name, row, seat: s }));
    }
  }
  return null;
}

function order(token, body) {
  const res = http.post(`${BASE}/v1/events/${ev.event_id}/orders`, JSON.stringify({ ...body, email: 'load@example.com' }), {
    headers: { 'Content-Type': 'application/json', ...auth(token), 'Idempotency-Key': `${__VU}-${Math.random()}` },
    timeout: '60s',
    tags: { name: 'create_order' },
  });
  attempts.add(1);
  orderTime.add(res.timings.duration);
  return res;
}

function waitTurn(token, startAt) {
  for (;;) {
    const res = http.post(`${BASE}/v1/events/${ev.event_id}/queue`, null, { headers: auth(token), timeout: '30s', tags: { name: 'queue' } });
    queuePolls.add(1);
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
  const token = buyers[__VU - 1];
  // Покупатель открывает страницу в случайный момент последних 30 секунд
  // до старта: схема и сессия загружаются до пика.
  const arrive = data.startAt - Math.random() * 30000;
  if (arrive > Date.now()) sleep((arrive - Date.now()) / 1000);
  const page = http.get(`${BASE}${eventPath}`, { headers: GZIP, tags: { name: 'event' }, timeout: '60s' });
  eventTime.add(page.timings.duration);
  http.get(`${BASE}/v1/me`, { headers: auth(token), tags: { name: 'me' } });
  if (USE_QUEUE) waitTurn(token, data.startAt);
  const wait = data.startAt - Date.now();
  if (wait > 0) sleep(wait / 1000);

  const k = wantTickets();
  for (let i = 0; i < MAX_TRIES; i++) {
    const sres = http.get(`${BASE}/v1/events/${ev.event_id}/availability?view=summary`, { headers: GZIP, tags: { name: 'summary' }, timeout: '60s' });
    summaryTime.add(sres.timings.duration);
    if (sres.status !== 200) {
      failed.add(1);
      if (sres.status >= 500) failed5xx.add(1);
      if (sres.status === 0) failedNet.add(1);
      return;
    }
    summaryBytes.add(sres.body.length);
    const target = pick(sres.json(), k);
    if (!target) {
      soldOut.add(1);
      return;
    }
    let body;
    if (target[2] === 'general') {
      body = { seats: [], general: [{ section: target[0], quantity: k }] };
    } else {
      const q = http.get(`${BASE}/v1/events/${ev.event_id}/availability?section=${encodeURIComponent(target[0])}`, {
        headers: GZIP,
        tags: { name: 'section' },
        timeout: '60s',
      });
      sectionTime.add(q.timings.duration);
      if (q.status !== 200) {
        failed.add(1);
        if (q.status >= 500) failed5xx.add(1);
        if (q.status === 0) failedNet.add(1);
        return;
      }
      sectionBytes.add(q.body.length);
      const seats = seatsInSector(target[0], q.json().taken, k);
      if (!seats) {
        conflicts.add(1);
        conflictStale.add(1);
        continue;
      }
      body = { seats, general: [] };
    }
    const res = order(token, body);
    if (res.status === 201) {
      won.add(1);
      ticketsWon.add(k);
      if (target[2] === 'general') zoneTickets.add(k);
      wonAt.add(Date.now() - data.startAt);
      return;
    }
    if (res.status === 409 || res.status === 422) {
      conflicts.add(1);
      let code = '';
      try {
        code = res.json().error.code;
      } catch (_) {
        // тело не JSON
      }
      if (code === 'seat_taken') conflictSeat.add(1);
      else if (code === 'not_enough_seats') conflictZone.add(1);
      else conflictOther.add(1);
      continue;
    }
    failed.add(1);
    if (res.status >= 500) failed5xx.add(1);
    if (res.status === 0) failedNet.add(1);
    return;
  }
  triesOut.add(1);
}

export function handleSummary(data) {
  const m = (name) => {
    const x = data.metrics[name];
    return x ? x.values : {};
  };
  const c = (name) => m(name).count ?? 0;
  const summary = {
    buyers: N,
    queue: USE_QUEUE,
    seats_total: ev.seats_total,
    won: c('storm_won'),
    tickets: c('storm_tickets'),
    zone_tickets: c('storm_zone_tickets'),
    conflicts: c('storm_conflicts'),
    conflict_seat_taken: c('storm_conflict_seat_taken'),
    conflict_not_enough: c('storm_conflict_not_enough'),
    conflict_no_seats_in_view: c('storm_conflict_no_seats_in_view'),
    conflict_other: c('storm_conflict_other'),
    gave_up_sold_out: c('storm_gave_up_sold_out'),
    gave_up_tries: c('storm_gave_up_tries'),
    failed: c('storm_failed'),
    failed_5xx: c('storm_failed_5xx'),
    failed_network: c('storm_failed_network'),
    attempts: c('storm_attempts'),
    won_at_ms: m('storm_won_at_ms'),
    event_ms: m('storm_event_ms'),
    summary_ms: m('storm_summary_ms'),
    section_ms: m('storm_section_ms'),
    order_ms: m('storm_order_ms'),
    summary_bytes: m('storm_summary_bytes'),
    section_bytes: m('storm_section_bytes'),
    data_received: c('data_received'),
    queue_wait_ms: m('queue_wait_ms'),
    queue_polls: c('queue_polls'),
    queue_failed: c('queue_failed'),
  };
  return { [__ENV.SUMMARY || 'summary.json']: JSON.stringify(summary, null, 1), stdout: JSON.stringify(summary) + '\n' };
}
