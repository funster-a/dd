<script setup lang="ts">
import { useToast } from '@/composables/toast'

const { items } = useToast()
</script>

<template>
  <div class="toasts" role="status" aria-live="polite">
    <TransitionGroup name="toast">
      <div v-for="t in items" :key="t.id" class="toast" :class="`toast--${t.tone}`">{{ t.text }}</div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.toasts {
  position: fixed;
  left: 50%;
  top: 76px;
  z-index: 60;
  transform: translateX(-50%);
  display: grid;
  gap: 8px;
  width: min(92vw, 420px);
  pointer-events: none;
}
.toast {
  padding: 12px 16px;
  border-radius: var(--radius);
  background: var(--ink);
  color: var(--paper);
  font-size: var(--text-sm);
  box-shadow: var(--shadow);
}
.toast--error {
  background: var(--danger);
  color: #fff;
}
.toast--ok {
  background: var(--ok);
  color: #fff;
}
.toast-enter-active,
.toast-leave-active {
  transition: all 0.2s var(--ease);
}
.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}
</style>
