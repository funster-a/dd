<script setup lang="ts">
import { reactive, ref } from 'vue'
import type { OrgVenue, VenueInput } from '@/api/types'
import { COMMON_TIMEZONES } from '@/lib/zoned'

// Площадка: название, адрес и часовой пояс. Координаты — по желанию, для
// точной ссылки на карту на странице события.
const props = defineProps<{ venue?: OrgVenue; submitLabel: string; busy?: boolean; error?: string }>()
const emit = defineEmits<{ submit: [v: VenueInput]; cancel: [] }>()

const f = reactive({
  name: props.venue?.name ?? '',
  address: props.venue?.address ?? '',
  timezone: props.venue?.timezone ?? 'Asia/Almaty',
  coords: props.venue?.latitude != null ? `${props.venue.latitude}, ${props.venue.longitude}` : '',
})
const local = ref('')

const zones = [...new Set([...COMMON_TIMEZONES, ...(typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : [])])]

function submit() {
  local.value = ''
  if (!f.name.trim()) return void (local.value = 'Укажите название площадки')
  let latitude: number | null = null
  let longitude: number | null = null
  if (f.coords.trim()) {
    // «43.2567, 76.9453» — как копируется из 2ГИС и Google Maps.
    const m = /^\s*(-?\d{1,2}(?:\.\d+)?)\s*[,; ]\s*(-?\d{1,3}(?:\.\d+)?)\s*$/.exec(f.coords)
    if (!m) return void (local.value = 'Координаты — два числа через запятую, например 43.2567, 76.9453')
    latitude = Number(m[1])
    longitude = Number(m[2])
    if (Math.abs(latitude) > 90 || Math.abs(longitude) > 180) return void (local.value = 'Координаты вне допустимого диапазона')
  }
  emit('submit', { name: f.name.trim(), address: f.address.trim(), timezone: f.timezone, latitude, longitude })
}
</script>

<template>
  <form class="vf" @submit.prevent="submit">
    <label class="field">
      <span class="field__label">Название</span>
      <input v-model="f.name" class="input" maxlength="200" placeholder="Например, «Казахская филармония»" autofocus />
    </label>
    <label class="field">
      <span class="field__label">Адрес</span>
      <input v-model="f.address" class="input" maxlength="500" placeholder="ул. Калдаякова, 35, Алматы" />
    </label>
    <div class="two">
      <label class="field">
        <span class="field__label">Часовой пояс</span>
        <select v-model="f.timezone" class="input">
          <option v-for="z in zones" :key="z" :value="z">{{ z }}</option>
        </select>
      </label>
      <label class="field">
        <span class="field__label">Координаты <span class="opt">необязательно</span></span>
        <input v-model="f.coords" class="input mono" inputmode="decimal" placeholder="43.2567, 76.9453" />
      </label>
    </div>
    <p v-if="local || error" class="error-text" role="alert">{{ local || error }}</p>
    <div class="actions">
      <button class="btn btn--accent" :disabled="busy">{{ submitLabel }}</button>
      <button type="button" class="btn btn--ghost" @click="emit('cancel')">Отмена</button>
    </div>
  </form>
</template>

<style scoped>
.vf {
  display: grid;
  gap: var(--space-4);
}
.two {
  display: grid;
  gap: var(--space-4);
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
}
.opt {
  font-size: var(--text-xs);
  color: var(--ink-3);
  margin-left: 6px;
}
.actions {
  display: flex;
  gap: var(--space-2);
}
</style>
