<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { orgApi } from '@/api/client'
import type { EventReport } from '@/api/types'
import { useToast } from '@/composables/toast'
import { money, plural } from '@/lib/format'

// Продажи события: заполняемость, деньги, проходы, категории и выгрузка.
const props = defineProps<{ eventId: string; slug: string }>()
const toast = useToast()
const rep = ref<EventReport | null>(null)
const failed = ref(false)
const downloading = ref(false)
let timer: ReturnType<typeof setInterval> | undefined

async function load() {
  try {
    rep.value = await orgApi.report(props.eventId)
    failed.value = false
  } catch {
    failed.value = true
  }
}
onMounted(() => {
  load()
  // Во время продаж и на входе цифры меняются — обновляем раз в 30 секунд.
  timer = setInterval(() => document.visibilityState === 'visible' && load(), 30000)
})
onBeforeUnmount(() => clearInterval(timer))

const pct = (n: number, of: number) => (of ? Math.round((n / of) * 1000) / 10 : 0)
const s = computed(() => rep.value?.seats)
const updated = computed(() =>
  rep.value ? new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(new Date(rep.value.generated_at)) : '',
)

async function exportCsv() {
  downloading.value = true
  try {
    const blob = await orgApi.exportTickets(props.eventId)
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `tickets-${props.slug}.csv`
    a.click()
    setTimeout(() => URL.revokeObjectURL(a.href), 1000)
  } catch {
    toast.show('Не удалось выгрузить билеты', 'error')
  } finally {
    downloading.value = false
  }
}
</script>

<template>
  <div v-if="failed && !rep" class="panel">
    <p>Не удалось загрузить продажи.</p>
    <button class="btn btn--sm" type="button" @click="load">Обновить</button>
  </div>
  <div v-else-if="!rep" class="skeleton" style="height: 360px"></div>
  <div v-else class="report">
    <section class="hero">
      <div class="hero__num">
        <span class="eyebrow">Заполняемость</span>
        <span class="big">{{ (rep.seats.occupancy_permille / 10).toLocaleString('ru-RU') }}<small>%</small></span>
        <span class="muted mono">{{ s!.sold }} из {{ s!.capacity }} {{ plural(s!.capacity, 'места', 'мест', 'мест') }}</span>
      </div>
      <div class="stack" role="img" :aria-label="`Продано ${s!.sold}, в корзинах ${s!.held}, свободно ${s!.available}`">
        <span class="stack__sold" :style="{ width: pct(s!.sold, s!.capacity) + '%' }"></span>
        <span class="stack__held" :style="{ width: pct(s!.held, s!.capacity) + '%' }"></span>
      </div>
      <ul class="stack__legend">
        <li><i class="dot dot--sold"></i>Продано <b class="mono">{{ s!.sold }}</b></li>
        <li><i class="dot dot--held"></i>В корзинах сейчас <b class="mono">{{ s!.held }}</b></li>
        <li><i class="dot"></i>Свободно <b class="mono">{{ s!.available }}</b></li>
      </ul>
    </section>

    <dl class="kpis">
      <div>
        <dt class="eyebrow">Выручка</dt>
        <dd class="mono">{{ money(rep.money.gross_tiyn) }}</dd>
        <span class="muted">{{ rep.money.paid_orders }} {{ plural(rep.money.paid_orders, 'заказ', 'заказа', 'заказов') }}, средний {{ money(rep.money.average_order_tiyn) }}</span>
      </div>
      <div>
        <dt class="eyebrow">Возвраты</dt>
        <dd class="mono">{{ money(rep.money.refunded_tiyn) }}</dd>
        <span class="muted">{{ rep.tickets.refunded }} {{ plural(rep.tickets.refunded, 'билет', 'билета', 'билетов') }}</span>
      </div>
      <div class="kpis__net">
        <dt class="eyebrow">Итого после возвратов</dt>
        <dd class="mono">{{ money(rep.money.net_tiyn) }}</dd>
      </div>
      <div>
        <dt class="eyebrow">Прошли на вход</dt>
        <dd class="mono">{{ rep.tickets.used }} <small>/ {{ rep.tickets.active }}</small></dd>
        <span class="muted">{{ pct(rep.tickets.used, rep.tickets.active) }}% действующих билетов</span>
      </div>
    </dl>

    <section>
      <h3 class="h3">По категориям</h3>
      <table class="table">
        <thead>
          <tr>
            <th>Категория</th>
            <th class="num">Цена</th>
            <th class="num">Продано</th>
            <th class="bar-col" aria-hidden="true"></th>
            <th class="num">Выручка</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in rep.categories" :key="c.name">
            <td>{{ c.name }}</td>
            <td class="num mono">{{ money(c.price_tiyn) }}</td>
            <td class="num mono">{{ c.sold }} / {{ c.capacity }}</td>
            <td class="bar-col"><span class="mini" :style="{ '--p': pct(c.sold, c.capacity) + '%' }"></span></td>
            <td class="num mono">{{ money(c.revenue_tiyn) }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <footer class="foot">
      <span class="muted mono">Обновлено в {{ updated }}</span>
      <button class="btn btn--sm btn--ghost" type="button" @click="load">Обновить</button>
      <button class="btn btn--sm" type="button" :disabled="downloading" @click="exportCsv">Скачать билеты · CSV</button>
    </footer>
  </div>
</template>

<style scoped>
.report {
  display: grid;
  gap: var(--space-6);
}
.panel {
  padding: var(--space-5);
  border-radius: var(--radius-lg);
  background: var(--paper-2);
  display: flex;
  gap: var(--space-4);
  align-items: center;
}
.hero {
  display: grid;
  gap: var(--space-3);
}
.hero__num {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-4);
}
.hero__num .eyebrow {
  width: 100%;
}
.big {
  font-size: clamp(3rem, 2rem + 4vw, 5.5rem);
  font-weight: 800;
  letter-spacing: -0.05em;
  line-height: 0.9;
}
.big small {
  font-size: 0.45em;
  letter-spacing: 0;
  margin-left: 4px;
  color: var(--ink-2);
}
.muted {
  color: var(--ink-2);
  font-size: var(--text-sm);
}
.stack {
  display: flex;
  height: 14px;
  border-radius: 99px;
  overflow: hidden;
  background: var(--paper-3);
}
.stack__sold {
  background: var(--accent);
}
.stack__held {
  background: repeating-linear-gradient(-45deg, var(--accent) 0 4px, color-mix(in srgb, var(--accent) 35%, var(--paper)) 4px 8px);
}
.stack__legend {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-5);
  margin: 0;
  padding: 0;
  list-style: none;
  font-size: var(--text-sm);
  color: var(--ink-2);
}
.stack__legend b {
  color: var(--ink);
  margin-left: 4px;
}
.dot {
  display: inline-block;
  width: 10px;
  height: 10px;
  border-radius: 3px;
  margin-right: 6px;
  background: var(--paper-3);
  vertical-align: -1px;
}
.dot--sold {
  background: var(--accent);
}
.dot--held {
  background: color-mix(in srgb, var(--accent) 45%, var(--paper));
}
.kpis {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  margin: 0;
  border-top: 1px solid var(--ink);
}
.kpis > div {
  display: grid;
  gap: 4px;
  padding: var(--space-4) var(--space-4) var(--space-4) 0;
  border-bottom: 1px solid var(--line);
}
.kpis dd {
  margin: 0;
  font-size: var(--text-2xl);
  font-weight: 600;
  letter-spacing: -0.02em;
}
.kpis dd small {
  font-size: 0.55em;
  color: var(--ink-3);
}
.kpis__net dd {
  color: var(--ok);
}
.h3 {
  font-size: var(--text-lg);
  margin-bottom: var(--space-3);
}
.table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--text-sm);
}
.table th {
  text-align: left;
  font-weight: 500;
  color: var(--ink-3);
  font-size: var(--text-xs);
  text-transform: uppercase;
  letter-spacing: 0.06em;
  padding: 0 var(--space-3) var(--space-2) 0;
  border-bottom: 1px solid var(--ink);
}
.table td {
  padding: var(--space-3) var(--space-3) var(--space-3) 0;
  border-bottom: 1px solid var(--line);
}
.num {
  text-align: right !important;
  white-space: nowrap;
}
.bar-col {
  width: 22%;
}
.mini {
  display: block;
  height: 6px;
  border-radius: 99px;
  background: linear-gradient(90deg, var(--accent) var(--p), var(--paper-3) var(--p));
}
@media (max-width: 640px) {
  .bar-col {
    display: none;
  }
}
.foot {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-3);
}
.foot .muted {
  margin-right: auto;
}
</style>
