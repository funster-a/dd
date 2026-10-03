<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { RADIUS, type Cart, type MapSeat, type SeatMap } from '@/lib/seatmap'
import { money } from '@/lib/format'

const props = defineProps<{ map: SeatMap; taken: Set<string>; cart: Cart; disabled?: boolean }>()
const emit = defineEmits<{ toggle: [seat: MapSeat] }>()

const MIN_ZOOM = 0.5
const zoom = ref(1)
const scroller = ref<HTMLElement | null>(null)

// На узком экране схема сначала вписывается в ширину целиком, дальше
// покупатель увеличивает нужный сектор; при смене масштаба центр остаётся.
function fit() {
  const w = scroller.value ? scroller.value.clientWidth - 32 : props.map.width
  zoom.value = Math.max(MIN_ZOOM, Math.min(1, w / props.map.width))
}
onMounted(fit)
watch(() => props.map.width, fit)
watch(zoom, async (z, old) => {
  const el = scroller.value
  if (!el) return
  const cx = (el.scrollLeft + el.clientWidth / 2) / old
  const cy = (el.scrollTop + el.clientHeight / 2) / old
  await nextTick()
  el.scrollLeft = cx * z - el.clientWidth / 2
  el.scrollTop = cy * z - el.clientHeight / 2
})
const hovered = ref<MapSeat | null>(null)
const width = computed(() => Math.round(props.map.width * zoom.value))

function state(s: MapSeat): 'selected' | 'taken' | 'free' {
  if (props.cart.seats.has(s.key)) return 'selected'
  return props.taken.has(s.key) ? 'taken' : 'free'
}

function label(s: MapSeat): string {
  const st = state(s)
  const tail = st === 'taken' ? 'занято' : money(s.priceTiyn) + (st === 'selected' ? ', выбрано' : '')
  return `${s.ref.section}, ряд ${s.ref.row}, место ${s.ref.seat}, ${tail}`
}

function activate(s: MapSeat) {
  if (!props.disabled && state(s) !== 'taken') emit('toggle', s)
}
</script>

<template>
  <div class="seatmap">
    <div class="seatmap__bar">
      <p class="seatmap__hint mono" aria-live="polite">
        <template v-if="hovered">
          {{ hovered.ref.section }} · ряд {{ hovered.ref.row }} · место {{ hovered.ref.seat }}
          <b>{{ state(hovered) === 'taken' ? 'занято' : money(hovered.priceTiyn) }}</b>
        </template>
        <template v-else>Нажмите на место, чтобы выбрать</template>
      </p>
      <div class="seatmap__zoom">
        <button type="button" aria-label="Уменьшить" :disabled="zoom <= MIN_ZOOM" @click="zoom = Math.max(MIN_ZOOM, zoom - 0.25)">−</button>
        <button type="button" aria-label="Увеличить" :disabled="zoom >= 2" @click="zoom = Math.min(2, zoom + 0.25)">+</button>
      </div>
    </div>

    <div ref="scroller" class="seatmap__scroll">
      <svg
        :width="width"
        :viewBox="`0 0 ${map.width} ${map.height}`"
        class="seatmap__svg"
        role="group"
        aria-label="Схема зала"
      >
        <g class="stage">
          <path :d="`M ${map.width * 0.18} ${map.stageY + 18} Q ${map.width / 2} ${map.stageY - 6} ${map.width * 0.82} ${map.stageY + 18}`" />
          <text :x="map.width / 2" :y="map.stageY + 34" text-anchor="middle">{{ map.stageLabel }}</text>
        </g>

        <g v-for="sec in map.sections" :key="sec.name">
          <text class="sec-label" :x="map.width / 2" :y="sec.y + 12" text-anchor="middle">{{ sec.name }}</text>
          <template v-for="r in sec.rowLabels" :key="r.label">
            <text class="row-label" x="10" :y="r.y + 3.5">{{ r.label }}</text>
            <text class="row-label" :x="map.width - 10" :y="r.y + 3.5" text-anchor="end">{{ r.label }}</text>
          </template>
          <g
            v-for="s in sec.seats"
            :key="s.key"
            class="seat"
            :class="[`seat--${state(s)}`, `cat-${s.category}`]"
            role="button"
            :tabindex="state(s) === 'taken' ? -1 : 0"
            :aria-label="label(s)"
            :aria-pressed="state(s) === 'selected'"
            :aria-disabled="state(s) === 'taken'"
            @click="activate(s)"
            @keydown.enter.prevent="activate(s)"
            @keydown.space.prevent="activate(s)"
            @mouseenter="hovered = s"
            @mouseleave="hovered = null"
            @focus="hovered = s"
            @blur="hovered = null"
          >
            <circle :cx="s.x" :cy="s.y" :r="RADIUS" />
            <path v-if="state(s) === 'taken'" :d="`M${s.x - 3} ${s.y - 3}L${s.x + 3} ${s.y + 3}M${s.x + 3} ${s.y - 3}L${s.x - 3} ${s.y + 3}`" />
          </g>
        </g>
      </svg>
    </div>
  </div>
</template>

<style scoped>
.seatmap {
  border: 1px solid var(--line);
  border-radius: var(--radius-lg);
  background: var(--paper);
  overflow: hidden;
}
.seatmap__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--line);
}
.seatmap__hint {
  font-size: var(--text-xs);
  color: var(--ink-2);
  min-height: 1.5em;
}
.seatmap__hint b {
  margin-left: 8px;
  color: var(--ink);
}
.seatmap__zoom {
  display: flex;
  gap: 4px;
}
.seatmap__zoom button {
  width: 32px;
  height: 32px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: transparent;
  font-size: 1.1rem;
  cursor: pointer;
}
.seatmap__zoom button[disabled] {
  opacity: 0.4;
  cursor: default;
}
.seatmap__scroll {
  overflow: auto;
  padding: var(--space-4);
  display: grid;
  justify-items: safe center;
  -webkit-overflow-scrolling: touch;
}
.seatmap__svg {
  max-width: none;
  height: auto;
  font-family: var(--font-mono);
}
.stage path {
  fill: none;
  stroke: var(--ink-3);
  stroke-width: 2;
  stroke-linecap: round;
}
.stage text {
  font-size: 10px;
  letter-spacing: 0.3em;
  fill: var(--ink-3);
}
.sec-label {
  font-family: var(--font-sans);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.02em;
  fill: var(--ink-2);
}
.row-label {
  font-size: 8px;
  fill: var(--ink-3);
}
.seat {
  cursor: pointer;
  outline: none;
}
.seat circle {
  transition:
    fill 0.12s,
    stroke 0.12s,
    transform 0.12s var(--ease);
  transform-box: fill-box;
  transform-origin: center;
  stroke-width: 1.5;
}
.seat--free circle {
  fill: color-mix(in srgb, var(--c) 16%, var(--paper));
  stroke: var(--c);
}
.seat--free:hover circle,
.seat--free:focus-visible circle {
  fill: var(--c);
  transform: scale(1.18);
}
.seat--selected circle {
  fill: var(--ink);
  stroke: var(--accent);
  stroke-width: 2.5;
  transform: scale(1.1);
}
.seat--selected:focus-visible circle {
  stroke-width: 3.5;
}
.seat--taken {
  cursor: not-allowed;
}
.seat--taken circle {
  fill: var(--paper-3);
  stroke: none;
}
.seat--taken path {
  stroke: var(--ink-3);
  stroke-width: 1.2;
  stroke-linecap: round;
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
</style>
