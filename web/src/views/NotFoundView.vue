<template>
  <section class="page nf">
    <p class="eyebrow">Ошибка 404</p>
    <h1 class="nf__title">Этот ряд <span class="serif">пуст</span></h1>
    <p class="nf__text">Страницы нет или ссылка устарела.</p>
    <RouterLink to="/" class="btn">На афишу</RouterLink>
  </section>
</template>

<style scoped>
.nf {
  padding-top: var(--space-8);
  display: grid;
  gap: var(--space-4);
  justify-items: start;
}
.nf__title {
  font-size: var(--text-4xl);
}
.nf__title .serif {
  color: var(--accent);
  font-weight: 500;
}
.nf__text {
  color: var(--ink-2);
  margin-bottom: var(--space-3);
}
</style>
