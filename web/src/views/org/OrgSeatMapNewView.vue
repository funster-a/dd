<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import SeatMapView from '@/components/SeatMap.vue'
import { newKey, orgApi } from '@/api/client'
import type { OrgVenue } from '@/api/types'
import { useToast } from '@/composables/toast'
import { explain } from '@/lib/eventForm'
import { LIMITS, newSection, sectionSeats, toLayout, validate, type SectionDraft } from '@/lib/layoutBuilder'
import { buildSeatMap, emptyCart } from '@/lib/seatmap'

const props = defineProps<{ id: string }>()
const router = useRouter()
const toast = useToast()

const venue = ref<OrgVenue | null>(null)
const name = ref('Основная')
const drafts = reactive<SectionDraft[]>([newSection(1)])
const busy = ref(false)
const error = ref('')
const key = newKey()

onMounted(async () => {
  venue.value = await orgApi.venue(props.id).catch(() => null)
})

// Шаблоны — отправная точка, дальше всё правится.
const TEMPLATES: { name: string; hint: string; sections: SectionDraft[] }[] = [
  {
    name: 'Театр',
    hint: 'партер веером и балкон',
    sections: [
      { ...newSection(1), name: 'Партер', rows: 12, seatsPerRow: 16, growth: 1 },
      { ...newSection(2), name: 'Балкон', rows: 5, seatsPerRow: 26, growth: 0 },
    ],
  },
  {
    name: 'Клуб',
    hint: 'столики и танцпол',
    sections: [
      { ...newSection(1), name: 'Столики', rows: 3, seatsPerRow: 10, rowLabels: 'letters' },
      { ...newSection(2), name: 'Танцпол', kind: 'general', capacity: 300 },
    ],
  },
  {
    name: 'Концертный зал',
    hint: 'фан-зона, партер, амфитеатр',
    sections: [
      { ...newSection(1), name: 'Фан-зона', kind: 'general', capacity: 500 },
      { ...newSection(2), name: 'Партер', rows: 15, seatsPerRow: 24 },
      { ...newSection(3), name: 'Амфитеатр', rows: 8, seatsPerRow: 30, growth: 1 },
    ],
  },
]

function applyTemplate(t: (typeof TEMPLATES)[number]) {
  drafts.splice(0, drafts.length, ...t.sections.map((s) => ({ ...s })))
  name.value = t.name
}

function add() {
  if (drafts.length < LIMITS.sections) drafts.push(newSection(drafts.length + 1))
}
function move(i: number, d: -1 | 1) {
  const j = i + d
  if (j < 0 || j >= drafts.length) return
  const [x] = drafts.splice(i, 1)
  drafts.splice(j, 0, x!)
}

const invalid = computed(() => validate(drafts))
const total = computed(() => drafts.reduce((n, d) => n + sectionSeats(d), 0))
// Превью строится той же функцией, что и схема для покупателя.
const preview = computed(() => (invalid.value ? null : buildSeatMap(toLayout(drafts), [])))
const noTaken = new Set<string>()
const cart = emptyCart()

async function save() {
  error.value = invalid.value ?? ''
  if (!name.value.trim()) error.value = 'Назовите схему, например «Основная» или «Без партера»'
  if (error.value) return
  busy.value = true
  try {
    await orgApi.createSeatMap(props.id, name.value.trim(), toLayout(drafts), key)
    toast.show('Схема сохранена', 'ok')
    await router.push({ name: 'org-venue', params: { id: props.id } })
  } catch (e) {
    error.value = explain(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="builder">
    <RouterLink :to="{ name: 'org-venue', params: { id } }" class="back">← {{ venue?.name ?? 'Площадка' }}</RouterLink>
    <header class="head">
      <div>
        <p class="eyebrow">Новая схема зала</p>
        <input v-model="name" class="title-input" maxlength="100" aria-label="Название схемы" />
      </div>
      <div class="total">
        <span class="total__n mono">{{ total.toLocaleString('ru-RU') }}</span>
        <span class="total__l">мест всего</span>
      </div>
    </header>

    <div class="templates">
      <span class="eyebrow">Начать с шаблона</span>
      <button v-for="t in TEMPLATES" :key="t.name" type="button" class="tpl" @click="applyTemplate(t)">
        <b>{{ t.name }}</b><span>{{ t.hint }}</span>
      </button>
    </div>

    <div class="work">
      <ol class="sections">
        <li v-for="(d, i) in drafts" :key="i" class="sec">
          <div class="sec__top">
            <span class="sec__n mono">{{ String(i + 1).padStart(2, '0') }}</span>
            <input v-model="d.name" class="input sec__name" maxlength="100" aria-label="Название сектора" />
            <div class="sec__tools">
              <button type="button" class="tool" aria-label="Выше" :disabled="i === 0" @click="move(i, -1)">↑</button>
              <button type="button" class="tool" aria-label="Ниже" :disabled="i === drafts.length - 1" @click="move(i, 1)">↓</button>
              <button type="button" class="tool tool--del" aria-label="Удалить сектор" :disabled="drafts.length === 1" @click="drafts.splice(i, 1)">×</button>
            </div>
          </div>

          <div class="seg" role="radiogroup" :aria-label="`Тип сектора ${d.name}`">
            <label class="seg__item" :class="{ 'is-on': d.kind === 'seat' }"><input v-model="d.kind" type="radio" value="seat" class="visually-hidden" />Места с рядами</label>
            <label class="seg__item" :class="{ 'is-on': d.kind === 'general' }"><input v-model="d.kind" type="radio" value="general" class="visually-hidden" />Входная зона</label>
          </div>

          <div v-if="d.kind === 'seat'" class="grid">
            <label class="field">
              <span class="field__label">Рядов</span>
              <input v-model.number="d.rows" class="input mono" type="number" min="1" :max="LIMITS.rows" />
            </label>
            <label class="field">
              <span class="field__label">Мест в первом ряду</span>
              <input v-model.number="d.seatsPerRow" class="input mono" type="number" min="1" :max="LIMITS.seats" />
            </label>
            <label class="field field--wide">
              <span class="field__label">
                Каждый следующий ряд
                <b class="mono">{{ d.growth > 0 ? `+${d.growth}` : d.growth }} {{ d.growth === 0 ? '— прямой зал' : d.growth > 0 ? '— веер' : '— сужается' }}</b>
              </span>
              <input v-model.number="d.growth" type="range" min="-4" max="6" step="1" class="range" />
            </label>
            <div class="field field--wide">
              <span class="field__label">Ряды обозначены</span>
              <div class="seg seg--sm">
                <label class="seg__item" :class="{ 'is-on': d.rowLabels === 'numbers' }"><input v-model="d.rowLabels" type="radio" value="numbers" class="visually-hidden" />1, 2, 3</label>
                <label class="seg__item" :class="{ 'is-on': d.rowLabels === 'letters' }"><input v-model="d.rowLabels" type="radio" value="letters" class="visually-hidden" />А, Б, В</label>
              </div>
            </div>
          </div>
          <label v-else class="field">
            <span class="field__label">Вместимость, человек</span>
            <input v-model.number="d.capacity" class="input mono" type="number" min="1" :max="LIMITS.capacity" />
          </label>

          <p class="sec__sum mono">{{ sectionSeats(d).toLocaleString('ru-RU') }} {{ d.kind === 'seat' ? 'мест' : 'входов' }}</p>
        </li>
        <li>
          <button type="button" class="btn btn--ghost add" @click="add">+ Сектор</button>
        </li>
      </ol>

      <aside class="preview">
        <p class="eyebrow">Превью · так увидят покупатели</p>
        <SeatMapView v-if="preview?.sections.length" :map="preview" :taken="noTaken" :cart="cart" disabled />
        <ul v-if="preview?.zones.length" class="zones">
          <li v-for="z in preview.zones" :key="z.name"><b>{{ z.name }}</b> <span class="mono">вход · {{ z.capacity }}</span></li>
        </ul>
        <p v-if="invalid" class="warn">{{ invalid }}</p>
      </aside>
    </div>

    <div class="bar">
      <p v-if="error" class="error-text" role="alert">{{ error }}</p>
      <p v-else class="muted">Схему потом нельзя изменить у опубликованных событий — но можно сделать новую.</p>
      <button class="btn btn--accent" type="button" :disabled="busy || !!invalid" @click="save">Сохранить схему</button>
    </div>
  </div>
</template>

<style scoped>
.builder {
  display: grid;
  gap: var(--space-5);
  max-width: 1280px;
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
  gap: var(--space-4);
  flex-wrap: wrap;
}
.title-input {
  font: inherit;
  font-size: var(--text-3xl);
  font-weight: 800;
  letter-spacing: -0.04em;
  border: 0;
  border-bottom: 2px dashed var(--line-strong);
  background: transparent;
  color: var(--ink);
  padding: 0;
  width: min(100%, 520px);
}
.title-input:focus {
  outline: none;
  border-bottom-color: var(--accent);
}
.total {
  display: grid;
  justify-items: end;
}
.total__n {
  font-size: var(--text-2xl);
  font-weight: 600;
}
.total__l {
  font-size: var(--text-xs);
  color: var(--ink-3);
}
.templates {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
}
.templates .eyebrow {
  margin-right: var(--space-2);
}
.tpl {
  display: grid;
  text-align: left;
  padding: var(--space-2) var(--space-4);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--paper);
  font: inherit;
  cursor: pointer;
}
.tpl span {
  font-size: var(--text-xs);
  color: var(--ink-3);
}
.tpl:hover {
  border-color: var(--ink);
}
.work {
  display: grid;
  gap: var(--space-5);
  grid-template-columns: minmax(300px, 420px) minmax(0, 1fr);
  align-items: start;
}
@media (max-width: 960px) {
  .work {
    grid-template-columns: 1fr;
  }
}
.sections {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: var(--space-3);
}
.sec {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--line);
  border-radius: var(--radius-lg);
  background: var(--paper);
}
.sec__top {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}
.sec__n {
  color: var(--accent);
  font-weight: 600;
  font-size: var(--text-sm);
}
.sec__name {
  flex: 1;
  min-width: 0;
  font-weight: 600;
}
.sec__tools {
  display: flex;
  gap: 2px;
}
.tool {
  width: 30px;
  height: 30px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: transparent;
  cursor: pointer;
  color: var(--ink-2);
}
.tool[disabled] {
  opacity: 0.35;
  cursor: default;
}
.tool--del:not([disabled]):hover {
  color: var(--danger);
  border-color: var(--danger);
}
.seg {
  display: inline-flex;
  gap: 4px;
  padding: 4px;
  background: var(--paper-2);
  border-radius: var(--radius);
  justify-self: start;
}
.seg__item {
  padding: 6px 12px;
  border-radius: var(--radius-sm);
  font-size: var(--text-sm);
  cursor: pointer;
  color: var(--ink-2);
}
.seg__item.is-on {
  background: var(--paper);
  color: var(--ink);
  box-shadow: var(--shadow);
}
.seg__item:focus-within {
  outline: 2px solid var(--ink);
}
.seg--sm .seg__item {
  padding: 4px 10px;
  font-family: var(--font-mono);
}
.grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-3);
}
.field--wide {
  grid-column: 1 / -1;
}
.field__label b {
  color: var(--ink);
  margin-left: 6px;
  font-weight: 600;
}
.range {
  width: 100%;
  accent-color: var(--accent);
}
.sec__sum {
  justify-self: end;
  font-size: var(--text-xs);
  color: var(--ink-3);
}
.add {
  width: 100%;
  border-style: dashed;
}
.preview {
  position: sticky;
  top: var(--space-5);
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
.warn {
  color: var(--warn);
  font-size: var(--text-sm);
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
.muted {
  color: var(--ink-2);
  font-size: var(--text-sm);
}
</style>
