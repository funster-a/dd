<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import VenueForm from '@/components/org/VenueForm.vue'
import { newKey, orgApi } from '@/api/client'
import type { OrgVenue, VenueInput } from '@/api/types'
import { useToast } from '@/composables/toast'
import { explain } from '@/lib/eventForm'

const route = useRoute()
const router = useRouter()
const toast = useToast()
const venues = ref<OrgVenue[] | null>(null)
const adding = ref(route.query.new === '1')
const busy = ref(false)
const error = ref('')
let key = newKey()

onMounted(async () => {
  venues.value = await orgApi.venues().catch(() => [])
  if (venues.value.length === 0) adding.value = true
})

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
