<script setup lang="ts">
import AppHeader from './components/AppHeader.vue'
import AppFooter from './components/AppFooter.vue'
import LoginSheet from './components/LoginSheet.vue'
import ToastHost from './components/ToastHost.vue'
</script>

<template>
  <a class="skip" href="#main">К содержанию</a>
  <AppHeader />
  <main id="main">
    <RouterView v-slot="{ Component }">
      <Transition name="page" mode="out-in">
        <component :is="Component" />
      </Transition>
    </RouterView>
  </main>
  <AppFooter />
  <LoginSheet />
  <ToastHost />
</template>

<style>
#app {
  display: flex;
  flex-direction: column;
  min-height: 100dvh;
}
main {
  flex: 1;
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
.page-enter-active,
.page-leave-active {
  transition:
    opacity 0.18s var(--ease),
    transform 0.18s var(--ease);
}
.page-enter-from {
  opacity: 0;
  transform: translateY(6px);
}
.page-leave-to {
  opacity: 0;
}
</style>
