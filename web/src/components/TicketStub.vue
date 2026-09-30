<script setup lang="ts">
import { computed } from 'vue'
import { ticketToken } from '@/api/client'

// Билет в виде корешка: слева место, справа QR. Используется на странице
// билета и в заказе.
const props = defineProps<{
  status: 'issued' | 'used' | 'revoked'
  kind: 'seat' | 'general'
  section: string
  row: string | null
  seat: string
  url: string
  title?: string
  when?: string
  where?: string
  large?: boolean
}>()

const qr = computed(() => `/v1/tickets/${encodeURIComponent(ticketToken(props.url))}/qr.png`)
const statusText = { issued: 'Действителен', used: 'Использован', revoked: 'Возвращён' } as const
</script>

<template>
  <div class="stub" :class="[`stub--${status}`, { 'stub--large': large }]">
    <div class="stub__main">
      <p v-if="when" class="eyebrow">{{ when }}</p>
      <p v-if="title" class="stub__title">{{ title }}</p>
      <p v-if="where" class="stub__where">{{ where }}</p>
      <dl class="stub__place">
        <div>
          <dt class="eyebrow">{{ kind === 'seat' ? 'Сектор' : 'Зона' }}</dt>
          <dd>{{ section }}</dd>
        </div>
        <template v-if="kind === 'seat'">
          <div>
            <dt class="eyebrow">Ряд</dt>
            <dd>{{ row }}</dd>
          </div>
          <div>
            <dt class="eyebrow">Место</dt>
            <dd>{{ seat }}</dd>
          </div>
        </template>
      </dl>
      <p class="stub__status mono">{{ statusText[status] }}</p>
    </div>
    <div class="stub__qr">
      <img v-if="status === 'issued'" :src="qr" alt="QR-код билета для прохода" width="160" height="160" />
      <span v-else class="stub__void mono" aria-hidden="true">{{ status === 'used' ? 'ПРОХОД' : 'ВОЗВРАТ' }}</span>
      <slot />
    </div>
  </div>
</template>

<style scoped>
.stub {
  --notch: 18px;
  position: relative;
  display: grid;
  grid-template-columns: 1fr auto;
  background: var(--paper-2);
  border-radius: var(--radius-lg);
  /* Вырезы на линии отрыва корешка. */
  mask:
    radial-gradient(circle var(--notch) at calc(100% - var(--qr-w, 184px)) 0, #0000 98%, #000) top / 100% 51% no-repeat,
    radial-gradient(circle var(--notch) at calc(100% - var(--qr-w, 184px)) 100%, #0000 98%, #000) bottom / 100% 51% no-repeat;
}
.stub__main {
  padding: var(--space-5);
  display: grid;
  gap: var(--space-2);
  align-content: start;
  min-width: 0;
}
.stub__title {
  font-size: var(--text-xl);
  font-weight: 700;
  letter-spacing: -0.02em;
  line-height: 1.15;
}
.stub__where {
  color: var(--ink-2);
  font-size: var(--text-sm);
}
.stub__place {
  display: flex;
  gap: var(--space-5);
  margin: var(--space-3) 0 0;
}
.stub__place dd {
  margin: 2px 0 0;
  font-size: var(--text-2xl);
  font-weight: 700;
  letter-spacing: -0.03em;
}
.stub__status {
  margin-top: auto;
  padding-top: var(--space-3);
  font-size: var(--text-xs);
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--ok);
}
.stub__qr {
  width: var(--qr-w, 184px);
  padding: var(--space-4);
  border-left: 2px dashed var(--line-strong);
  display: grid;
  place-items: center;
  gap: var(--space-3);
}
.stub__qr img {
  width: 100%;
  height: auto;
  aspect-ratio: 1;
  background: #fff;
  padding: 8px;
  border-radius: var(--radius-sm);
  image-rendering: pixelated;
}
.stub__void {
  writing-mode: vertical-rl;
  transform: rotate(180deg);
  font-size: var(--text-lg);
  letter-spacing: 0.3em;
  color: var(--ink-3);
}
.stub--used .stub__status,
.stub--revoked .stub__status {
  color: var(--ink-3);
}
.stub--used .stub__main,
.stub--revoked .stub__main {
  opacity: 0.6;
}
.stub--large {
  --qr-w: 100%;
  grid-template-columns: 1fr;
  mask:
    radial-gradient(circle var(--notch) at 0 var(--cut, 62%), #0000 98%, #000) left / 51% 100% no-repeat,
    radial-gradient(circle var(--notch) at 100% var(--cut, 62%), #0000 98%, #000) right / 51% 100% no-repeat;
}
.stub--large .stub__qr {
  border-left: 0;
  border-top: 2px dashed var(--line-strong);
  padding: var(--space-5);
}
.stub--large .stub__qr img {
  max-width: 280px;
}
.stub--large .stub__void {
  writing-mode: horizontal-tb;
  transform: none;
  padding: var(--space-6) 0;
}
@media (max-width: 520px) {
  .stub:not(.stub--large) {
    --qr-w: 128px;
  }
  .stub__place {
    gap: var(--space-4);
  }
}
</style>
