<script setup lang="ts">
import type { GeneralZone } from '@/lib/seatmap'
import { money, plural } from '@/lib/format'

defineProps<{ zones: GeneralZone[]; available: Record<string, number>; quantities: Record<string, number>; disabled?: boolean }>()
const emit = defineEmits<{ change: [zone: string, quantity: number] }>()
</script>

<template>
  <ul class="zones">
    <li v-for="z in zones" :key="z.name" class="zone" :class="`cat-${z.category}`">
      <span class="zone__dot" aria-hidden="true"></span>
      <div class="zone__info">
        <span class="zone__name">{{ z.name }}</span>
        <span class="zone__meta mono">
          {{ money(z.priceTiyn) }} ·
          <template v-if="(available[z.name] ?? 0) > 0">
            осталось {{ available[z.name] }} {{ plural(available[z.name] ?? 0, 'место', 'места', 'мест') }}
          </template>
          <template v-else>мест нет</template>
        </span>
      </div>
      <div class="stepper" role="group" :aria-label="`Количество билетов: ${z.name}`">
        <button type="button" aria-label="Меньше" :disabled="disabled || !quantities[z.name]" @click="emit('change', z.name, (quantities[z.name] ?? 0) - 1)">−</button>
        <output class="mono" aria-live="polite">{{ quantities[z.name] ?? 0 }}</output>
        <button type="button" aria-label="Больше" :disabled="disabled || (available[z.name] ?? 0) <= (quantities[z.name] ?? 0)" @click="emit('change', z.name, (quantities[z.name] ?? 0) + 1)">+</button>
      </div>
    </li>
  </ul>
</template>

<style scoped>
.zones {
  list-style: none;
  margin: 0;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: var(--radius-lg);
}
.zone {
  display: grid;
  grid-template-columns: auto 1fr auto;
  align-items: center;
  gap: var(--space-4);
  padding: var(--space-4);
}
.zone + .zone {
  border-top: 1px solid var(--line);
}
.zone__dot {
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: var(--c);
}
.zone__info {
  display: grid;
  gap: 2px;
}
.zone__name {
  font-weight: 600;
}
.zone__meta {
  font-size: var(--text-xs);
  color: var(--ink-2);
}
.stepper {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.stepper button {
  width: 40px;
  height: 40px;
  border: 1px solid var(--line-strong);
  border-radius: 50%;
  background: transparent;
  font-size: 1.2rem;
  cursor: pointer;
}
.stepper button[disabled] {
  opacity: 0.35;
  cursor: default;
}
.stepper output {
  min-width: 2ch;
  text-align: center;
  font-size: var(--text-lg);
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
.cat-7 {
  --c: var(--cat-7);
}
.cat-8 {
  --c: var(--cat-8);
}
.cat-9 {
  --c: var(--cat-9);
}
</style>
