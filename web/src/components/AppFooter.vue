<template>
  <footer class="footer">
    <div class="page footer__inner">
      <div>
        <div class="footer__brand">Партер</div>
        <p class="footer__note">Билеты напрямую от организаторов. Места на схеме зала, электронный билет с QR-кодом.</p>
      </div>
      <div class="footer__cols">
        <p class="eyebrow">Покупателям</p>
        <p>Вход по номеру телефона, без паролей.</p>
        <p>Вернуть билет можно в кабинете до срока, указанного на странице события.</p>
        <p class="eyebrow footer__org">Организаторам</p>
        <p><RouterLink to="/org">Кабинет организатора →</RouterLink></p>
      </div>
    </div>
  </footer>
</template>

<style scoped>
.footer__org {
  margin-top: var(--space-3);
}
.footer a {
  color: var(--ink);
}
.footer {
  margin-top: var(--space-8);
  border-top: 1px solid var(--line);
  padding: var(--space-7) 0 var(--space-6);
  color: var(--ink-2);
  font-size: var(--text-sm);
}
.footer__inner {
  display: grid;
  gap: var(--space-6);
  grid-template-columns: 1fr;
}
@media (min-width: 720px) {
  .footer__inner {
    grid-template-columns: 1.4fr 1fr;
  }
}
.footer__brand {
  font-weight: 800;
  font-size: var(--text-2xl);
  letter-spacing: -0.04em;
  color: var(--ink);
  margin-bottom: var(--space-2);
}
.footer__note {
  max-width: 38ch;
}
.footer__cols {
  display: grid;
  gap: var(--space-2);
  align-content: start;
}
</style>
