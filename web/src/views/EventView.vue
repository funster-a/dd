<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import SeatMapView from '@/components/SeatMap.vue'
import ZonePicker from '@/components/ZonePicker.vue'
import BottomSheet from '@/components/BottomSheet.vue'
import { ApiError, api, newKey } from '@/api/client'
import type { Availability, PublicEvent } from '@/api/types'
import { useAuth } from '@/composables/auth'
import { useToast } from '@/composables/toast'
import { dayMonth, fullDate, money, tickets, time, weekday } from '@/lib/format'
import { buildSeatMap, cartCount, cartTotal, emptyCart, setZoneQuantity, takenSet, toggleSeat, type MapSeat } from '@/lib/seatmap'

const props = defineProps<{ org: string; slug: string }>()
const router = useRouter()
const { requireLogin } = useAuth()
const toast = useToast()

const event = ref<PublicEvent | null>(null)
const notFound = ref(false)
const availability = ref<Availability | null>(null)
const cart = reactive(emptyCart())
const checkoutOpen = ref(false)
const email = ref(readEmail())
const busy = ref(false)
let checkoutKey = newKey()
let poll: ReturnType<typeof setInterval> | undefined

const map = computed(() => (event.value?.layout ? buildSeatMap(event.value.layout, event.value.prices) : null))
const taken = computed(() => takenSet(availability.value))
const zoneAvailable = computed(() => Object.fromEntries((availability.value?.general ?? []).map((g) => [g.section, g.available])))
const zoneQuantities = computed(() => Object.fromEntries(cart.general))
const count = computed(() => cartCount(cart))
const total = computed(() => (map.value ? cartTotal(cart, map.value.zones) : 0))
const tz = computed(() => event.value?.venue.timezone ?? 'UTC')

// Можно ли сейчас покупать: событие активно, продажи идут.
const saleState = computed<'open' | 'cancelled' | 'free_entry' | 'not_started' | 'closed'>(() => {
  const e = event.value
  if (!e) return 'closed'
  if (e.status === 'cancelled') return 'cancelled'
  if (e.admission === 'free_entry') return 'free_entry'
  const now = Date.now()
  if (e.sales_start_at && now < Date.parse(e.sales_start_at)) return 'not_started'
  if (now >= Date.parse(e.sales_end_at ?? e.starts_at)) return 'closed'
  return 'open'
})

const paragraphs = computed(() => (event.value?.description ?? '').split(/\n\s*\n/).map((p) => p.trim()).filter(Boolean))
const mapLink = computed(() => {
  const v = event.value?.venue
  if (!v) return ''
  if (v.latitude != null && v.longitude != null) return `https://2gis.kz/geo/${v.longitude},${v.latitude}`
  return `https://2gis.kz/search/${encodeURIComponent(v.address || v.name)}`
})

async function load() {
  try {
    event.value = await api.event(props.org, props.slug)
    document.title = `${event.value.title} — Партер`
    if (event.value.admission === 'ticketed' && event.value.status === 'published') await refresh()
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) notFound.value = true
    else toast.show('Не удалось загрузить событие', 'error')
  }
}

async function refresh() {
  if (!event.value) return
  try {
    availability.value = await api.availability(event.value.id)
    // Место, которое заняли, пока покупатель выбирал, уходит из корзины.
    for (const [key] of cart.seats) if (taken.value.has(key)) cart.seats.delete(key)
  } catch {
    // занятость подтянется при следующем опросе
  }
}

onMounted(() => {
  load()
  poll = setInterval(() => {
    if (document.visibilityState === 'visible' && saleState.value === 'open') refresh()
  }, 15000)
})
onBeforeUnmount(() => clearInterval(poll))
watch(() => [props.org, props.slug], load)

function onToggle(s: MapSeat) {
  if (!event.value) return
  if (!toggleSeat(cart, s, taken.value, event.value.max_tickets_per_buyer)) {
    toast.show(`Не больше ${tickets(event.value.max_tickets_per_buyer)} на покупателя`)
  }
}

function onZone(zone: string, q: number) {
  if (!event.value) return
  const got = setZoneQuantity(cart, zone, q, zoneAvailable.value[zone] ?? 0, event.value.max_tickets_per_buyer)
  if (got < q && q <= (zoneAvailable.value[zone] ?? 0)) toast.show(`Не больше ${tickets(event.value.max_tickets_per_buyer)} на покупателя`)
}

async function startCheckout() {
  try {
    await requireLogin()
  } catch {
    return
  }
  checkoutKey = newKey()
  checkoutOpen.value = true
}

function readEmail(): string {
  try {
    return localStorage.getItem('dd.email') ?? ''
  } catch {
    return ''
  }
}

const emailValid = computed(() => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.value.trim()))

const errorText: Record<string, string> = {
  seat_taken: 'Кто-то успел раньше: одно из мест уже заняли. Схема обновлена — выберите другое.',
  not_enough_seats: 'В зоне осталось меньше мест, чем выбрано.',
  ticket_limit_exceeded: 'Превышен лимит билетов на одного покупателя.',
  sales_closed: 'Продажи на это событие закрыты.',
  sales_not_started: 'Продажи ещё не открылись.',
  event_cancelled: 'Событие отменено.',
  order_in_progress: 'Заказ уже оформляется в другой вкладке.',
  session_store_unavailable: 'Вход временно недоступен. Попробуйте через несколько секунд — места пока не заняты.',
}

async function submit() {
  if (!event.value || !emailValid.value) return
  busy.value = true
  try {
    localStorage.setItem('dd.email', email.value.trim())
  } catch {
    // не страшно
  }
  try {
    const order = await api.createOrder(
      event.value.id,
      {
        seats: [...cart.seats.values()].map((s) => s.ref),
        general: [...cart.general].map(([section, quantity]) => ({ section, quantity })),
        email: email.value.trim(),
      },
      checkoutKey,
    )
    if (order.status === 'paid') {
      await router.push(`/orders/${order.id}`) // бесплатные билеты — сразу
      return
    }
    const payment = await api.startPayment(order.id, newKey())
    window.location.href = payment.payment_url
  } catch (e) {
    const code = e instanceof ApiError ? e.code : ''
    toast.show(errorText[code] ?? 'Не получилось оформить заказ. Попробуйте ещё раз.', 'error')
    if (code === 'seat_taken' || code === 'not_enough_seats') {
      checkoutOpen.value = false
      await refresh()
    }
    checkoutKey = newKey()
  } finally {
    busy.value = false
  }
}

const cartLines = computed(() => [
  ...[...cart.seats.values()].map((s) => ({ key: s.key, text: `${s.ref.section} · ряд ${s.ref.row} · место ${s.ref.seat}`, price: s.priceTiyn })),
  ...[...cart.general].map(([name, q]) => ({
    key: name,
    text: `${name} × ${q}`,
    price: (map.value?.zones.find((z) => z.name === name)?.priceTiyn ?? 0) * q,
  })),
])
</script>

<template>
  <div v-if="notFound" class="page state">
    <p class="eyebrow">Событие не найдено</p>
    <h1 class="state__title">Такого события нет</h1>
    <p class="state__text">Возможно, ссылка устарела или событие ещё не опубликовано.</p>
    <RouterLink to="/" class="btn">На афишу</RouterLink>
  </div>

  <div v-else-if="!event" class="page hero" aria-busy="true">
    <div class="hero__text">
      <div class="skeleton" style="height: 14px; width: 40%"></div>
      <div class="skeleton" style="height: 72px; width: 90%"></div>
      <div class="skeleton" style="height: 20px; width: 60%"></div>
    </div>
    <div class="skeleton hero__poster"></div>
  </div>

  <article v-else class="event" :class="{ 'event--with-cart': count > 0 }">
    <div v-if="event.status === 'cancelled'" class="banner banner--danger">
      <div class="page">Событие отменено. Деньги за купленные билеты возвращаются автоматически.</div>
    </div>

    <header class="page hero">
      <div class="hero__text">
        <p class="eyebrow">{{ event.organizer_name }} · {{ event.age_rating }}</p>
        <h1 class="hero__title">{{ event.title }}</h1>
        <dl class="facts">
          <div class="fact">
            <dt class="eyebrow">Когда</dt>
            <dd>
              <span class="fact__big">{{ dayMonth(event.starts_at, tz) }}</span>
              <span class="mono fact__small">{{ weekday(event.starts_at, tz) }} · {{ time(event.starts_at, tz) }}</span>
            </dd>
          </div>
          <div class="fact">
            <dt class="eyebrow">Где</dt>
            <dd>
              <span class="fact__big">{{ event.venue.name }}</span>
              <a v-if="event.venue.address || event.venue.latitude != null" :href="mapLink" target="_blank" rel="noopener" class="fact__small">
                {{ event.venue.address || 'Открыть на карте' }} ↗
              </a>
            </dd>
          </div>
        </dl>
        <a v-if="saleState === 'open'" href="#seats" class="btn btn--accent hero__cta">Выбрать места</a>
        <p v-else-if="saleState === 'free_entry'" class="note note--ok">Вход свободный — билеты не нужны. Просто приходите.</p>
        <p v-else-if="saleState === 'not_started'" class="note">Продажи откроются {{ fullDate(event.sales_start_at!, tz) }}</p>
        <p v-else-if="saleState === 'closed'" class="note">Продажи закрыты</p>
      </div>
      <figure class="hero__poster">
        <video
          v-if="event.cover_video_url"
          :src="event.cover_video_url"
          :poster="event.cover_image_url"
          autoplay
          muted
          loop
          playsinline
          aria-hidden="true"
        ></video>
        <img v-else :src="event.cover_image_url" alt="" />
      </figure>
    </header>

    <section v-if="paragraphs.length" class="page about">
      <h2 class="eyebrow about__label">О событии</h2>
      <div class="about__text">
        <p v-for="(p, i) in paragraphs" :key="i">{{ p }}</p>
      </div>
    </section>

    <section v-if="map && saleState === 'open'" id="seats" class="page seats">
      <div class="seats__head">
        <h2 class="seats__title">Выберите места</h2>
        <ul class="legend">
          <li v-for="c in map.categories" :key="c.name" :class="`cat-${c.color}`">
            <span class="legend__dot"></span>{{ c.name }} <span class="mono">{{ money(c.priceTiyn) }}</span>
          </li>
          <li class="legend--taken"><span class="legend__dot"></span>Занято</li>
        </ul>
      </div>

      <SeatMapView v-if="map.sections.length" :map="map" :taken="taken" :cart="cart" @toggle="onToggle" />
      <div v-if="map.zones.length" class="seats__zones">
        <h3 class="seats__subtitle">Входные зоны</h3>
        <ZonePicker :zones="map.zones" :available="zoneAvailable" :quantities="zoneQuantities" @change="onZone" />
      </div>
      <p class="seats__note">
        Не больше {{ tickets(event.max_tickets_per_buyer) }} на покупателя. Места держатся за вами 10 минут, пока вы платите.
        Вернуть билет можно не позже чем за {{ event.refund_deadline_hours }} ч до начала.
      </p>
    </section>

    <Transition name="cart">
      <div v-if="count > 0 && saleState === 'open'" class="cart">
        <div class="page cart__inner">
          <div class="cart__summary">
            <span class="cart__count">{{ tickets(count) }}</span>
            <span class="cart__total mono">{{ money(total) }}</span>
          </div>
          <button class="btn btn--accent" type="button" @click="startCheckout">Оформить</button>
        </div>
      </div>
    </Transition>

    <BottomSheet :open="checkoutOpen" title="Оформление заказа" @close="checkoutOpen = false">
      <form class="checkout" @submit.prevent="submit">
        <div class="stub">
          <p class="eyebrow">{{ dayMonth(event.starts_at, tz) }} · {{ time(event.starts_at, tz) }}</p>
          <p class="stub__title">{{ event.title }}</p>
          <ul class="stub__lines">
            <li v-for="l in cartLines" :key="l.key">
              <span>{{ l.text }}</span>
              <span class="mono">{{ money(l.price) }}</span>
            </li>
          </ul>
          <div class="stub__total">
            <span>Итого</span>
            <span class="mono">{{ money(total) }}</span>
          </div>
        </div>
        <label class="field">
          <span class="field__label">Email для билетов</span>
          <input v-model="email" class="input" type="email" autocomplete="email" inputmode="email" placeholder="you@example.com" required />
        </label>
        <button class="btn btn--accent checkout__submit" :disabled="busy || !emailValid">
          {{ total === 0 ? 'Получить билеты' : `Перейти к оплате · ${money(total)}` }}
        </button>
        <p class="checkout__note">Оплата на защищённой странице платёжного провайдера. Данные карты не проходят через Партер.</p>
      </form>
    </BottomSheet>
  </article>
</template>

<style scoped>
.state {
  padding-top: var(--space-8);
  display: grid;
  gap: var(--space-4);
  justify-items: start;
}
.state__title {
  font-size: var(--text-3xl);
}
.state__text {
  color: var(--ink-2);
  margin-bottom: var(--space-3);
}
.banner {
  padding: var(--space-3) 0;
  font-weight: 500;
}
.banner--danger {
  background: var(--danger);
  color: #fff;
}
.hero {
  display: grid;
  gap: var(--space-6);
  padding-top: clamp(24px, 5vw, 64px);
  padding-bottom: var(--space-7);
}
@media (min-width: 900px) {
  .hero {
    grid-template-columns: 1.15fr 1fr;
    align-items: end;
    gap: var(--space-7);
  }
}
.hero__text {
  display: grid;
  gap: var(--space-5);
  align-content: end;
}
.hero__title {
  font-size: var(--text-4xl);
  overflow-wrap: anywhere;
}
.hero__poster {
  margin: 0;
  aspect-ratio: 4 / 5;
  border-radius: var(--radius-lg);
  overflow: hidden;
  background: var(--paper-2);
  outline: 1px solid var(--line);
  outline-offset: -1px;
}
@media (max-width: 899px) {
  .hero__poster {
    order: -1;
    aspect-ratio: 16 / 11;
  }
}
.hero__poster img,
.hero__poster video {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.hero__cta {
  justify-self: start;
}
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: var(--space-4);
  margin: 0;
  padding: var(--space-5) 0;
  border-top: 1px solid var(--ink);
  border-bottom: 1px solid var(--line);
}
.fact dd {
  margin: var(--space-2) 0 0;
  display: grid;
  gap: 2px;
}
.fact__big {
  font-size: var(--text-xl);
  font-weight: 700;
  letter-spacing: -0.02em;
}
.fact__small {
  font-size: var(--text-sm);
  color: var(--ink-2);
}
.note {
  padding: var(--space-4);
  border-radius: var(--radius);
  background: var(--paper-2);
  font-weight: 500;
}
.note--ok {
  background: var(--ok-soft);
  color: var(--ok);
}
.about {
  display: grid;
  gap: var(--space-4);
  padding-bottom: var(--space-7);
}
@media (min-width: 900px) {
  .about {
    grid-template-columns: 1fr 3fr;
  }
}
.about__text {
  display: grid;
  gap: var(--space-4);
  font-size: var(--text-lg);
  max-width: 66ch;
}
.seats {
  display: grid;
  gap: var(--space-5);
  padding-top: var(--space-6);
  border-top: 1px solid var(--line);
  scroll-margin-top: 72px;
}
.seats__head {
  display: grid;
  gap: var(--space-4);
}
.seats__title {
  font-size: var(--text-3xl);
}
.seats__subtitle {
  font-size: var(--text-xl);
  margin-bottom: var(--space-3);
}
.seats__note {
  color: var(--ink-2);
  font-size: var(--text-sm);
  max-width: 70ch;
}
.legend {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-4);
  margin: 0;
  padding: 0;
  list-style: none;
  font-size: var(--text-sm);
}
.legend li {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.legend .mono {
  color: var(--ink-2);
}
.legend__dot {
  width: 12px;
  height: 12px;
  border-radius: 50%;
  background: color-mix(in srgb, var(--c) 16%, var(--paper));
  border: 1.5px solid var(--c);
}
.legend--taken .legend__dot {
  background: var(--paper-3);
  border-color: transparent;
}
.cat-1 {
  --c: var(--cat-1);
}
.cat-2 {
  --c: var(--cat-2);
}
.cat-3 {
  --c: var(--cat-3);
}
.cat-4 {
  --c: var(--cat-4);
}
.cat-5 {
  --c: var(--cat-5);
}
.cat-6 {
  --c: var(--cat-6);
}
.event--with-cart {
  padding-bottom: 88px;
}
.cart {
  position: fixed;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 30;
  background: var(--ink);
  color: var(--paper);
  padding: var(--space-3) 0 calc(var(--space-3) + env(safe-area-inset-bottom));
}
.cart__inner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
}
.cart__summary {
  display: grid;
}
.cart__count {
  font-size: var(--text-sm);
  opacity: 0.7;
}
.cart__total {
  font-size: var(--text-xl);
  font-weight: 600;
}
.cart-enter-active,
.cart-leave-active {
  transition: transform 0.25s var(--ease);
}
.cart-enter-from,
.cart-leave-to {
  transform: translateY(100%);
}
.checkout {
  display: grid;
  gap: var(--space-5);
}
.checkout__submit {
  width: 100%;
}
.checkout__note {
  font-size: var(--text-xs);
  color: var(--ink-2);
  text-align: center;
}
/* Корешок билета: вырезы по краям и перфорация перед итогом. */
.stub {
  position: relative;
  padding: var(--space-4);
  border-radius: var(--radius);
  background: var(--paper-2);
  display: grid;
  gap: var(--space-2);
}
.stub__title {
  font-weight: 700;
  font-size: var(--text-lg);
  letter-spacing: -0.02em;
}
.stub__lines {
  list-style: none;
  margin: var(--space-2) 0 0;
  padding: 0;
  display: grid;
  gap: 6px;
  font-size: var(--text-sm);
}
.stub__lines li,
.stub__total {
  display: flex;
  justify-content: space-between;
  gap: var(--space-3);
}
.stub__total {
  margin-top: var(--space-3);
  padding-top: var(--space-3);
  border-top: 1.5px dashed var(--line-strong);
  font-weight: 700;
}
.stub::before,
.stub::after {
  content: '';
  position: absolute;
  bottom: 44px;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: var(--paper);
}
.stub::before {
  left: -8px;
}
.stub::after {
  right: -8px;
}
</style>
