<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import TicketStub from '@/components/TicketStub.vue'
import { ApiError, api, newKey } from '@/api/client'
import type { Order, OrderSummary, Ticket } from '@/api/types'
import { useAuth } from '@/composables/auth'
import { useToast } from '@/composables/toast'
import { dayMonth, money, orderStatusLabel, tickets as ticketsWord, time } from '@/lib/format'

const props = defineProps<{ id: string }>()
const route = useRoute()
const { requireLogin } = useAuth()
const toast = useToast()

const order = ref<Order | null>(null)
const summary = ref<OrderSummary | null>(null)
const tickets = ref<Ticket[]>([])
const failed = ref<'' | 'not_found' | 'login' | 'error'>('')
const busy = ref(false)
const now = ref(Date.now())
const fromPayment = route.query.paid === '1'
let timer: ReturnType<typeof setInterval> | undefined
let polls = 0

const tz = computed(() => summary.value?.timezone ?? 'UTC')
const left = computed(() => (order.value ? Math.max(0, Date.parse(order.value.expires_at) - now.value) : 0))
const countdown = computed(() => {
  const s = Math.ceil(left.value / 1000)
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
})
const hasTickets = computed(() => ['paid', 'partially_refunded', 'refunded'].includes(order.value?.status ?? ''))
// Билеты выпускает воркер после оплаты: пока их меньше, чем позиций, ждём.
const issuing = computed(() => order.value?.status === 'paid' && tickets.value.length < order.value.items.length)
const waitingPayment = computed(() => fromPayment && order.value?.status === 'pending' && left.value > 0)

async function load() {
  try {
    await requireLogin()
  } catch {
    failed.value = 'login'
    return
  }
  try {
    const [o, list] = await Promise.all([api.order(props.id), api.myOrders().catch(() => [] as OrderSummary[])])
    order.value = o
    summary.value = list.find((x) => x.id === o.id) ?? summary.value
    if (hasTickets.value) tickets.value = await api.orderTickets(o.id)
    failed.value = ''
  } catch (e) {
    failed.value = e instanceof ApiError && (e.status === 404 || e.status === 403) ? 'not_found' : 'error'
  }
}

onMounted(async () => {
  await load()
  timer = setInterval(() => {
    now.value = Date.now()
    // Опрос раз в 2 секунды, пока ждём вебхук или выпуск билетов.
    if ((waitingPayment.value || issuing.value) && ++polls % 2 === 0 && polls < 120) load()
  }, 1000)
})
onBeforeUnmount(() => clearInterval(timer))

async function pay() {
  if (!order.value) return
  busy.value = true
  try {
    const p = await api.startPayment(order.value.id, newKey())
    window.location.href = p.payment_url
  } catch (e) {
    toast.show(e instanceof ApiError && e.code === 'order_expired' ? 'Время на оплату вышло' : 'Не удалось перейти к оплате', 'error')
    await load()
    busy.value = false
  }
}

async function cancel() {
  if (!order.value || !confirm('Отменить заказ? Места станут свободны.')) return
  busy.value = true
  try {
    order.value = await api.cancelOrder(order.value.id, newKey())
    toast.show('Заказ отменён')
  } catch {
    toast.show('Не удалось отменить заказ', 'error')
  } finally {
    busy.value = false
  }
}

// При возврате по желанию покупателя сервисный сбор не возвращается
// (ADR 019) — говорим об этом до подтверждения, а не после.
const refundQuestion = computed(() =>
  order.value && order.value.fee_tiyn > 0
    ? 'Вернуть билет? Он сразу перестанет действовать, цена билета вернётся на карту. Сервисный сбор не возвращается.'
    : 'Вернуть билет? Он сразу перестанет действовать, деньги вернутся на карту.',
)

const refundErrors: Record<string, string> = {
  refund_deadline_passed: 'Срок возврата уже прошёл',
  ticket_used: 'По этому билету уже прошли',
  ticket_already_refunded: 'Билет уже возвращён',
  event_cancelled: 'Событие отменено — деньги вернутся автоматически',
}

async function refund(t: Ticket) {
  if (!order.value || !confirm(refundQuestion.value)) return
  busy.value = true
  try {
    const r = await api.refund(order.value.id, [t.id], newKey())
    toast.show(r.amount_tiyn > 0 ? `Возврат ${money(r.amount_tiyn)} оформлен` : 'Билет возвращён', 'ok')
    await load()
  } catch (e) {
    toast.show(refundErrors[e instanceof ApiError ? e.code : ''] ?? 'Не удалось вернуть билет', 'error')
  } finally {
    busy.value = false
  }
}

function itemText(i: { kind: string; section: string; row: string | null; seat: string }) {
  return i.kind === 'seat' ? `${i.section} · ряд ${i.row} · место ${i.seat}` : i.section
}
</script>

<template>
  <div class="page order">
    <RouterLink to="/me" class="back">← Мои билеты</RouterLink>

    <div v-if="failed" class="empty">
      <h1 class="empty__title">{{ failed === 'not_found' ? 'Заказ не найден' : failed === 'login' ? 'Нужно войти' : 'Что-то пошло не так' }}</h1>
      <p v-if="failed === 'login'">Заказ виден только покупателю — войдите по номеру телефона.</p>
      <button v-if="failed !== 'not_found'" class="btn" type="button" @click="load">Попробовать снова</button>
    </div>

    <div v-else-if="!order" aria-busy="true" class="skeleton-stack">
      <div class="skeleton" style="height: 18px; width: 30%"></div>
      <div class="skeleton" style="height: 56px; width: 70%"></div>
      <div class="skeleton" style="height: 220px"></div>
    </div>

    <template v-else>
      <header class="head">
        <p class="eyebrow">
          Заказ <span class="mono">№{{ order.id.slice(-8).toUpperCase() }}</span> ·
          <span class="status" :class="`status--${order.status}`">{{ orderStatusLabel[order.status] }}</span>
        </p>
        <h1 class="head__title">
          <RouterLink v-if="summary" :to="`/e/${summary.organizer_slug}/${summary.event_slug}`">{{ summary.event_title }}</RouterLink>
          <template v-else>{{ ticketsWord(order.items.length) }}</template>
        </h1>
        <p v-if="summary" class="head__meta mono">
          {{ dayMonth(summary.event_starts_at, tz) }} · {{ time(summary.event_starts_at, tz) }} · {{ summary.venue }}
        </p>
      </header>

      <section v-if="waitingPayment" class="panel panel--wait" aria-live="polite">
        <span class="spinner" aria-hidden="true"></span>
        <div>
          <p class="panel__title">Проверяем оплату</p>
          <p>Провайдер подтверждает платёж, обычно это несколько секунд.</p>
        </div>
      </section>

      <section v-else-if="order.status === 'pending'" class="panel panel--pay">
        <div>
          <p class="panel__title">Места держатся за вами ещё <span class="mono">{{ countdown }}</span></p>
          <p>Оплатите заказ, пока время не вышло, иначе места вернутся в продажу.</p>
        </div>
        <div class="panel__actions">
          <button class="btn btn--accent" type="button" :disabled="busy || left === 0" @click="pay">Оплатить {{ money(order.total_tiyn) }}</button>
          <button class="btn btn--ghost" type="button" :disabled="busy" @click="cancel">Отменить</button>
        </div>
      </section>

      <section v-else-if="order.status === 'expired' || order.status === 'cancelled'" class="panel">
        <p>
          {{ order.status === 'expired' ? 'Время на оплату вышло, места вернулись в продажу.' : 'Заказ отменён.' }}
          <RouterLink v-if="summary" :to="`/e/${summary.organizer_slug}/${summary.event_slug}`">Выбрать заново</RouterLink>
        </p>
      </section>

      <section v-if="hasTickets" class="tickets">
        <h2 class="section-title">Билеты</h2>
        <p v-if="issuing" class="panel panel--wait" aria-live="polite">
          <span class="spinner" aria-hidden="true"></span> Выпускаем билеты…
        </p>
        <p class="hint">Покажите QR-код на входе. Ссылку на билет можно переслать тому, кто пойдёт.</p>
        <ul class="tickets__list">
          <li v-for="t in tickets" :key="t.id">
            <TicketStub v-bind="t" />
            <div class="tickets__actions">
              <a :href="t.url" class="link" target="_blank" rel="noopener">Открыть билет ↗</a>
              <button v-if="t.status === 'issued'" class="link link--muted" type="button" :disabled="busy" @click="refund(t)">Вернуть</button>
            </div>
          </li>
        </ul>
      </section>

      <section class="receipt">
        <h2 class="section-title">Состав заказа</h2>
        <ul>
          <li v-for="(i, n) in order.items" :key="n">
            <span>{{ itemText(i) }}</span>
            <span class="mono">{{ money(i.price_tiyn) }}</span>
          </li>
        </ul>
        <div v-if="order.fee_tiyn > 0" class="receipt__fee">
          <span>Сервисный сбор</span>
          <span class="mono">{{ money(order.fee_tiyn) }}</span>
        </div>
        <div class="receipt__total">
          <span>Итого</span>
          <span class="mono">{{ money(order.total_tiyn) }}</span>
        </div>
        <p class="hint">Билеты и чек придут на {{ order.email }}</p>
      </section>
    </template>
  </div>
</template>

<style scoped>
.order {
  display: grid;
  gap: var(--space-6);
  padding-top: var(--space-6);
  padding-bottom: var(--space-8);
  max-width: 820px;
}
.back {
  font-size: var(--text-sm);
  color: var(--ink-2);
  text-decoration: none;
}
.back:hover {
  color: var(--ink);
}
.skeleton-stack,
.empty {
  display: grid;
  gap: var(--space-4);
  justify-items: start;
}
.empty__title {
  font-size: var(--text-3xl);
}
.head {
  display: grid;
  gap: var(--space-3);
}
.head__title {
  font-size: var(--text-3xl);
}
.head__title a {
  color: inherit;
  text-decoration: none;
}
.head__title a:hover {
  text-decoration: underline;
  text-decoration-thickness: 2px;
  text-underline-offset: 6px;
}
.head__meta {
  color: var(--ink-2);
  font-size: var(--text-sm);
}
.status {
  color: var(--ink);
}
.status--paid {
  color: var(--ok);
}
.status--pending {
  color: var(--accent);
}
.panel {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  padding: var(--space-5);
  border-radius: var(--radius-lg);
  background: var(--paper-2);
}
.panel--pay {
  background: var(--accent-soft);
}
.panel--wait {
  justify-content: flex-start;
}
.panel__title {
  font-weight: 700;
  font-size: var(--text-lg);
  margin-bottom: 4px;
}
.panel__actions {
  display: flex;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.spinner {
  width: 22px;
  height: 22px;
  border-radius: 50%;
  border: 2.5px solid var(--line-strong);
  border-top-color: var(--accent);
  animation: spin 0.8s linear infinite;
  flex: none;
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
.section-title {
  font-size: var(--text-xl);
  margin-bottom: var(--space-3);
}
.hint {
  color: var(--ink-2);
  font-size: var(--text-sm);
}
.tickets {
  display: grid;
  gap: var(--space-3);
}
.tickets__list {
  list-style: none;
  margin: var(--space-2) 0 0;
  padding: 0;
  display: grid;
  gap: var(--space-5);
}
.tickets__actions {
  display: flex;
  gap: var(--space-4);
  padding: var(--space-2) var(--space-2) 0;
  font-size: var(--text-sm);
}
.link {
  color: var(--ink);
  font: inherit;
  background: none;
  border: 0;
  padding: 0;
  cursor: pointer;
  text-decoration: underline;
  text-underline-offset: 3px;
}
.link--muted {
  color: var(--ink-2);
}
.receipt ul {
  list-style: none;
  margin: 0;
  padding: 0;
  border-top: 1px solid var(--ink);
}
.receipt li,
.receipt__fee,
.receipt__total {
  display: flex;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-3) 0;
  border-bottom: 1px solid var(--line);
}
.receipt__fee {
  color: var(--ink-2);
}
.receipt__total {
  font-weight: 700;
  font-size: var(--text-lg);
  border-bottom: 0;
  margin-bottom: var(--space-2);
}
</style>
