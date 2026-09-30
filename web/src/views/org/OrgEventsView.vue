<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { orgApi } from '@/api/client'
import type { EventReport, OrgEvent, OrgVenue } from '@/api/types'
import { dayMonth, money, time, weekday } from '@/lib/format'

const events = ref<OrgEvent[] | null>(null)
const venues = ref<OrgVenue[]>([])
const reports = ref<Record<string, EventReport>>({})
const failed = ref(false)

const venueOf = (id: string) => venues.value.find((v) => v.id === id)
const tz = (e: OrgEvent) => venueOf(e.venue_id)?.timezone ?? 'UTC'

async function load() {
  failed.value = false
  try {
    const [es, vs] = await Promise.all([orgApi.events(), orgApi.venues()])
    events.value = es
    venues.value = vs
    // Продажи — по событиям в продаже; отчёт лёгкий, их немного.
    const live = es.filter((e) => e.status === 'published' && e.admission === 'ticketed').slice(0, 24)
    const got = await Promise.all(live.map((e) => orgApi.report(e.id).catch(() => null)))
    reports.value = Object.fromEntries(got.filter((r): r is EventReport => !!r).map((r) => [r.event.id, r]))
  } catch {
    failed.value = true
  }
}
onMounted(load)

const now = Date.now()
const groups = computed(() => {
  const list = events.value ?? []
  const upcoming = (e: OrgEvent) => Date.parse(e.ends_at) > now
  return [
    { key: 'live', title: 'В продаже', items: list.filter((e) => e.status === 'published' && upcoming(e)) },
    { key: 'draft', title: 'Черновики', items: list.filter((e) => e.status === 'draft') },
    { key: 'past', title: 'Прошедшие и отменённые', items: list.filter((e) => e.status === 'cancelled' || (e.status === 'published' && !upcoming(e))) },
  ].filter((g) => g.items.length)
})

const totals = computed(() => {
  const rs = Object.values(reports.value)
  return {
    sold: rs.reduce((n, r) => n + r.seats.sold, 0),
    net: rs.reduce((n, r) => n + r.money.net_tiyn, 0),
    live: groups.value.find((g) => g.key === 'live')?.items.length ?? 0,
  }
})

const statusLabel = { draft: 'Черновик', published: 'В продаже', cancelled: 'Отменено' } as const
</script>

<template>
  <div class="events">
    <header class="head">
      <div>
        <p class="eyebrow">Кабинет</p>
        <h1 class="title">События</h1>
      </div>
      <RouterLink v-if="events?.length" :to="{ name: 'org-event-new' }" class="btn btn--accent">+ Новое событие</RouterLink>
    </header>

    <div v-if="failed" class="panel">
      <p>Не удалось загрузить события.</p>
      <button class="btn btn--sm" type="button" @click="load">Попробовать снова</button>
    </div>

    <div v-else-if="!events" class="list" aria-busy="true">
      <div v-for="n in 3" :key="n" class="skeleton" style="height: 88px"></div>
    </div>

    <!-- Первый вход: три шага до первой продажи. -->
    <section v-else-if="events.length === 0" class="start">
      <h2 class="start__title">Три шага до первой продажи</h2>
      <ol class="steps">
        <li :class="{ done: venues.length > 0 }">
          <span class="steps__n mono">01</span>
          <div>
            <b>Площадка</b>
            <p>Адрес и часовой пояс — время события покупатели увидят по местному времени зала.</p>
            <RouterLink :to="{ name: 'org-venues' }" class="btn btn--sm" :class="{ 'btn--accent': venues.length === 0 }">
              {{ venues.length ? 'Площадки' : 'Добавить площадку' }}
            </RouterLink>
          </div>
        </li>
        <li>
          <span class="steps__n mono">02</span>
          <div>
            <b>Схема зала</b>
            <p>Сектора, ряды и места — или входная зона без мест. Собирается за пару минут.</p>
          </div>
        </li>
        <li>
          <span class="steps__n mono">03</span>
          <div>
            <b>Событие</b>
            <p>Дата, цены по секторам и обложка. После публикации билеты сразу в продаже.</p>
            <RouterLink v-if="venues.length" :to="{ name: 'org-event-new' }" class="btn btn--sm btn--accent">Создать событие</RouterLink>
          </div>
        </li>
      </ol>
    </section>

    <template v-else>
      <dl v-if="totals.live" class="kpis">
        <div>
          <dt class="eyebrow">В продаже</dt>
          <dd>{{ totals.live }}</dd>
        </div>
        <div>
          <dt class="eyebrow">Продано мест</dt>
          <dd>{{ totals.sold }}</dd>
        </div>
        <div>
          <dt class="eyebrow">Выручка после возвратов</dt>
          <dd class="mono">{{ money(totals.net) }}</dd>
        </div>
      </dl>

      <section v-for="g in groups" :key="g.key" class="group">
        <h2 class="group__title">{{ g.title }} <span class="mono">{{ g.items.length }}</span></h2>
        <ul class="list">
          <li v-for="e in g.items" :key="e.id">
            <RouterLink :to="{ name: 'org-event', params: { id: e.id } }" class="row" :class="{ 'row--dim': g.key === 'past' }">
              <span class="row__cover">
                <img v-if="e.cover_image_url" :src="e.cover_image_url" alt="" loading="lazy" />
                <span v-else class="mono">{{ e.title.slice(0, 1) }}</span>
              </span>
              <span class="row__date">
                <b>{{ dayMonth(e.starts_at, tz(e)) }}</b>
                <span class="mono">{{ weekday(e.starts_at, tz(e)) }} {{ time(e.starts_at, tz(e)) }}</span>
              </span>
              <span class="row__main">
                <span class="row__title">{{ e.title || 'Без названия' }}</span>
                <span class="row__meta">{{ venueOf(e.venue_id)?.name ?? '—' }}<template v-if="e.admission === 'free_entry'"> · свободный вход</template></span>
              </span>
              <span v-if="reports[e.id]" class="row__sales">
                <span class="bar" :style="{ '--p': reports[e.id]!.seats.occupancy_permille / 10 + '%' }"></span>
                <span class="mono">{{ reports[e.id]!.seats.sold }} / {{ reports[e.id]!.seats.capacity }}</span>
              </span>
              <span v-else class="row__sales"></span>
              <span class="chip" :class="`chip--${e.status}`">{{ statusLabel[e.status] }}</span>
            </RouterLink>
          </li>
        </ul>
      </section>
    </template>
  </div>
</template>

<style scoped>
.events {
  display: grid;
  gap: var(--space-6);
  max-width: 1080px;
}
.head {
  display: flex;
  align-items: end;
  justify-content: space-between;
  gap: var(--space-4);
  flex-wrap: wrap;
}
.title {
  font-size: var(--text-3xl);
  margin-top: var(--space-1);
}
.panel {
  padding: var(--space-5);
  border-radius: var(--radius-lg);
  background: var(--paper-2);
  display: flex;
  gap: var(--space-4);
  align-items: center;
}
.kpis {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  margin: 0;
  border-top: 1px solid var(--ink);
  border-bottom: 1px solid var(--line);
}
.kpis > div {
  padding: var(--space-4) var(--space-4) var(--space-4) 0;
}
.kpis dd {
  margin: 6px 0 0;
  font-size: var(--text-2xl);
  font-weight: 700;
  letter-spacing: -0.03em;
}
.group {
  display: grid;
  gap: var(--space-2);
}
.group__title {
  font-size: var(--text-sm);
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--ink-2);
  font-weight: 600;
}
.group__title .mono {
  color: var(--ink-3);
  margin-left: 6px;
}
.list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: var(--space-2);
}
.row {
  display: grid;
  grid-template-columns: 48px 120px 1fr 160px 110px;
  gap: var(--space-4);
  align-items: center;
  padding: var(--space-3);
  border-radius: var(--radius);
  background: var(--paper);
  border: 1px solid var(--line);
  text-decoration: none;
  color: inherit;
  transition:
    border-color 0.15s,
    transform 0.15s var(--ease);
}
.row:hover {
  border-color: var(--line-strong);
  transform: translateY(-1px);
}
.row--dim {
  opacity: 0.65;
}
.row__cover {
  width: 48px;
  height: 60px;
  border-radius: var(--radius-sm);
  overflow: hidden;
  background: var(--ink);
  color: var(--paper);
  display: grid;
  place-items: center;
  font-size: var(--text-xl);
}
.row__cover img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.row__date {
  display: grid;
  font-size: var(--text-sm);
  color: var(--ink-2);
}
.row__date b {
  color: var(--ink);
}
.row__main {
  display: grid;
  gap: 2px;
  min-width: 0;
}
.row__title {
  font-weight: 700;
  letter-spacing: -0.02em;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.row__meta {
  font-size: var(--text-sm);
  color: var(--ink-2);
}
.row__sales {
  display: grid;
  gap: 6px;
  font-size: var(--text-xs);
  color: var(--ink-2);
}
.bar {
  height: 6px;
  border-radius: 99px;
  background: linear-gradient(90deg, var(--accent) var(--p), var(--paper-3) var(--p));
}
.row .chip {
  justify-self: end;
}
@media (max-width: 760px) {
  .row {
    grid-template-columns: 48px 1fr auto;
  }
  .row__date {
    grid-column: 2;
    grid-row: 2;
    display: flex;
    gap: 6px;
  }
  .row__cover {
    grid-row: span 2;
  }
  .row__sales {
    display: none;
  }
}
.start {
  display: grid;
  gap: var(--space-5);
  padding: clamp(20px, 4vw, 40px);
  border-radius: var(--radius-lg);
  background: var(--paper-2);
}
.start__title {
  font-size: var(--text-2xl);
}
.steps {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: var(--space-4);
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
}
.steps li {
  display: flex;
  gap: var(--space-3);
  padding: var(--space-4);
  background: var(--paper);
  border-radius: var(--radius);
}
.steps li > div {
  display: grid;
  gap: var(--space-2);
  justify-items: start;
}
.steps p {
  color: var(--ink-2);
  font-size: var(--text-sm);
}
.steps__n {
  color: var(--accent);
  font-weight: 600;
}
.steps li.done .steps__n::after {
  content: ' ✓';
  color: var(--ok);
}
</style>
