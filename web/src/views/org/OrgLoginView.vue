<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ApiError, orgApi } from '@/api/client'
import { useOrgAuth } from '@/composables/orgAuth'

const route = useRoute()
const router = useRouter()
const { state, login } = useOrgAuth()

const step = ref<'email' | 'code'>('email')
const email = ref(state.email)
const code = ref('')
const error = ref('')
const busy = ref(false)
const resendIn = ref(0)
let timer: ReturnType<typeof setInterval> | undefined

const emailOk = computed(() => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.value.trim()))

function countdown(seconds: number) {
  resendIn.value = seconds
  clearInterval(timer)
  timer = setInterval(() => {
    resendIn.value = Math.max(0, resendIn.value - 1)
    if (resendIn.value === 0) clearInterval(timer)
  }, 1000)
}
onBeforeUnmount(() => clearInterval(timer))

async function sendCode() {
  if (!emailOk.value) {
    error.value = 'Введите email, на который зарегистрирован кабинет'
    return
  }
  busy.value = true
  error.value = ''
  try {
    const res = await orgApi.requestCode(email.value.trim().toLowerCase())
    step.value = 'code'
    countdown(res.resend_after_seconds ?? 60)
  } catch (e) {
    error.value = e instanceof ApiError && e.status === 429 ? 'Код уже отправлен, подождите минуту' : 'Не удалось отправить код, попробуйте ещё раз'
  } finally {
    busy.value = false
  }
}

async function submitCode() {
  if (code.value.length !== 6) return
  busy.value = true
  error.value = ''
  try {
    await login(email.value.trim().toLowerCase(), code.value)
    const next = typeof route.query.next === 'string' && route.query.next.startsWith('/org') ? route.query.next : '/org'
    await router.replace(next)
  } catch {
    error.value = 'Код не подошёл. Проверьте почту или запросите новый'
    code.value = ''
  } finally {
    busy.value = false
  }
}

watch(code, (v) => {
  code.value = v.replace(/\D/g, '').slice(0, 6)
  if (code.value.length === 6) submitCode()
})
</script>

<template>
  <div class="login">
    <aside class="login__poster" aria-hidden="true">
      <div class="poster__top">
        <span class="brand-mark"></span>
        <span class="poster__brand">Партер</span>
        <span class="poster__tag mono">для организаторов</span>
      </div>
      <p class="poster__title">
        Ваш зал.<br />
        Ваши билеты.<br />
        <span class="serif">без посредников</span>
      </p>
      <ul class="poster__facts mono">
        <li>Схема зала за пять минут</li>
        <li>Продажа с вашей страницы</li>
        <li>Контроль входа с телефона</li>
      </ul>
    </aside>

    <main class="login__form">
      <RouterLink to="/" class="back">← На афишу</RouterLink>
      <div class="form-wrap">
        <p class="eyebrow">Кабинет организатора</p>
        <h1 class="title">Вход</h1>

        <form v-if="step === 'email'" class="form" @submit.prevent="sendCode">
          <label class="field">
            <span class="field__label">Рабочий email</span>
            <input v-model="email" class="input" type="email" autocomplete="email" inputmode="email" placeholder="you@club.kz" autofocus />
          </label>
          <p v-if="error" class="error-text" role="alert">{{ error }}</p>
          <button class="btn btn--accent" :disabled="busy">Получить код</button>
          <p class="hint">Пароль не нужен: пришлём шестизначный код. Кабинет открывает администратор платформы.</p>
        </form>

        <form v-else class="form" @submit.prevent="submitCode">
          <p class="sent">
            Код отправлен на <b>{{ email }}</b>
            <button type="button" class="link" @click="step = 'email'">Изменить</button>
          </p>
          <label class="field">
            <span class="field__label">Код из письма</span>
            <input v-model="code" class="input input--code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" placeholder="••••••" autofocus />
          </label>
          <p v-if="error" class="error-text" role="alert">{{ error }}</p>
          <button class="btn btn--accent" :disabled="busy || code.length !== 6">Войти</button>
          <button type="button" class="link" :disabled="resendIn > 0 || busy" @click="sendCode">
            {{ resendIn > 0 ? `Отправить ещё раз через ${resendIn} с` : 'Отправить код ещё раз' }}
          </button>
        </form>
      </div>
    </main>
  </div>
</template>

<style scoped>
.login {
  min-height: 100dvh;
  display: grid;
}
@media (min-width: 900px) {
  .login {
    grid-template-columns: 1.1fr 1fr;
  }
}
.login__poster {
  --mark: var(--accent);
  --mark-bg: #16140f;
  background: #16140f;
  color: #f1ece2;
  padding: clamp(24px, 5vw, 64px);
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  gap: var(--space-7);
  position: relative;
  overflow: hidden;
}
/* Перфорация по краю, как у корешка билета. */
.login__poster::after {
  content: '';
  position: absolute;
  top: 0;
  bottom: 0;
  right: -9px;
  width: 18px;
  background: radial-gradient(circle at 9px 12px, var(--paper) 6px, transparent 6.5px) 0 0 / 18px 24px repeat-y;
}
@media (max-width: 899px) {
  .login__poster {
    min-height: 260px;
  }
  .login__poster::after {
    display: none;
  }
  .poster__facts {
    display: none;
  }
}
.poster__top {
  display: flex;
  align-items: center;
  gap: 10px;
}
.poster__brand {
  font-weight: 800;
  font-size: 1.25rem;
  letter-spacing: -0.04em;
}
.poster__tag {
  margin-left: auto;
  font-size: var(--text-xs);
  text-transform: uppercase;
  letter-spacing: 0.12em;
  opacity: 0.6;
}
.poster__title {
  font-size: clamp(2.6rem, 1.6rem + 4vw, 5.5rem);
  font-weight: 800;
  line-height: 0.95;
  letter-spacing: -0.045em;
}
.poster__title .serif {
  color: var(--accent);
  font-weight: 500;
  letter-spacing: -0.02em;
}
.poster__facts {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 10px;
  font-size: var(--text-sm);
  opacity: 0.75;
}
.poster__facts li::before {
  content: '— ';
  color: var(--accent);
}
.login__form {
  padding: clamp(24px, 5vw, 64px);
  display: flex;
  flex-direction: column;
}
.back {
  font-size: var(--text-sm);
  color: var(--ink-2);
  text-decoration: none;
  align-self: flex-end;
}
.form-wrap {
  margin: auto 0;
  width: 100%;
  max-width: 400px;
  align-self: center;
  display: grid;
  gap: var(--space-3);
  padding: var(--space-7) 0;
}
.title {
  font-size: var(--text-3xl);
  margin-bottom: var(--space-4);
}
.form {
  display: grid;
  gap: var(--space-4);
}
.hint {
  font-size: var(--text-sm);
  color: var(--ink-2);
}
.sent {
  color: var(--ink-2);
}
.link {
  background: none;
  border: 0;
  padding: 0;
  font: inherit;
  color: var(--ink);
  text-decoration: underline;
  text-underline-offset: 3px;
  cursor: pointer;
  justify-self: start;
  margin-left: 6px;
}
.link[disabled] {
  color: var(--ink-3);
  cursor: default;
  text-decoration: none;
}
</style>
