<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { orgApi } from '@/api/client'
import type { OrgSeatMap, OrgVenue } from '@/api/types'
import { AGE_RATINGS, type EventForm } from '@/lib/eventForm'
import { slugify } from '@/lib/slug'

// Поля события: основное, место и время, формат входа, окно продаж.
// Форма — reactive-объект родителя, поля меняются на месте.
const props = defineProps<{ form: EventForm; venues: OrgVenue[]; orgSlug?: string; autoSlug?: boolean }>()

const seatMaps = ref<OrgSeatMap[]>([])
const slugTouched = ref(!props.autoSlug)
const venue = computed(() => props.venues.find((v) => v.id === props.form.venue_id))

watch(
  () => props.form.venue_id,
  async (id) => {
    seatMaps.value = id ? await orgApi.seatMaps(id).catch(() => []) : []
    if (props.form.seat_map_id && !seatMaps.value.some((m) => m.id === props.form.seat_map_id)) props.form.seat_map_id = ''
    if (!props.form.seat_map_id && seatMaps.value.length === 1) props.form.seat_map_id = seatMaps.value[0]!.id
  },
  { immediate: true },
)

watch(
  () => props.form.title,
  (t) => {
    if (!slugTouched.value) props.form.slug = slugify(t)
  },
)

function onSlug(e: Event) {
  slugTouched.value = true
  props.form.slug = (e.target as HTMLInputElement).value.toLowerCase().replace(/[^a-z0-9-]/g, '')
}
</script>

<template>
  <div class="fields">
    <fieldset class="block">
      <legend class="block__title">Основное</legend>
      <label class="field">
        <span class="field__label">Название</span>
        <input v-model="form.title" class="input input--big" maxlength="200" placeholder="Например, «Оркестр ветра»" required />
      </label>
      <label class="field">
        <span class="field__label">Адрес страницы</span>
        <span class="slug">
          <span class="slug__prefix mono">/e/{{ orgSlug ?? '…' }}/</span>
          <input :value="form.slug" class="input slug__input mono" maxlength="63" placeholder="orchestra" @input="onSlug" />
        </span>
      </label>
      <label class="field">
        <span class="field__label">Описание <span class="opt">абзацы через пустую строку</span></span>
        <textarea v-model="form.description" class="input textarea" rows="6" maxlength="10000" placeholder="Что будет, кто выступает, сколько длится"></textarea>
      </label>
      <div class="field">
        <span class="field__label">Возраст</span>
        <div class="seg" role="radiogroup" aria-label="Возрастное ограничение">
          <label v-for="a in AGE_RATINGS" :key="a" class="seg__item" :class="{ 'is-on': form.age_rating === a }">
            <input v-model="form.age_rating" type="radio" :value="a" class="visually-hidden" />{{ a }}
          </label>
        </div>
      </div>
    </fieldset>

    <fieldset class="block">
      <legend class="block__title">Где и когда</legend>
      <label class="field">
        <span class="field__label">Площадка</span>
        <select v-model="form.venue_id" class="input">
          <option value="" disabled>Выберите площадку</option>
          <option v-for="v in venues" :key="v.id" :value="v.id">{{ v.name }}{{ v.address ? ` — ${v.address}` : '' }}</option>
        </select>
      </label>
      <div class="two">
        <label class="field">
          <span class="field__label">Начало</span>
          <input v-model="form.starts" class="input" type="datetime-local" required />
        </label>
        <label class="field">
          <span class="field__label">Окончание</span>
          <input v-model="form.ends" class="input" type="datetime-local" :min="form.starts" required />
        </label>
      </div>
      <p v-if="venue" class="note mono">Время площадки · {{ venue.timezone }}</p>
    </fieldset>

    <fieldset class="block">
      <legend class="block__title">Вход</legend>
      <div class="cards" role="radiogroup" aria-label="Формат входа">
        <label class="card" :class="{ 'is-on': form.admission === 'ticketed' }">
          <input v-model="form.admission" type="radio" value="ticketed" class="visually-hidden" />
          <b>По билетам</b>
          <span>Места на схеме или входная зона, цены по секторам. Можно и бесплатные билеты.</span>
        </label>
        <label class="card" :class="{ 'is-on': form.admission === 'free_entry' }">
          <input v-model="form.admission" type="radio" value="free_entry" class="visually-hidden" />
          <b>Свободный вход</b>
          <span>Билеты не нужны: событие просто появится в афише.</span>
        </label>
      </div>
      <template v-if="form.admission === 'ticketed'">
        <label class="field">
          <span class="field__label">Схема зала</span>
          <select v-model="form.seat_map_id" class="input" :disabled="!form.venue_id">
            <option value="" disabled>{{ form.venue_id ? (seatMaps.length ? 'Выберите схему' : 'У площадки пока нет схем') : 'Сначала площадка' }}</option>
            <option v-for="m in seatMaps" :key="m.id" :value="m.id">{{ m.name }} · {{ m.seat_count }} мест</option>
          </select>
        </label>
        <RouterLink v-if="form.venue_id" :to="{ name: 'org-seatmap-new', params: { id: form.venue_id } }" class="add-link">+ Новая схема зала</RouterLink>
        <div class="two">
          <label class="field">
            <span class="field__label">Билетов на покупателя</span>
            <input v-model.number="form.max_tickets_per_buyer" class="input" type="number" min="1" max="100" />
          </label>
          <label class="field">
            <span class="field__label">Возврат — за сколько часов до начала</span>
            <input v-model.number="form.refund_deadline_hours" class="input" type="number" min="0" max="8760" />
          </label>
        </div>
      </template>
    </fieldset>

    <fieldset v-if="form.admission === 'ticketed'" class="block">
      <legend class="block__title">Продажи <span class="opt">необязательно</span></legend>
      <div class="two">
        <label class="field">
          <span class="field__label">Открыть продажи</span>
          <input v-model="form.salesStart" class="input" type="datetime-local" />
        </label>
        <label class="field">
          <span class="field__label">Закрыть продажи</span>
          <input v-model="form.salesEnd" class="input" type="datetime-local" :max="form.ends" />
        </label>
      </div>
      <p class="note">Без дат продажи идут с публикации и до начала события.</p>
    </fieldset>
  </div>
</template>

<style scoped>
.fields {
  display: grid;
  gap: var(--space-4);
}
.block {
  margin: 0;
  padding: var(--space-5);
  border: 1px solid var(--line);
  border-radius: var(--radius-lg);
  background: var(--paper);
  display: grid;
  gap: var(--space-4);
  min-width: 0;
}
.block__title {
  float: left; /* legend внутри grid-контейнера */
  width: 100%;
  font-weight: 700;
  font-size: var(--text-lg);
  letter-spacing: -0.02em;
  padding: 0;
}
.opt {
  font-weight: 400;
  font-size: var(--text-xs);
  color: var(--ink-3);
  margin-left: 6px;
}
.input--big {
  font-size: var(--text-lg);
  font-weight: 600;
}
.textarea {
  resize: vertical;
  line-height: 1.5;
  font-family: inherit;
}
.two {
  display: grid;
  gap: var(--space-4);
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
}
.slug {
  display: flex;
  align-items: stretch;
  border: 1px solid var(--line-strong);
  border-radius: var(--radius);
  overflow: hidden;
  background: var(--paper);
}
.slug:focus-within {
  outline: 2px solid var(--ink);
  outline-offset: 1px;
}
.slug__prefix {
  display: flex;
  align-items: center;
  padding: 0 var(--space-3);
  background: var(--paper-2);
  color: var(--ink-3);
  font-size: var(--text-sm);
  white-space: nowrap;
  max-width: 45%;
  overflow: hidden;
  text-overflow: ellipsis;
}
.slug__input {
  border: 0;
  border-radius: 0;
  outline: none;
  min-width: 0;
  flex: 1;
}
.seg {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px;
  padding: 4px;
  background: var(--paper-2);
  border-radius: var(--radius);
  justify-self: start;
}
.seg__item {
  padding: 6px 12px;
  border-radius: var(--radius-sm);
  font-family: var(--font-mono);
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
.cards {
  display: grid;
  gap: var(--space-3);
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
}
.card {
  display: grid;
  gap: 4px;
  padding: var(--space-4);
  border: 1.5px solid var(--line);
  border-radius: var(--radius);
  cursor: pointer;
}
.card span {
  font-size: var(--text-sm);
  color: var(--ink-2);
}
.card.is-on {
  border-color: var(--ink);
  box-shadow: inset 0 0 0 1px var(--ink);
}
.card:focus-within {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}
.note {
  font-size: var(--text-xs);
  color: var(--ink-3);
}
.add-link {
  font-size: var(--text-sm);
  color: var(--ink);
  justify-self: start;
}
</style>
