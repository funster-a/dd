<script setup lang="ts">
import { computed, ref } from 'vue'
import { fillLevel, type PlanSector, type StadiumPlan } from '@/lib/plan'
import { money, plural } from '@/lib/format'

// План стадиона (ADR 024): покупатель выбирает сектор, места — на
// следующем шаге. Сектор — кнопка: Tab, Enter и пробел работают как мышь.
const props = defineProps<{ plan: StadiumPlan; selected?: string | null; hint?: string }>()
const emit = defineEmits<{ select: [name: string] }>()
const hovered = ref<PlanSector | null>(null)

const track = computed(() => {
  // Беговая дорожка — скруглённый прямоугольник вокруг поля.
  const [x, y, w, h] = props.plan.field
  const pad = Math.min(w, h) * 0.22
  return { x: x - pad, y: y - pad, w: w + 2 * pad, h: h + 2 * pad, r: (h + 2 * pad) / 2 }
})

function seatsLeft(s: PlanSector): string {
  if (s.available === null) return ''
  if (s.available <= 0) return 'мест нет'
  return `${s.available} ${plural(s.available, 'место', 'места', 'мест')}`
}

function label(s: PlanSector): string {
  return [s.name, s.stand, seatsLeft(s), s.available ? money(s.priceTiyn) : ''].filter(Boolean).join(', ')
}

function pick(s: PlanSector) {
  if (s.available !== 0) emit('select', s.name)
}
</script>

<template>
  <div class="plan">
    <p class="plan__hint mono" aria-live="polite">
      <template v-if="hovered">
        {{ hovered.name }} · {{ hovered.stand }} · {{ seatsLeft(hovered) }}
        <b v-if="hovered.available">{{ money(hovered.priceTiyn) }}</b>
      </template>
      <template v-else>{{ hint ?? 'Выберите сектор — места покажем на следующем шаге' }}</template>
    </p>
    <div class="plan__scroll">
      <svg class="plan__svg" :viewBox="`0 0 ${plan.width} ${plan.height}`" role="group" aria-label="План стадиона">
        <rect class="plan__track" :x="track.x" :y="track.y" :width="track.w" :height="track.h" :rx="track.r" />
        <rect class="plan__field" :x="plan.field[0]" :y="plan.field[1]" :width="plan.field[2]" :height="plan.field[3]" rx="4" />
        <line
          class="plan__line"
          :x1="plan.field[0] + plan.field[2] / 2"
          :x2="plan.field[0] + plan.field[2] / 2"
          :y1="plan.field[1]"
          :y2="plan.field[1] + plan.field[3]"
        />
        <circle class="plan__line" :cx="plan.field[0] + plan.field[2] / 2" :cy="plan.field[1] + plan.field[3] / 2" :r="plan.field[3] * 0.15" />
        <text class="plan__field-label" :x="plan.field[0] + plan.field[2] / 2" :y="plan.field[1] + plan.field[3] + 26" text-anchor="middle">
          {{ plan.fieldLabel.toUpperCase() }}
        </text>
        <g
          v-for="s in plan.sectors"
          :key="s.name"
          class="sector"
          :class="[`cat-${s.category}`, `sector--${fillLevel(s)}`, { 'sector--selected': s.name === selected }]"
          role="button"
          :tabindex="s.available === 0 ? -1 : 0"
          :aria-label="label(s)"
          :aria-disabled="s.available === 0"
          :aria-pressed="s.name === selected"
          @click="pick(s)"
          @keydown.enter.prevent="pick(s)"
          @keydown.space.prevent="pick(s)"
          @mouseenter="hovered = s"
          @mouseleave="hovered = null"
          @focus="hovered = s"
          @blur="hovered = null"
        >
          <polygon :points="s.points" />
          <text :x="s.cx" :y="s.cy + 4" text-anchor="middle">{{ s.number }}</text>
        </g>
      </svg>
    </div>
  </div>
</template>

<style scoped>
.plan {
  border: 1px solid var(--line);
  border-radius: var(--radius-lg);
  background: var(--paper);
  overflow: hidden;
}
.plan__hint {
  font-size: var(--text-xs);
  color: var(--ink-2);
  min-height: 1.5em;
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--line);
}
.plan__hint b {
  margin-left: 8px;
  color: var(--ink);
}
.plan__scroll {
  padding: var(--space-3);
}
.plan__svg {
  display: block;
  width: 100%;
  height: auto;
  font-family: var(--font-mono);
}
.plan__track {
  fill: color-mix(in srgb, #c4553b 22%, var(--paper));
}
.plan__field {
  fill: color-mix(in srgb, #3f9b52 55%, var(--paper));
}
.plan__line {
  fill: none;
  stroke: color-mix(in srgb, white 70%, transparent);
  stroke-width: 2;
}
.plan__field-label {
  font-size: 14px;
  letter-spacing: 0.3em;
  fill: var(--ink-3);
}
.sector {
  cursor: pointer;
  outline: none;
}
.sector polygon {
  stroke: var(--paper);
  stroke-width: 2;
  fill: var(--c);
  transition:
    fill 0.12s,
    opacity 0.12s;
}
.sector text {
  font-size: 13px;
  font-weight: 600;
  fill: white;
  pointer-events: none;
}
.sector--few polygon {
  fill: color-mix(in srgb, var(--c) 45%, var(--paper));
}
.sector--few text {
  fill: var(--ink);
}
.sector--none {
  cursor: not-allowed;
}
.sector--none polygon {
  fill: var(--paper-3);
}
.sector--none text {
  fill: var(--ink-3);
}
.sector--unknown polygon {
  opacity: 0.6;
}
.sector:not(.sector--none):hover polygon,
.sector:not(.sector--none):focus-visible polygon {
  fill: color-mix(in srgb, var(--c) 80%, var(--ink));
}
.sector:focus-visible polygon,
.sector--selected polygon {
  stroke: var(--ink);
  stroke-width: 4;
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
