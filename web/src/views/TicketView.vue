<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import TicketStub from '@/components/TicketStub.vue'
import { ApiError, api } from '@/api/client'
import type { TicketView } from '@/api/types'
import { dayMonth, time, weekday } from '@/lib/format'

// Публичная страница билета по подписанной ссылке: вход не нужен,
// ссылку можно переслать тому, кто пойдёт.
const props = defineProps<{ token: string }>()
const ticket = ref<TicketView | null>(null)
const failed = ref<'' | 'not_found' | 'error'>('')

onMounted(async () => {
  try {
    ticket.value = await api.ticket(props.token)
    document.title = `Билет · ${ticket.value.event}`
  } catch (e) {
    failed.value = e instanceof ApiError && e.status === 404 ? 'not_found' : 'error'
  }
})

const when = computed(() => {
  const t = ticket.value
  return t ? `${dayMonth(t.starts_at, t.timezone)} · ${weekday(t.starts_at, t.timezone)} · ${time(t.starts_at, t.timezone)}` : ''
})
</script>

<template>
  <div class="page wrap">
    <div v-if="failed" class="empty">
      <h1>{{ failed === 'not_found' ? 'Билет не найден' : 'Не удалось загрузить билет' }}</h1>
      <p>Проверьте ссылку: её можно открыть из письма или раздела «Мои билеты».</p>
    </div>
    <div v-else-if="!ticket" class="skeleton" style="height: 560px; border-radius: var(--radius-lg)"></div>
    <template v-else>
      <TicketStub
        v-bind="ticket"
        :title="ticket.event"
        :when="when"
        :where="[ticket.venue, ticket.address].filter(Boolean).join(', ')"
        large
      />
      <p class="note">
        <template v-if="ticket.status === 'issued'">Покажите код на входе. Сделайте скриншот на случай плохой связи.</template>
        <template v-else-if="ticket.status === 'used'">По этому билету уже прошли.</template>
        <template v-else>Билет возвращён и больше не действует.</template>
        <span class="mono age">{{ ticket.age_rating }}</span>
      </p>
    </template>
  </div>
</template>

<style scoped>
.wrap {
  max-width: 460px;
  padding-top: var(--space-7);
  padding-bottom: var(--space-8);
  display: grid;
  gap: var(--space-4);
}
.empty {
  display: grid;
  gap: var(--space-3);
}
.empty h1 {
  font-size: var(--text-3xl);
}
.note {
  color: var(--ink-2);
  font-size: var(--text-sm);
  text-align: center;
}
.age {
  margin-left: var(--space-2);
  padding: 2px 6px;
  border: 1px solid var(--line-strong);
  border-radius: var(--radius-sm);
}
</style>
