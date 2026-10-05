<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import VenueForm from '@/components/org/VenueForm.vue'
import { newKey, orgApi } from '@/api/client'
import type { OrgVenue, SeatMapTemplate, VenueInput } from '@/api/types'
import { useToast } from '@/composables/toast'
import { explain } from '@/lib/eventForm'
import { plural } from '@/lib/format'

const route = useRoute()
const router = useRouter()
const toast = useToast()
const venues = ref<OrgVenue[] | null>(null)
const adding = ref(route.query.new === '1')
const busy = ref(false)
const error = ref('')
let key = newKey()

const templates = ref<SeatMapTemplate[]>([])
const fromTemplate = ref('')
let templateKeys = { venue: newKey(), map: newKey() }

onMounted(async () => {
  orgApi
    .seatMapTemplates()
    .then((t) => (templates.value = t))
    .catch(() => {})
  venues.value = await orgApi.venues().catch(() => [])
  if (venues.value.length === 0) adding.value = true
})

// Площадка по шаблону (ADR 024): площадка и готовая схема в один шаг.
// Ключи идемпотентности общие на попытку: повтор после сбоя сети не
// создаст вторую площадку.
async function createFromTemplate(t: SeatMapTemplate) {
  fromTemplate.value = t.id
  error.value = ''
  try {
    const full = await orgApi.seatMapTemplate(t.id)
    const venue = await orgApi.createVenue(
      { name: t.venue_name, address: t.address, timezone: 'Asia/Almaty', latitude: null, longitude: null },
      templateKeys.venue,
    )
    await orgApi.createSeatMap(venue.id, t.name, full.layout!, templateKeys.map)
    templateKeys = { venue: newKey(), map: newKey() }
    toast.show('Площадка со схемой добавлена', 'ok')
    await router.push({ name: 'org-venue', params: { id: venue.id } })
  } catch (e) {
    toast.show(explain(e), 'error')
  } finally {
    fromTemplate.value = ''
  }
}

async function create(v: VenueInput) {
  busy.value = true
  error.value = ''
  try {
    const created = await orgApi.createVenue(v, key)
    key = newKey()
    toast.show('Площадка добавлена', 'ok')
    // Дальше площадке нужна схема зала.
    await router.push({ name: 'org-venue', params: { id: created.id } })
  } catch (e) {
    error.value = explain(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="venues">
    <header class="head">
      <div>
        <p class="eyebrow">Кабинет</p>
        <h1 class="title">Площадки</h1>
      </div>
      <button v-if="!adding" type="button" class="btn btn--accent" @click="adding = true">+ Площадка</button>
    </header>

    <section v-if="adding" class="card card--form">
      <h2 class="h2">Новая площадка</h2>
      <VenueForm submit-label="Добавить" :busy="busy" :error="error" @submit="create" @cancel="adding = false" />
    </section>

    <section v-if="templates.length" class="templates">
      <h2 class="h2">Готовые схемы площадок</h2>
      <ul class="grid">
        <li v-for="t in templates" :key="t.id" class="card card--template">
          <span class="card__name">{{ t.venue_name }}</span>
          <span class="card__addr">{{ t.address }}</span>
          <span class="card__tz mono">{{ t.seat_count.toLocaleString('ru-RU') }} {{ plural(t.seat_count, 'место', 'места', 'мест') }}</span>
          <span class="card__note">{{ t.note }}</span>
          <button type="button" class="btn btn--sm" :disabled="!!fromTemplate" @click="createFromTemplate(t)">
            {{ fromTemplate === t.id ? 'Добавляем…' : 'Добавить со схемой' }}
          </button>
        </li>
      </ul>
    </section>

    <div v-if="!venues" class="grid">
      <div v-for="n in 2" :key="n" class="skeleton" style="height: 140px"></div>
    </div>
    <ul v-else-if="venues.length" class="grid">
      <li v-for="v in venues" :key="v.id">
        <RouterLink :to="{ name: 'org-venue', params: { id: v.id } }" class="card card--venue">
          <span class="card__icon" aria-hidden="true">
            <svg viewBox="0 0 40 40"><path d="M6 34V16l14-9 14 9v18M14 34v-9h12v9" /></svg>
          </span>
          <span class="card__name">{{ v.name }}</span>
          <span class="card__addr">{{ v.address || 'Адрес не указан' }}</span>
          <span class="card__tz mono">{{ v.timezone }}</span>
        </RouterLink>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.venues {
  display: grid;
  gap: var(--space-5);
  max-width: 1080px;
}
.head {
  display: flex;
  align-items: end;
  justify-content: space-between;
  gap: var(--space-4);
}
.title {
  font-size: var(--text-3xl);
  margin-top: var(--space-1);
}
.h2 {
  font-size: var(--text-xl);
  margin-bottom: var(--space-4);
}
.grid {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: var(--space-4);
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
}
.card {
  display: grid;
  gap: 4px;
  padding: var(--space-5);
  border: 1px solid var(--line);
  border-radius: var(--radius-lg);
  background: var(--paper);
}
.card--form {
  max-width: 720px;
}
.card--template .btn {
  justify-self: start;
  margin-top: var(--space-3);
}
.card__note {
  margin-top: var(--space-2);
  font-size: var(--text-xs);
  color: var(--ink-3);
}
.card--venue {
  color: inherit;
  text-decoration: none;
  transition:
    border-color 0.15s,
    transform 0.15s var(--ease);
}
.card--venue:hover {
  border-color: var(--line-strong);
  transform: translateY(-2px);
}
.card__icon svg {
  width: 36px;
  height: 36px;
  fill: none;
  stroke: var(--accent);
  stroke-width: 1.8;
  stroke-linejoin: round;
  margin-bottom: var(--space-3);
}
.card__name {
  font-weight: 700;
  font-size: var(--text-lg);
  letter-spacing: -0.02em;
}
.card__addr {
  color: var(--ink-2);
  font-size: var(--text-sm);
}
.card__tz {
  margin-top: var(--space-2);
  font-size: var(--text-xs);
  color: var(--ink-3);
}
</style>
