<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import SeatMapView from '@/components/SeatMap.vue'
import { orgApi } from '@/api/client'
import type { OrgSeatMap, PriceCategory } from '@/api/types'
import { useToast } from '@/composables/toast'
import { explain } from '@/lib/eventForm'
import { money } from '@/lib/format'
import { parseTenge, tengeInput } from '@/lib/money'
import { buildSeatMap, emptyCart } from '@/lib/seatmap'

// Ценовые категории события: название, цена и сектора схемы. Каждому
// сектору — ровно одна цена; превью схемы красится по категориям.
const props = defineProps<{ eventId: string; seatMap: OrgSeatMap; locked: boolean }>()
const emit = defineEmits<{ saved: [categories: PriceCategory[]] }>()
const toast = useToast()

interface Row {
  name: string
  price: string
  sections: string[]
}
const rows = reactive<Row[]>([])
const loaded = ref(false)
const busy = ref(false)
const error = ref('')

const sections = computed(() => props.seatMap.layout.sections.map((s) => s.name))
const seatsIn = (name: string) => {
  const s = props.seatMap.layout.sections.find((x) => x.name === name)
  return s?.kind === 'general' ? (s.capacity ?? 0) : (s?.rows ?? []).reduce((n, r) => n + r.seats.length, 0)
}
const owner = (section: string) => rows.findIndex((r) => r.sections.includes(section))
const unpriced = computed(() => sections.value.filter((s) => owner(s) < 0))

onMounted(async () => {
  const cats = await orgApi.prices(props.eventId).catch(() => [] as PriceCategory[])
  if (cats.length) rows.push(...cats.map((c) => ({ name: c.name, price: tengeInput(c.price_tiyn), sections: [...c.sections] })))
  // Первый раз — по категории на сектор: организатору остаётся вписать цены.
  else rows.push(...sections.value.map((s) => ({ name: s, price: '', sections: [s] })))
  loaded.value = true
})

function toggle(i: number, section: string) {
  if (props.locked) return
  const row = rows[i]!
  if (row.sections.includes(section)) {
    row.sections = row.sections.filter((s) => s !== section)
    return
  }
  const prev = owner(section)
  if (prev >= 0) rows[prev]!.sections = rows[prev]!.sections.filter((s) => s !== section)
  row.sections.push(section)
}

function addRow() {
  rows.push({ name: `Категория ${rows.length + 1}`, price: '', sections: [] })
}

const parsed = computed(() => rows.map((r) => ({ name: r.name.trim(), price_tiyn: parseTenge(r.price), sections: r.sections })))

// Превью: схема с текущими (ещё не сохранёнными) ценами.
const preview = computed(() =>
  buildSeatMap(
    props.seatMap.layout,
    parsed.value
      .filter((p) => p.sections.length)
      .map((p, i) => ({ id: String(i), name: p.name || '—', price_tiyn: p.price_tiyn ?? 0, currency: 'KZT', sections: p.sections })),
  ),
)
// Цвет категории — как на превью: схема красит категории по убыванию цены.
const colorOf = (name: string) => preview.value.categories.find((c) => c.name === (name.trim() || '—'))?.color ?? 0
const noTaken = new Set<string>()
const cart = emptyCart()

async function save() {
  error.value = ''
  const cats = parsed.value.filter((p) => p.sections.length)
  if (unpriced.value.length) return void (error.value = `Без цены: ${unpriced.value.join(', ')}`)
  const bad = cats.find((c) => !c.name || c.price_tiyn === null)
  if (bad) return void (error.value = bad.name ? `«${bad.name}»: цена в тенге, например 5000 или 4 999,50` : 'У каждой категории должно быть название')
  busy.value = true
  try {
    const saved = await orgApi.setPrices(props.eventId, cats.map((c) => ({ name: c.name, price_tiyn: c.price_tiyn!, sections: c.sections })))
    toast.show('Цены сохранены', 'ok')
    emit('saved', saved)
  } catch (e) {
    error.value = explain(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div v-if="loaded" class="prices">
    <div class="prices__list">
      <div v-for="(r, i) in rows" :key="i" class="cat" :class="`cat-${colorOf(r.name)}`">
        <span class="cat__dot" aria-hidden="true"></span>
        <div class="cat__fields">
          <input v-model="r.name" class="input cat__name" :disabled="locked" aria-label="Название категории" maxlength="100" />
          <label class="cat__price">
            <input v-model="r.price" class="input mono" inputmode="decimal" :disabled="locked" placeholder="0" aria-label="Цена в тенге" />
            <span class="cat__cur">₸</span>
          </label>
          <button v-if="!locked && rows.length > 1" type="button" class="icon" aria-label="Удалить категорию" @click="rows.splice(i, 1)">×</button>
        </div>
        <div class="chips" role="group" :aria-label="`Сектора категории ${r.name}`">
          <button
            v-for="s in sections"
            :key="s"
            type="button"
            class="chip-btn"
            :class="{ 'is-on': r.sections.includes(s), 'is-other': !r.sections.includes(s) && owner(s) >= 0 }"
            :aria-pressed="r.sections.includes(s)"
            :disabled="locked"
            @click="toggle(i, s)"
          >
            {{ s }} <span class="mono">{{ seatsIn(s) }}</span>
          </button>
        </div>
        <p v-if="parsed[i]?.price_tiyn === 0" class="free">Бесплатные билеты: покупатель оформит заказ без оплаты.</p>
      </div>
      <button v-if="!locked" type="button" class="btn btn--sm btn--ghost add" @click="addRow">+ Категория</button>
    </div>

    <aside class="prices__preview">
      <p class="eyebrow">Так увидят покупатели</p>
      <ul class="legend">
        <li v-for="c in preview.categories" :key="c.name" :class="`cat-${c.color}`">
          <span class="cat__dot"></span>{{ c.name }} <span class="mono">{{ money(c.priceTiyn) }}</span>
        </li>
      </ul>
      <SeatMapView v-if="preview.sections.length" :map="preview" :taken="noTaken" :cart="cart" disabled />
      <p v-if="preview.zones.length" class="zones mono">
        Входные зоны: <span v-for="z in preview.zones" :key="z.name">{{ z.name }} · {{ z.capacity }} · {{ money(z.priceTiyn) }}</span>
      </p>
    </aside>

    <div v-if="!locked" class="bar">
      <p v-if="error" class="error-text" role="alert">{{ error }}</p>
      <p v-else-if="unpriced.length" class="warn">Без цены: {{ unpriced.join(', ') }}</p>
      <button class="btn btn--accent" type="button" :disabled="busy" @click="save">Сохранить цены</button>
    </div>
  </div>
  <div v-else class="skeleton" style="height: 320px"></div>
</template>

<style scoped>
.prices {
  display: grid;
  gap: var(--space-5);
}
@media (min-width: 1100px) {
  .prices {
    grid-template-columns: minmax(0, 1fr) minmax(0, 1.1fr);
    align-items: start;
  }
  .bar {
    grid-column: 1 / -1;
  }
  .prices__preview {
    position: sticky;
    top: var(--space-5);
  }
}
.prices__list {
  display: grid;
  gap: var(--space-3);
}
.cat {
  display: grid;
  grid-template-columns: 14px 1fr;
  gap: var(--space-2) var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--paper);
}
.cat__dot {
  width: 12px;
  height: 12px;
  margin-top: 14px;
  border-radius: 50%;
  background: var(--c);
  display: inline-block;
}
.cat__fields {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}
.cat__name {
  flex: 1;
  font-weight: 600;
  min-width: 0;
}
.cat__price {
  position: relative;
  width: 150px;
  flex: none;
}
.cat__price .input {
  width: 100%;
  padding-right: 32px;
  text-align: right;
}
.cat__cur {
  position: absolute;
  right: 12px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--ink-3);
}
.icon {
  width: 32px;
  height: 32px;
  border: 0;
  background: none;
  font-size: 1.4rem;
  color: var(--ink-3);
  cursor: pointer;
  flex: none;
}
.icon:hover {
  color: var(--danger);
}
.chips {
  grid-column: 2;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.chip-btn {
  padding: 5px 10px;
  border-radius: 99px;
  border: 1px dashed var(--line-strong);
  background: transparent;
  font: inherit;
  font-size: var(--text-sm);
  cursor: pointer;
  color: var(--ink-2);
}
.chip-btn .mono {
  font-size: var(--text-xs);
  color: var(--ink-3);
  margin-left: 4px;
}
.chip-btn.is-on {
  border: 1px solid var(--c);
  background: color-mix(in srgb, var(--c) 14%, var(--paper));
  color: var(--ink);
}
.chip-btn.is-other {
  opacity: 0.45;
  text-decoration: line-through;
  text-decoration-color: var(--ink-3);
}
.chip-btn[disabled] {
  cursor: default;
}
.free {
  grid-column: 2;
  font-size: var(--text-xs);
  color: var(--ok);
}
.add {
  justify-self: start;
}
.prices__preview {
  display: grid;
  gap: var(--space-3);
  min-width: 0;
}
.legend {
  display: flex;
  flex-wrap: wrap;
  gap: 6px var(--space-4);
  margin: 0;
  padding: 0;
  list-style: none;
  font-size: var(--text-sm);
}
.legend li {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.legend .cat__dot {
  margin: 0;
}
.legend .mono {
  color: var(--ink-2);
}
.zones {
  font-size: var(--text-xs);
  color: var(--ink-2);
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
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
.warn {
  color: var(--warn);
  font-size: var(--text-sm);
}
.cat-0 { --c: var(--line-strong); }
.cat-1 { --c: var(--cat-1); }
.cat-2 { --c: var(--cat-2); }
.cat-3 { --c: var(--cat-3); }
.cat-4 { --c: var(--cat-4); }
.cat-5 { --c: var(--cat-5); }
.cat-6 { --c: var(--cat-6); }
</style>
