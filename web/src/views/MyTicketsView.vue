<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { OrderSummary } from '@/api/types'
import { useAuth } from '@/composables/auth'
import { dayMonth, money, orderStatusLabel, tickets, time, weekday } from '@/lib/format'

const { authed, openSheet } = useAuth()
const orders = ref<OrderSummary[] | null>(null)
const failed = ref(false)

async function load() {
  if (!authed.value) return
  failed.value = false
  try {
    orders.value = await api.myOrders()
  } catch {
    failed.value = true
  }
}
onMounted(load)
watch(authed, load)

const now = Date.now()
const upcoming = computed(() => (orders.value ?? []).filter((o) => Date.parse(o.event_starts_at) >= now - 6 * 3600_000).reverse())
const past = computed(() => (orders.value ?? []).filter((o) => Date.parse(o.event_starts_at) < now - 6 * 3600_000))
</script>

<template>
  <div class="page me">
    <header class="me__head">
      <p class="eyebrow">Личный кабинет</p>
      <h1 class="me__title">Мои билеты</h1>
    </header>

    <div v-if="!authed" class="empty">
      <p>Билеты привязаны к номеру телефона. Войдите, чтобы увидеть свои заказы.</p>
      <button class="btn btn--accent" type="button" @click="openSheet">Войти по номеру</button>
    </div>

    <div v-else-if="failed" class="empty">
      <p>Не удалось загрузить заказы.</p>
      <button class="btn" type="button" @click="load">Попробовать снова</button>
    </div>

    <ul v-else-if="!orders" class="list" aria-busy="true">
      <li v-for="n in 3" :key="n" class="skeleton" style="height: 96px"></li>
    </ul>

    <div v-else-if="orders.length === 0" class="empty">
      <p>Пока ни одного билета. Самое время что-нибудь выбрать.</p>
      <RouterLink to="/" class="btn btn--accent">Смотреть афишу</RouterLink>
    </div>

    <template v-else>
      <section v-for="group in [{ title: 'Впереди', items: upcoming }, { title: 'Прошедшие', items: past }]" :key="group.title">
        <template v-if="group.items.length">
          <h2 class="group">{{ group.title }}</h2>
          <ul class="list">
            <li v-for="o in group.items" :key="o.id">
              <RouterLink :to="`/orders/${o.id}`" class="row" :class="{ 'row--past': group.items === past }">
                <div class="row__date">
                  <span class="row__day">{{ dayMonth(o.event_starts_at, o.timezone) }}</span>
                  <span class="mono">{{ weekday(o.event_starts_at, o.timezone) }} {{ time(o.event_starts_at, o.timezone) }}</span>
                </div>
                <div class="row__main">
                  <span class="row__title">{{ o.event_title }}</span>
                  <span class="row__meta">{{ o.venue }} · {{ tickets(o.items) }} · <span class="mono">{{ money(o.total_tiyn) }}</span></span>
                </div>
                <span class="row__status" :class="`row__status--${o.status}`">{{ orderStatusLabel[o.status] }}</span>
              </RouterLink>
            </li>
          </ul>
        </template>
      </section>
    </template>
  </div>
</template>

<style scoped>
.me {
  display: grid;
  gap: var(--space-6);
  padding-top: var(--space-7);
  padding-bottom: var(--space-8);
  max-width: 920px;
}
.me__title {
  font-size: var(--text-4xl);
  margin-top: var(--space-2);
}
.empty {
  display: grid;
  gap: var(--space-4);
  justify-items: start;
  padding: var(--space-6);
  border-radius: var(--radius-lg);
  background: var(--paper-2);
  font-size: var(--text-lg);
}
.group {
  font-size: var(--text-sm);
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--ink-2);
  font-weight: 600;
  padding-bottom: var(--space-3);
  border-bottom: 1px solid var(--ink);
}
.list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
}
.row {
  display: grid;
  grid-template-columns: 140px 1fr auto;
  gap: var(--space-4);
  align-items: center;
  padding: var(--space-4) 0;
  border-bottom: 1px solid var(--line);
  color: inherit;
  text-decoration: none;
  transition: background 0.15s;
}
.row:hover {
  background: var(--paper-2);
}
.row--past {
  opacity: 0.6;
}
.row__date {
  display: grid;
  font-size: var(--text-sm);
  color: var(--ink-2);
}
.row__day {
  font-size: var(--text-lg);
  font-weight: 700;
  color: var(--ink);
}
.row__main {
  display: grid;
  gap: 2px;
  min-width: 0;
}
.row__title {
  font-weight: 700;
  font-size: var(--text-lg);
  letter-spacing: -0.02em;
}
.row__meta {
  color: var(--ink-2);
  font-size: var(--text-sm);
}
.row__status {
  font-size: var(--text-xs);
  padding: 4px 10px;
  border-radius: 99px;
  background: var(--paper-3);
  white-space: nowrap;
}
.row__status--pending {
  background: var(--accent-soft);
  color: var(--accent-ink);
}
.row__status--paid {
  background: var(--ok-soft);
  color: var(--ok);
}
@media (max-width: 640px) {
  .row {
    grid-template-columns: 1fr auto;
  }
  .row__date {
    grid-column: 1 / -1;
    display: flex;
    gap: var(--space-2);
    align-items: baseline;
  }
  .row__day {
    font-size: var(--text-md);
  }
}
</style>
