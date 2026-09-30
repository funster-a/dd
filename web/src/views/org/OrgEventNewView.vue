<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import EventFormFields from '@/components/org/EventFormFields.vue'
import { newKey, orgApi } from '@/api/client'
import type { OrgVenue } from '@/api/types'
import { useProfile } from '@/composables/profile'
import { useToast } from '@/composables/toast'
import { emptyForm, explain, toInput } from '@/lib/eventForm'

const router = useRouter()
const route = useRoute()
const toast = useToast()
const profile = useProfile()

const venues = ref<OrgVenue[] | null>(null)
const form = reactive(emptyForm())
const busy = ref(false)
const error = ref('')
const key = newKey()

onMounted(async () => {
  venues.value = await orgApi.venues().catch(() => [])
  const preset = typeof route.query.venue === 'string' ? route.query.venue : ''
  form.venue_id = venues.value.find((v) => v.id === preset)?.id ?? (venues.value.length === 1 ? venues.value[0]!.id : '')
})

async function submit() {
  const tz = venues.value?.find((v) => v.id === form.venue_id)?.timezone ?? 'UTC'
  const r = toInput(form, tz)
  if ('error' in r) {
    error.value = r.error
    return
  }
  busy.value = true
  error.value = ''
  try {
    const e = await orgApi.createEvent(r.input, key)
    toast.show('Черновик создан', 'ok')
    await router.replace({ name: 'org-event', params: { id: e.id }, query: { tab: e.admission === 'ticketed' ? 'prices' : 'cover' } })
  } catch (e) {
    error.value = explain(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="new">
    <RouterLink :to="{ name: 'org-events' }" class="back">← События</RouterLink>
    <header>
      <p class="eyebrow">Черновик</p>
      <h1 class="title">Новое событие</h1>
      <p class="lead">Сначала основное. Цены, обложку и публикацию настроите на следующем шаге — до публикации покупатели ничего не видят.</p>
    </header>

    <div v-if="venues && venues.length === 0" class="empty">
      <p>Событию нужна площадка — зал, клуб или открытая сцена с адресом и часовым поясом.</p>
      <RouterLink :to="{ name: 'org-venues', query: { new: '1' } }" class="btn btn--accent">Добавить площадку</RouterLink>
    </div>

    <form v-else-if="venues" class="form" @submit.prevent="submit">
      <EventFormFields :form="form" :venues="venues" :org-slug="profile?.slug" auto-slug />
      <div class="actions">
        <p v-if="error" class="error-text" role="alert">{{ error }}</p>
        <button class="btn btn--accent" :disabled="busy">Создать черновик →</button>
      </div>
    </form>
  </div>
</template>

<style scoped>
.new {
  display: grid;
  gap: var(--space-5);
  max-width: 760px;
}
.back {
  font-size: var(--text-sm);
  color: var(--ink-2);
  text-decoration: none;
}
.title {
  font-size: var(--text-3xl);
  margin: var(--space-1) 0 var(--space-3);
}
.lead {
  color: var(--ink-2);
  max-width: 60ch;
}
.empty {
  display: grid;
  gap: var(--space-4);
  justify-items: start;
  padding: var(--space-5);
  border-radius: var(--radius-lg);
  background: var(--paper-2);
}
.form {
  display: grid;
  gap: var(--space-5);
}
.actions {
  position: sticky;
  bottom: 0;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: var(--space-4);
  padding: var(--space-4) 0;
  background: linear-gradient(transparent, var(--paper) 30%);
}
.actions .error-text {
  margin-right: auto;
}
</style>
