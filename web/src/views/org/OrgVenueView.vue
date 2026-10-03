<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import SeatMapView from '@/components/SeatMap.vue'
import StadiumPlanView from '@/components/StadiumPlan.vue'
import VenueForm from '@/components/org/VenueForm.vue'
import { ApiError, orgApi } from '@/api/client'
import type { OrgSeatMap, OrgVenue, VenueInput } from '@/api/types'
import { useToast } from '@/composables/toast'
import { explain } from '@/lib/eventForm'
import { buildSeatMap, emptyCart } from '@/lib/seatmap'
import { buildPlan } from '@/lib/plan'
import { plural } from '@/lib/format'

const props = defineProps<{ id: string }>()
const toast = useToast()
const venue = ref<OrgVenue | null>(null)
const maps = ref<OrgSeatMap[]>([])
const missing = ref(false)
const editing = ref(false)
const busy = ref(false)
const error = ref('')
const open = ref<string | null>(null)

watch(
  () => props.id,
  async (id) => {
    try {
      const [v, ms] = await Promise.all([orgApi.venue(id), orgApi.seatMaps(id)])
      venue.value = v
      maps.value = ms
      open.value = ms[0]?.id ?? null
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) missing.value = true
    }
  },
  { immediate: true },
)

async function save(v: VenueInput) {
  busy.value = true
  error.value = ''
  try {
    venue.value = await orgApi.updateVenue(props.id, v)
    editing.value = false
    toast.show('Сохранено', 'ok')
  } catch (e) {
    error.value = explain(e)
  } finally {
    busy.value = false
  }
}

const preview = computed(() => {
  const m = maps.value.find((x) => x.id === open.value)
  // У стадиона превью — план секторов, а не 24 тысячи мест (ADR 024).
  return m && !m.layout.plan ? buildSeatMap(m.layout, []) : null
})
const previewPlan = computed(() => {
  const m = maps.value.find((x) => x.id === open.value)
  return m ? buildPlan(m.layout, [], undefined) : null
})
const noTaken = new Set<string>()
const cart = emptyCart()
const summary = (m: OrgSeatMap) =>
  m.layout.plan
    ? planSummary(m)
    : m.layout.sections.map((s) => (s.kind === 'general' ? `${s.name} · ${s.capacity} вход` : `${s.name} · ${s.rows?.length ?? 0} р.`)).join('  ·  ')

function planSummary(m: OrgSeatMap): string {
  const stands = new Set(m.layout.sections.map((s) => s.stand).filter(Boolean))
  return `${stands.size} ${plural(stands.size, 'трибуна', 'трибуны', 'трибун')} · ${m.layout.sections.length} ${plural(m.layout.sections.length, 'сектор', 'сектора', 'секторов')}`
}
</script>

<template>
  <div v-if="missing" class="page-missing"><h1>Площадка не найдена</h1></div>
  <div v-else-if="!venue" class="skeleton" style="height: 400px; max-width: 1080px"></div>
  <div v-else class="venue">
    <RouterLink :to="{ name: 'org-venues' }" class="back">← Площадки</RouterLink>

    <header class="head">
      <div v-if="!editing">
        <p class="eyebrow mono">{{ venue.timezone }}</p>
        <h1 class="title">{{ venue.name }}</h1>
        <p class="addr">{{ venue.address || 'Адрес не указан' }}</p>
      </div>
      <div class="head__actions" v-if="!editing">
        <button type="button" class="btn btn--sm btn--ghost" @click="editing = true">Изменить</button>
        <RouterLink v-if="maps.length" :to="{ name: 'org-event-new', query: { venue: venue.id } }" class="btn btn--sm btn--accent">Событие здесь</RouterLink>
      </div>
    </header>

    <section v-if="editing" class="card">
      <VenueForm :venue="venue" submit-label="Сохранить" :busy="busy" :error="error" @submit="save" @cancel="editing = false" />
    </section>

    <section class="maps">
      <div class="maps__head">
        <h2 class="h2">Схемы зала</h2>
        <RouterLink :to="{ name: 'org-seatmap-new', params: { id: venue.id } }" class="btn btn--sm">+ Новая схема</RouterLink>
      </div>
      <p class="muted">
        Схема — сектора с рядами и местами или входные зоны. Одну схему можно использовать во многих событиях; у опубликованного события
        схема не меняется, поэтому новая рассадка — это новая схема.
      </p>

      <div v-if="maps.length === 0" class="empty">
        <p>У площадки пока нет схемы. Соберите её из секторов — это пара минут.</p>
        <RouterLink :to="{ name: 'org-seatmap-new', params: { id: venue.id } }" class="btn btn--accent">Собрать схему зала</RouterLink>
      </div>

      <div v-else class="maps__body">
        <ul class="maps__list" role="tablist">
          <li v-for="m in maps" :key="m.id">
            <button type="button" role="tab" class="map" :class="{ 'is-on': open === m.id }" :aria-selected="open === m.id" @click="open = m.id">
              <span class="map__name">{{ m.name }}</span>
              <span class="map__count mono">{{ m.seat_count }} мест</span>
              <span class="map__sum">{{ summary(m) }}</span>
            </button>
          </li>
        </ul>
        <div class="maps__preview">
          <StadiumPlanView v-if="previewPlan" :plan="previewPlan" hint="План площадки: наведите на сектор, чтобы увидеть трибуну" />
          <SeatMapView v-if="preview?.sections.length" :key="open ?? ''" :map="preview" :taken="noTaken" :cart="cart" disabled />
          <ul v-if="preview?.zones.length" class="zones">
            <li v-for="z in preview.zones" :key="z.name"><b>{{ z.name }}</b> <span class="mono">вход · {{ z.capacity }} чел.</span></li>
          </ul>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.venue {
  display: grid;
  gap: var(--space-5);
  max-width: 1080px;
}
.back {
  font-size: var(--text-sm);
  color: var(--ink-2);
  text-decoration: none;
}
.head {
  display: flex;
  align-items: end;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: var(--space-4);
}
.head__actions {
  display: flex;
  gap: var(--space-2);
}
.title {
  font-size: var(--text-3xl);
  margin: var(--space-1) 0 var(--space-2);
}
.addr,
.muted {
  color: var(--ink-2);
}
.muted {
  font-size: var(--text-sm);
  max-width: 70ch;
}
.card {
  padding: var(--space-5);
  border: 1px solid var(--line);
  border-radius: var(--radius-lg);
  max-width: 720px;
}
.maps {
  display: grid;
  gap: var(--space-3);
}
.maps__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.h2 {
  font-size: var(--text-xl);
}
.empty {
  display: grid;
  gap: var(--space-4);
  justify-items: start;
  padding: var(--space-5);
  border-radius: var(--radius-lg);
  background: var(--paper-2);
}
.maps__body {
  display: grid;
  gap: var(--space-4);
  grid-template-columns: 260px minmax(0, 1fr);
  align-items: start;
}
@media (max-width: 800px) {
  .maps__body {
    grid-template-columns: 1fr;
  }
}
.maps__list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: var(--space-2);
}
.map {
  width: 100%;
  display: grid;
  gap: 2px;
  text-align: left;
  padding: var(--space-3) var(--space-4);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--paper);
  font: inherit;
  cursor: pointer;
}
.map.is-on {
  border-color: var(--ink);
  box-shadow: inset 3px 0 0 var(--accent);
}
.map__name {
  font-weight: 700;
}
.map__count {
  font-size: var(--text-xs);
  color: var(--accent);
}
.map__sum {
  font-size: var(--text-xs);
  color: var(--ink-3);
}
.maps__preview {
  display: grid;
  gap: var(--space-3);
  min-width: 0;
}
.zones {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.zones li {
  padding: var(--space-3) var(--space-4);
  border: 1px dashed var(--line-strong);
  border-radius: var(--radius);
  font-size: var(--text-sm);
}
.zones .mono {
  color: var(--ink-2);
  margin-left: 6px;
}
</style>
