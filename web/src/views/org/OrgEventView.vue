<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import BottomSheet from '@/components/BottomSheet.vue'
import CoverUpload from '@/components/org/CoverUpload.vue'
import EventFormFields from '@/components/org/EventFormFields.vue'
import PricesEditor from '@/components/org/PricesEditor.vue'
import ReportPanel from '@/components/org/ReportPanel.vue'
import ScannersPanel from '@/components/org/ScannersPanel.vue'
import { ApiError, newKey, orgApi } from '@/api/client'
import type { OrgEvent, OrgSeatMap, OrgVenue, PriceCategory } from '@/api/types'
import { useProfile } from '@/composables/profile'
import { useToast } from '@/composables/toast'
import { emptyForm, explain, formFromEvent, toInput } from '@/lib/eventForm'
import { fullDate, tickets } from '@/lib/format'

const props = defineProps<{ id: string }>()
const route = useRoute()
const router = useRouter()
const toast = useToast()
const profile = useProfile()

const event = ref<OrgEvent | null>(null)
const venues = ref<OrgVenue[]>([])
const seatMap = ref<OrgSeatMap | null>(null)
const prices = ref<PriceCategory[]>([])
const missing = ref(false)
const form = reactive(emptyForm())
const saving = ref(false)
const formError = ref('')
const publishOpen = ref(false)
const cancelOpen = ref(false)
const cancelAck = ref(false)
const acting = ref(false)

const venue = computed(() => venues.value.find((v) => v.id === event.value?.venue_id))
const tz = computed(() => venue.value?.timezone ?? 'UTC')
const draft = computed(() => event.value?.status === 'draft')
const ticketed = computed(() => event.value?.admission === 'ticketed')
const publicUrl = computed(() => (profile.value && event.value ? `/e/${profile.value.slug}/${event.value.slug}` : ''))

type Tab = { key: string; label: string; done?: boolean }
const tabs = computed<Tab[]>(() => {
  const e = event.value
  if (!e) return []
  if (e.status === 'draft')
    return [
      { key: 'details', label: 'Основное', done: true },
      ...(ticketed.value ? [{ key: 'prices', label: 'Цены', done: pricesComplete.value }] : []),
      { key: 'cover', label: 'Обложка', done: !!e.cover_image_key },
      { key: 'publish', label: 'Публикация' },
    ]
  return [
    ...(ticketed.value ? [{ key: 'sales', label: 'Продажи' }] : []),
    ...(ticketed.value && e.status === 'published' ? [{ key: 'entry', label: 'Контроль входа' }] : []),
    { key: 'about', label: 'О событии' },
  ]
})
const tab = computed(() => {
  const q = route.query.tab
  return tabs.value.find((t) => t.key === q)?.key ?? tabs.value[0]?.key ?? 'details'
})
const go = (key: string) => router.replace({ query: { ...route.query, tab: key } })

// Каждому сектору схемы — цена: иначе публикация не пройдёт.
const pricesComplete = computed(() => {
  if (!seatMap.value) return false
  const priced = new Set(prices.value.flatMap((p) => p.sections))
  return seatMap.value.layout.sections.every((s) => priced.has(s.name))
})

const checklist = computed(() => {
  const e = event.value
  if (!e) return []
  const future = Date.parse(e.starts_at) > Date.now()
  return [
    { ok: future, text: future ? `Начало: ${fullDate(e.starts_at, tz.value)}` : 'Дата начала уже прошла', tab: 'details' },
    ...(ticketed.value
      ? [{ ok: pricesComplete.value, text: pricesComplete.value ? `Цены на все сектора · ${seatMap.value?.seat_count ?? 0} мест` : 'Назначьте цену каждому сектору', tab: 'prices' }]
      : [{ ok: true, text: 'Свободный вход — без билетов', tab: 'details' }]),
    { ok: !!e.cover_image_key, text: e.cover_image_key ? 'Обложка загружена' : 'Загрузите обложку', tab: 'cover' },
  ]
})
const ready = computed(() => checklist.value.every((c) => c.ok))

async function load() {
  try {
    const [e, vs] = await Promise.all([orgApi.event(props.id), orgApi.venues()])
    event.value = e
    venues.value = vs
    Object.assign(form, formFromEvent(e, vs.find((v) => v.id === e.venue_id)?.timezone ?? 'UTC'))
    document.title = `${e.title} — кабинет`
    await loadSeating()
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) missing.value = true
    else toast.show('Не удалось загрузить событие', 'error')
  }
}

async function loadSeating() {
  const e = event.value
  if (!e?.seat_map_id || e.admission !== 'ticketed') {
    seatMap.value = null
    prices.value = []
    return
  }
  const [m, p] = await Promise.all([orgApi.seatMap(e.seat_map_id), orgApi.prices(e.id)])
  seatMap.value = m
  prices.value = p
}

watch(() => props.id, load, { immediate: true })

async function saveDetails() {
  const r = toInput(form, venues.value.find((v) => v.id === form.venue_id)?.timezone ?? 'UTC')
  if ('error' in r) return void (formError.value = r.error)
  saving.value = true
  formError.value = ''
  try {
    const before = event.value?.seat_map_id
    event.value = await orgApi.updateEvent(props.id, r.input)
    if (before !== event.value.seat_map_id || !seatMap.value) await loadSeating()
    toast.show('Сохранено', 'ok')
    if (before !== event.value.seat_map_id && ticketed.value) go('prices')
  } catch (e) {
    formError.value = explain(e)
  } finally {
    saving.value = false
  }
}

const publishKey = ref(newKey())
async function publish() {
  acting.value = true
  try {
    const res = await orgApi.publish(props.id, publishKey.value)
    event.value = res.event
    publishOpen.value = false
    toast.show(res.seats ? `Опубликовано: ${tickets(res.seats)} в продаже` : 'Опубликовано', 'ok')
    await router.replace({ query: {} })
  } catch (e) {
    publishKey.value = newKey()
    toast.show(explain(e, 'Не удалось опубликовать'), 'error')
  } finally {
    acting.value = false
  }
}

async function cancelEvent() {
  acting.value = true
  try {
    event.value = await orgApi.cancel(props.id, newKey())
    cancelOpen.value = false
    toast.show('Событие отменено. Деньги покупателям возвращаются автоматически', 'ok')
  } catch (e) {
    toast.show(explain(e, 'Не удалось отменить'), 'error')
  } finally {
    acting.value = false
  }
}

const statusLabel = { draft: 'Черновик', published: 'В продаже', cancelled: 'Отменено' } as const
</script>

<template>
  <div v-if="missing" class="missing">
    <h1>Событие не найдено</h1>
    <RouterLink :to="{ name: 'org-events' }" class="btn">К событиям</RouterLink>
  </div>

  <div v-else-if="!event" class="skeleton" style="height: 480px; max-width: 1080px"></div>

  <div v-else class="ev">
    <RouterLink :to="{ name: 'org-events' }" class="back">← События</RouterLink>

    <header class="head">
      <div class="head__cover">
        <img v-if="event.cover_image_url" :src="event.cover_image_url" alt="" />
      </div>
      <div class="head__text">
        <p class="head__meta">
          <span class="chip" :class="`chip--${event.status}`">{{ statusLabel[event.status] }}</span>
          <span class="mono">{{ fullDate(event.starts_at, tz) }}</span>
          <span>{{ venue?.name }}</span>
        </p>
        <h1 class="title">{{ event.title }}</h1>
        <a v-if="publicUrl && !draft" :href="publicUrl" target="_blank" rel="noopener" class="public mono">{{ publicUrl }} ↗</a>
      </div>
    </header>

    <nav class="tabs" role="tablist" aria-label="Разделы события">
      <button
        v-for="(t, i) in tabs"
        :key="t.key"
        type="button"
        role="tab"
        class="tabs__tab"
        :class="{ 'is-on': tab === t.key }"
        :aria-selected="tab === t.key"
        @click="go(t.key)"
      >
        <span v-if="draft" class="tabs__n mono" :class="{ 'is-done': t.done }">{{ t.done && t.key !== 'publish' ? '✓' : String(i + 1).padStart(2, '0') }}</span>
        {{ t.label }}
      </button>
    </nav>

    <section class="body" role="tabpanel">
      <!-- Черновик: основное -->
      <form v-if="tab === 'details' && draft" class="details" @submit.prevent="saveDetails">
        <EventFormFields :form="form" :venues="venues" :org-slug="profile?.slug" />
        <div class="bar">
          <p v-if="formError" class="error-text" role="alert">{{ formError }}</p>
          <button class="btn btn--accent" :disabled="saving">Сохранить</button>
        </div>
      </form>

      <template v-else-if="tab === 'prices'">
        <p v-if="!seatMap" class="panel">Сначала выберите схему зала во вкладке «Основное».</p>
        <PricesEditor v-else :key="seatMap.id" :event-id="event.id" :seat-map="seatMap" :locked="!draft" @saved="(c) => (prices = c)" />
      </template>

      <CoverUpload v-else-if="tab === 'cover'" :event="event" :locked="!draft" @saved="(e) => (event = e)" />

      <div v-else-if="tab === 'publish'" class="publish">
        <ul class="check">
          <li v-for="c in checklist" :key="c.text" :class="{ ok: c.ok }">
            <span class="check__mark" aria-hidden="true">{{ c.ok ? '✓' : '!' }}</span>
            <span>{{ c.text }}</span>
            <button v-if="!c.ok" type="button" class="link" @click="go(c.tab)">Исправить</button>
          </li>
        </ul>
        <div class="publish__note">
          <p><b>После публикации</b> событие появится в афише и на своей странице, билеты сразу в продаже.</p>
          <p>Схема зала, цены и даты фиксируются: покупатели платят за то, что увидели. Отменить событие можно в любой момент — деньги вернутся автоматически.</p>
        </div>
        <button class="btn btn--accent publish__btn" type="button" :disabled="!ready" @click="publishOpen = true">Опубликовать</button>
      </div>

      <ReportPanel v-else-if="tab === 'sales'" :event-id="event.id" :slug="event.slug" />

      <ScannersPanel v-else-if="tab === 'entry'" :event-id="event.id" :locked="event.status !== 'published'" />

      <div v-else-if="tab === 'about'" class="about">
        <dl class="facts">
          <div><dt>Площадка</dt><dd>{{ venue?.name }}<span v-if="venue?.address" class="muted"> · {{ venue.address }}</span></dd></div>
          <div><dt>Начало</dt><dd class="mono">{{ fullDate(event.starts_at, tz) }}</dd></div>
          <div><dt>Окончание</dt><dd class="mono">{{ fullDate(event.ends_at, tz) }}</dd></div>
          <div><dt>Возраст</dt><dd class="mono">{{ event.age_rating }}</dd></div>
          <template v-if="ticketed">
            <div><dt>Схема зала</dt><dd>{{ seatMap?.name }} · {{ seatMap?.seat_count }} мест</dd></div>
            <div><dt>Билетов на покупателя</dt><dd class="mono">{{ event.max_tickets_per_buyer }}</dd></div>
            <div><dt>Возврат</dt><dd>не позже чем за {{ event.refund_deadline_hours }} ч до начала</dd></div>
          </template>
          <div v-else><dt>Вход</dt><dd>свободный</dd></div>
        </dl>
        <PricesEditor v-if="seatMap" :key="seatMap.id" :event-id="event.id" :seat-map="seatMap" locked />
        <section v-if="event.status === 'published'" class="danger">
          <div>
            <h3>Отменить событие</h3>
            <p>Продажи остановятся, все билеты аннулируются, покупателям вернутся деньги — полностью, автоматически.</p>
          </div>
          <button class="btn btn--danger" type="button" @click="cancelOpen = true">Отменить событие</button>
        </section>
      </div>
    </section>

    <BottomSheet :open="publishOpen" title="Опубликовать событие?" @close="publishOpen = false">
      <div class="confirm">
        <p>«{{ event.title }}» появится в афише, продажи начнутся{{ event.sales_start_at ? ` ${fullDate(event.sales_start_at, tz)}` : ' сразу' }}.</p>
        <p class="muted">Схему, цены и даты после этого изменить нельзя.</p>
        <button class="btn btn--accent" type="button" :disabled="acting" @click="publish">Опубликовать</button>
      </div>
    </BottomSheet>

    <BottomSheet :open="cancelOpen" title="Отменить событие?" @close="cancelOpen = false">
      <div class="confirm">
        <p>Это нельзя отменить. Все билеты перестанут действовать, каждый заказ вернётся покупателю полностью.</p>
        <label class="ack"><input v-model="cancelAck" type="checkbox" /> Понимаю, отменить «{{ event.title }}»</label>
        <button class="btn btn--danger" type="button" :disabled="acting || !cancelAck" @click="cancelEvent">Отменить событие</button>
      </div>
    </BottomSheet>
  </div>
</template>

<style scoped>
.ev {
  display: grid;
  gap: var(--space-5);
  max-width: 1080px;
}
.missing {
  display: grid;
  gap: var(--space-4);
  justify-items: start;
}
.back {
  font-size: var(--text-sm);
  color: var(--ink-2);
  text-decoration: none;
}
.head {
  display: flex;
  gap: var(--space-5);
  align-items: end;
}
.head__cover {
  flex: none;
  width: 88px;
  aspect-ratio: 4 / 5;
  border-radius: var(--radius);
  overflow: hidden;
  background: var(--paper-3);
}
.head__cover img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.head__text {
  display: grid;
  gap: var(--space-2);
  min-width: 0;
}
.head__meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2) var(--space-4);
  font-size: var(--text-sm);
  color: var(--ink-2);
}
.title {
  font-size: var(--text-3xl);
  overflow-wrap: anywhere;
}
.public {
  font-size: var(--text-sm);
  color: var(--ink-2);
}
.tabs {
  display: flex;
  gap: var(--space-1);
  border-bottom: 1px solid var(--line);
  overflow-x: auto;
  scrollbar-width: none;
}
.tabs__tab {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: var(--space-3) var(--space-4);
  border: 0;
  background: none;
  font: inherit;
  font-weight: 500;
  color: var(--ink-2);
  cursor: pointer;
  white-space: nowrap;
  box-shadow: inset 0 -2px 0 transparent;
}
.tabs__tab:hover {
  color: var(--ink);
}
.tabs__tab.is-on {
  color: var(--ink);
  box-shadow: inset 0 -2px 0 var(--accent);
}
.tabs__n {
  font-size: var(--text-xs);
  color: var(--ink-3);
}
.tabs__n.is-done {
  color: var(--ok);
}
.body {
  min-width: 0;
}
.details {
  display: grid;
  gap: var(--space-4);
  max-width: 760px;
}
.bar {
  position: sticky;
  bottom: 0;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: var(--space-4);
  padding: var(--space-4) 0;
  background: linear-gradient(transparent, var(--paper) 30%);
}
.bar p {
  margin-right: auto;
}
.panel {
  padding: var(--space-5);
  border-radius: var(--radius-lg);
  background: var(--paper-2);
}
.publish {
  display: grid;
  gap: var(--space-5);
  max-width: 640px;
}
.check {
  list-style: none;
  margin: 0;
  padding: 0;
  border-top: 1px solid var(--ink);
}
.check li {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-4) 0;
  border-bottom: 1px solid var(--line);
}
.check__mark {
  width: 26px;
  height: 26px;
  flex: none;
  display: grid;
  place-items: center;
  border-radius: 50%;
  font-weight: 700;
  font-size: var(--text-sm);
  background: var(--warn-soft);
  color: var(--warn);
}
.check li.ok .check__mark {
  background: var(--ok-soft);
  color: var(--ok);
}
.check .link {
  margin-left: auto;
}
.link {
  background: none;
  border: 0;
  padding: 0;
  font: inherit;
  font-size: var(--text-sm);
  color: var(--ink);
  text-decoration: underline;
  text-underline-offset: 3px;
  cursor: pointer;
}
.publish__note {
  display: grid;
  gap: var(--space-2);
  color: var(--ink-2);
}
.publish__note b {
  color: var(--ink);
}
.publish__btn {
  justify-self: start;
  padding-inline: var(--space-7);
}
.about {
  display: grid;
  gap: var(--space-6);
}
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  margin: 0;
  border-top: 1px solid var(--ink);
}
.facts > div {
  padding: var(--space-3) var(--space-4) var(--space-3) 0;
  border-bottom: 1px solid var(--line);
}
.facts dt {
  font-size: var(--text-xs);
  color: var(--ink-3);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
.facts dd {
  margin: 4px 0 0;
}
.muted {
  color: var(--ink-2);
}
.danger {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  padding: var(--space-5);
  border: 1px solid color-mix(in srgb, var(--danger) 40%, var(--line));
  border-radius: var(--radius-lg);
}
.danger h3 {
  font-size: var(--text-lg);
  margin-bottom: 4px;
}
.danger p {
  color: var(--ink-2);
  font-size: var(--text-sm);
  max-width: 56ch;
}
.confirm {
  display: grid;
  gap: var(--space-4);
}
.ack {
  display: flex;
  gap: var(--space-2);
  align-items: center;
  font-weight: 500;
}
@media (max-width: 600px) {
  .head {
    align-items: flex-start;
  }
  .head__cover {
    width: 64px;
  }
}
</style>
