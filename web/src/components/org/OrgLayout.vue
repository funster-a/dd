<script setup lang="ts">
import { useRouter } from 'vue-router'
import { useOrgAuth } from '@/composables/orgAuth'

const { state, logout } = useOrgAuth()
const router = useRouter()

async function signOut() {
  await logout()
  await router.push({ name: 'org-login' })
}
</script>

<template>
  <div class="shell">
    <a class="skip" href="#org-main">К содержанию</a>
    <aside class="rail">
      <RouterLink :to="{ name: 'org-events' }" class="rail__brand" aria-label="Кабинет — события">
        <span class="brand-mark"></span>
        <span class="rail__name">Партер</span>
        <span class="rail__tag mono">кабинет</span>
      </RouterLink>
      <nav class="rail__nav" aria-label="Разделы кабинета">
        <RouterLink :to="{ name: 'org-events' }" class="rail__link" :class="{ 'is-active': $route.path === '/org' || $route.path.startsWith('/org/events') }">
          <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M3 5.5h14v3a1.8 1.8 0 0 0 0 3.5v3H3v-3a1.8 1.8 0 0 0 0-3.5z" /></svg>
          События
        </RouterLink>
        <RouterLink :to="{ name: 'org-venues' }" class="rail__link" :class="{ 'is-active': $route.path.startsWith('/org/venues') }">
          <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M3 16V8l7-4 7 4v8M7 16v-4h6v4" /></svg>
          Площадки
        </RouterLink>
      </nav>
      <div class="rail__foot">
        <a href="/" target="_blank" rel="noopener" class="rail__small">Афиша ↗</a>
        <span class="rail__email" :title="state.email">{{ state.email }}</span>
        <button type="button" class="rail__small rail__out" @click="signOut">Выйти</button>
      </div>
    </aside>
    <main id="org-main" class="main">
      <RouterView v-slot="{ Component }">
        <Transition name="page" mode="out-in">
          <component :is="Component" />
        </Transition>
      </RouterView>
    </main>
  </div>
</template>

<style scoped>
.shell {
  min-height: 100dvh;
  display: grid;
  grid-template-columns: 232px 1fr;
  background: linear-gradient(90deg, var(--paper-2) 231px, var(--line) 231px 232px, transparent 232px);
}
.rail {
  --mark-bg: var(--paper-2);
  position: sticky;
  top: 0;
  height: 100dvh;
  display: flex;
  flex-direction: column;
  gap: var(--space-6);
  padding: var(--space-5) var(--space-4);
  background: var(--paper-2);
  border-right: 1px solid var(--line);
}
.rail__brand {
  display: flex;
  align-items: center;
  gap: 10px;
  text-decoration: none;
  padding: 0 var(--space-2);
}
.rail__name {
  font-weight: 800;
  font-size: 1.2rem;
  letter-spacing: -0.04em;
}
.rail__tag {
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.14em;
  color: var(--accent);
  margin-top: 3px;
}
.rail__nav {
  display: grid;
  gap: 2px;
}
.rail__link {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px var(--space-3);
  border-radius: var(--radius-sm);
  text-decoration: none;
  color: var(--ink-2);
  font-weight: 500;
  font-size: var(--text-sm);
}
.rail__link svg {
  width: 18px;
  height: 18px;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.6;
  stroke-linejoin: round;
}
.rail__link:hover {
  color: var(--ink);
  background: var(--paper-3);
}
.rail__link.is-active {
  color: var(--ink);
  background: var(--paper);
  box-shadow: inset 3px 0 0 var(--accent);
}
.rail__foot {
  margin-top: auto;
  display: grid;
  gap: 6px;
  padding: var(--space-3);
  border-top: 1px solid var(--line);
  font-size: var(--text-xs);
}
.rail__email {
  color: var(--ink-2);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rail__small {
  color: var(--ink-2);
  text-decoration: none;
  font: inherit;
  background: none;
  border: 0;
  padding: 0;
  cursor: pointer;
  text-align: left;
}
.rail__small:hover {
  color: var(--ink);
}
.main {
  min-width: 0;
  padding: clamp(20px, 3vw, 40px) clamp(16px, 3.5vw, 48px) var(--space-8);
}
.skip {
  position: absolute;
  left: -999px;
  top: 8px;
  z-index: 100;
  background: var(--ink);
  color: var(--paper);
  padding: 8px 12px;
  border-radius: 6px;
}
.skip:focus {
  left: 8px;
}
@media (max-width: 860px) {
  .shell {
    grid-template-columns: 1fr;
    background: none;
  }
  .rail {
    position: sticky;
    z-index: 20;
    height: auto;
    flex-direction: row;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-2) var(--space-4);
    border-right: 0;
    border-bottom: 1px solid var(--line);
  }
  .rail__tag,
  .rail__email,
  .rail__foot a {
    display: none;
  }
  .rail__nav {
    display: flex;
    margin-left: auto;
  }
  .rail__link {
    padding: 8px 10px;
  }
  .rail__link svg {
    display: none;
  }
  .rail__link.is-active {
    box-shadow: inset 0 -2px 0 var(--accent);
    background: transparent;
  }
  .rail__foot {
    margin: 0;
    border: 0;
    padding: 0;
  }
}
</style>
