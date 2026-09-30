<script setup lang="ts">
import { computed, ref } from 'vue'
import type { UpcomingEvent } from '@/api/types'
import { dayMonth, priceFrom, time, weekday } from '@/lib/format'

const props = defineProps<{ event: UpcomingEvent; featured?: boolean }>()
const broken = ref(false)
const to = computed(() => `/e/${props.event.organizer_slug}/${props.event.slug}`)
const price = computed(() => (props.event.admission === 'free_entry' ? priceFrom(null) : priceFrom(props.event.min_price_tiyn)))
</script>

<template>
  <RouterLink :to="to" class="card" :class="{ 'card--featured': featured }">
    <div class="card__poster">
      <img v-if="event.cover_image_url && !broken" :src="event.cover_image_url" :alt="''" loading="lazy" @error="broken = true" decoding="async" />
      <div v-else class="card__placeholder" aria-hidden="true">{{ event.title.slice(0, 1) }}</div>
      <span class="card__age mono">{{ event.age_rating }}</span>
    </div>
    <div class="card__body">
      <div class="card__date mono">
        <span class="card__day">{{ dayMonth(event.starts_at, event.timezone) }}</span>
        <span class="card__sep">·</span>
        <span>{{ weekday(event.starts_at, event.timezone) }}</span>
        <span class="card__sep">·</span>
        <span>{{ time(event.starts_at, event.timezone) }}</span>
      </div>
      <h3 class="card__title">{{ event.title }}</h3>
      <div class="card__meta">
        <span>{{ event.venue }}</span>
        <span class="card__price">{{ price }}</span>
      </div>
    </div>
  </RouterLink>
</template>

<style scoped>
.card {
  display: grid;
  gap: var(--space-4);
  text-decoration: none;
  color: inherit;
}
.card__poster {
  position: relative;
  aspect-ratio: 4 / 5;
  overflow: hidden;
  border-radius: var(--radius);
  background: var(--paper-2);
  outline: 1px solid var(--line);
  outline-offset: -1px;
}
.card__poster img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  transition: transform 0.5s var(--ease);
}
.card:hover .card__poster img {
  transform: scale(1.035);
}
.card__placeholder {
  display: grid;
  place-items: center;
  height: 100%;
  font-size: 7rem;
  font-weight: 800;
  color: var(--paper-3);
  background: var(--ink);
}
.card__age {
  position: absolute;
  top: 12px;
  right: 12px;
  padding: 3px 8px;
  border-radius: 999px;
  background: var(--paper);
  color: var(--ink);
  font-size: var(--text-xs);
  font-weight: 600;
}
.card__body {
  display: grid;
  gap: var(--space-2);
}
.card__date {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  font-size: var(--text-xs);
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--ink-2);
}
.card__day {
  color: var(--accent);
  font-weight: 600;
}
.card__sep {
  color: var(--ink-3);
}
.card__title {
  font-size: var(--text-xl);
  transition: color 0.15s;
}
.card:hover .card__title {
  color: var(--accent);
}
.card__meta {
  display: flex;
  justify-content: space-between;
  gap: var(--space-3);
  font-size: var(--text-sm);
  color: var(--ink-2);
}
.card__price {
  flex: none;
  color: var(--ink);
  font-weight: 600;
}

/* Главное событие: постер слева, текст справа, крупно. */
@media (min-width: 860px) {
  .card--featured {
    grid-template-columns: 1.1fr 1fr;
    align-items: end;
    gap: var(--space-7);
  }
  .card--featured .card__poster {
    aspect-ratio: 5 / 4;
  }
  .card--featured .card__title {
    font-size: var(--text-3xl);
  }
  .card--featured .card__date {
    font-size: var(--text-sm);
  }
  .card--featured .card__meta {
    font-size: var(--text-md);
  }
}
</style>
