<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'

// Окно поверх страницы: снизу на телефоне, по центру на широком экране.
// Закрывается по Esc, клику на фон и кнопке; фокус уходит внутрь окна.
const props = defineProps<{ open: boolean; title: string }>()
const emit = defineEmits<{ close: [] }>()
const panel = ref<HTMLElement | null>(null)
let returnFocus: HTMLElement | null = null

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}

watch(
  () => props.open,
  async (open) => {
    if (open) {
      returnFocus = document.activeElement as HTMLElement | null
      document.addEventListener('keydown', onKey)
      document.body.style.overflow = 'hidden'
      await nextTick()
      panel.value?.querySelector<HTMLElement>('input, button:not(.sheet__close), [href]')?.focus()
    } else {
      document.removeEventListener('keydown', onKey)
      document.body.style.overflow = ''
      returnFocus?.focus()
    }
  },
)
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKey)
  document.body.style.overflow = ''
})
</script>

<template>
  <Teleport to="body">
    <Transition name="sheet">
      <div v-if="open" class="sheet" @click.self="emit('close')">
        <div ref="panel" class="sheet__panel" role="dialog" aria-modal="true" :aria-label="title">
          <div class="sheet__head">
            <h2 class="sheet__title">{{ title }}</h2>
            <button class="sheet__close" type="button" aria-label="Закрыть" @click="emit('close')">
              <svg width="20" height="20" viewBox="0 0 20 20" aria-hidden="true">
                <path d="M5 5l10 10M15 5L5 15" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
              </svg>
            </button>
          </div>
          <slot />
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.sheet {
  position: fixed;
  inset: 0;
  z-index: 50;
  display: flex;
  align-items: flex-end;
  justify-content: center;
  background: rgba(22, 20, 15, 0.42);
}
.sheet__panel {
  width: 100%;
  max-width: 460px;
  max-height: 92dvh;
  overflow: auto;
  background: var(--paper);
  border-radius: var(--radius-lg) var(--radius-lg) 0 0;
  padding: var(--space-5) var(--space-5) calc(var(--space-6) + env(safe-area-inset-bottom));
  box-shadow: var(--shadow);
}
@media (min-width: 640px) {
  .sheet {
    align-items: center;
  }
  .sheet__panel {
    border-radius: var(--radius-lg);
  }
}
.sheet__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
  margin-bottom: var(--space-5);
}
.sheet__title {
  font-size: var(--text-xl);
}
.sheet__close {
  flex: none;
  display: grid;
  place-items: center;
  width: 36px;
  height: 36px;
  border: 1px solid var(--line);
  border-radius: 50%;
  background: transparent;
  cursor: pointer;
}
.sheet-enter-active,
.sheet-leave-active {
  transition: opacity 0.2s var(--ease);
}
.sheet-enter-active .sheet__panel,
.sheet-leave-active .sheet__panel {
  transition: transform 0.25s var(--ease);
}
.sheet-enter-from,
.sheet-leave-to {
  opacity: 0;
}
.sheet-enter-from .sheet__panel,
.sheet-leave-to .sheet__panel {
  transform: translateY(24px);
}
</style>
