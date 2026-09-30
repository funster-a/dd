<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import EventCard from '@/components/EventCard.vue'
import { api } from '@/api/client'
import type { UpcomingEvent } from '@/api/types'

const events = ref<UpcomingEvent[] | null>(null)
const failed = ref(false)

onMounted(async () => {
  document.title = 'Партер — билеты на события'
  try {
    events.value = await api.upcoming()
  } catch {
    failed.value = true
    events.value = []
  }
})

const featured = computed(() => events.value?.[0])
const rest = computed(() => events.value?.slice(1) ?? [])
const month = new Intl.DateTimeFormat('ru-RU', { month: 'long', year: 'numeric' }).format(new Date())
</script>

<template>
  <div class="home">
    <section class="page masthead">
      <p class="eyebrow">Афиша · {{ month }}</p>
      <h1 class="masthead__title">
        Лучшие места
        <span class="serif masthead__accent">ещё свободны</span>
      </h1>
      <p class="masthead__lead">
        Концерты, стендап, театр и лекции. Выбирайте место на схеме зала — билет с QR-кодом придёт через минуту после оплаты.
      </p>
    </section>

    <section class="page">
      <div v-if="events === null" class="grid" aria-busy="true">
        <div v-for="i in 3" :key="i" class="sk">
          <div class="skeleton sk__poster"></div>
          <div class="skeleton sk__line"></div>
          <div class="skeleton sk__line sk__line--short"></div>
        </div>
      </div>

      <div v-else-if="events.length === 0" class="empty">
        <p class="empty__title">{{ failed ? 'Афиша не загрузилась' : 'Скоро здесь появятся события' }}</p>
        <p class="empty__text">
          {{ failed ? 'Проверьте соединение и обновите страницу.' : 'Организаторы как раз готовят анонсы. Загляните чуть позже.' }}
        </p>
      </div>

      <template v-else>
        <EventCard v-if="featured" :event="featured" featured class="featured" />
        <div v-if="rest.length" class="section-head">
          <h2 class="section-head__title">Ближайшие события</h2>
          <span class="mono section-head__count">{{ String(rest.length).padStart(2, '0') }}</span>
        </div>
        <div class="grid">
          <EventCard v-for="e in rest" :key="e.id" :event="e" />
        </div>
      </template>
    </section>
  </div>
</template>

<style scoped>
.masthead {
  padding-top: clamp(40px, 8vw, 96px);
  padding-bottom: clamp(32px, 6vw, 72px);
  display: grid;
  gap: var(--space-5);
}
.masthead__title {
  font-size: var(--text-4xl);
  max-width: 12ch;
}
.masthead__accent {
  display: block;
  color: var(--accent);
  font-weight: 500;
}
.masthead__lead {
  max-width: 52ch;
  font-size: var(--text-lg);
  color: var(--ink-2);
}
.featured {
  padding-bottom: var(--space-7);
  border-bottom: 1px solid var(--line);
}
.section-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  margin: var(--space-7) 0 var(--space-5);
}
.section-head__title {
  font-size: var(--text-2xl);
}
.section-head__count {
  color: var(--ink-3);
}
.grid {
  display: grid;
  gap: var(--space-7) var(--space-5);
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
}
.sk {
  display: grid;
  gap: var(--space-3);
}
.sk__poster {
  aspect-ratio: 4 / 5;
}
.sk__line {
  height: 18px;
}
.sk__line--short {
  width: 60%;
}
.empty {
  padding: var(--space-8) 0;
  border-top: 1px solid var(--line);
  display: grid;
  gap: var(--space-2);
}
.empty__title {
  font-size: var(--text-2xl);
  font-weight: 800;
  letter-spacing: -0.03em;
}
.empty__text {
  color: var(--ink-2);
}
</style>
