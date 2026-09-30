<script setup lang="ts">
import { useAuth } from '@/composables/auth'

const { authed, openSheet, logout } = useAuth()
</script>

<template>
  <header class="header">
    <div class="page header__inner">
      <RouterLink to="/" class="brand" aria-label="Партер — на главную">
        <span class="brand__mark" aria-hidden="true"></span>
        <span class="brand__name">Партер</span>
      </RouterLink>
      <nav class="nav">
        <RouterLink to="/" class="nav__link">Афиша</RouterLink>
        <RouterLink to="/me" class="nav__link">Мои билеты</RouterLink>
        <button v-if="!authed" class="btn btn--sm btn--ghost" type="button" @click="openSheet">Войти</button>
        <button v-else class="nav__link nav__link--button" type="button" @click="logout">Выйти</button>
      </nav>
    </div>
  </header>
</template>

<style scoped>
.header {
  position: sticky;
  top: 0;
  z-index: 20;
  background: color-mix(in srgb, var(--paper) 88%, transparent);
  backdrop-filter: saturate(1.4) blur(10px);
  border-bottom: 1px solid var(--line);
}
.header__inner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 64px;
}
.brand {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  text-decoration: none;
}
.brand__mark {
  position: relative;
  width: 26px;
  height: 26px;
  border-radius: 6px;
  background: var(--ink);
}
/* Вырезы как у билета и красная полоса — отрывной корешок. */
.brand__mark::before,
.brand__mark::after {
  content: '';
  position: absolute;
  left: 50%;
  width: 8px;
  height: 8px;
  margin-left: -4px;
  border-radius: 50%;
  background: var(--paper);
}
.brand__mark::before {
  top: -4px;
}
.brand__mark::after {
  bottom: -4px;
}
.brand__name {
  font-weight: 800;
  font-size: 1.25rem;
  letter-spacing: -0.04em;
}
.nav {
  display: flex;
  align-items: center;
  gap: clamp(8px, 2.5vw, 24px);
}
.nav__link {
  font-size: var(--text-sm);
  font-weight: 500;
  text-decoration: none;
  color: var(--ink-2);
  padding: 6px 2px;
  border-bottom: 1px solid transparent;
  transition: color 0.15s;
}
.nav__link:hover,
.nav__link.router-link-exact-active {
  color: var(--ink);
  border-bottom-color: var(--ink);
}
.nav__link--button {
  background: none;
  border-top: 0;
  border-left: 0;
  border-right: 0;
  cursor: pointer;
}
@media (max-width: 420px) {
  .nav__link:first-child {
    display: none;
  }
}
</style>
