<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import BottomSheet from './BottomSheet.vue'
import { ApiError, api } from '@/api/client'
import { useAuth } from '@/composables/auth'
import { normalizePhone } from '@/lib/format'

const { state, login, closeSheet } = useAuth()
const step = ref<'phone' | 'code'>('phone')
const phoneInput = ref('')
const code = ref('')
const error = ref('')
const busy = ref(false)
const resendIn = ref(0)
let timer: ReturnType<typeof setInterval> | undefined

const phone = computed(() => normalizePhone(phoneInput.value))

watch(
  () => state.sheetOpen,
  (open) => {
    if (open) {
      step.value = 'phone'
      code.value = ''
      error.value = ''
    }
  },
)

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
  if (!phone.value) {
    error.value = 'Введите номер полностью, например +7 707 123 45 67'
    return
  }
  busy.value = true
  error.value = ''
  try {
    const res = await api.requestCode(phone.value)
    step.value = 'code'
    countdown(res.resend_after_seconds ?? 60)
  } catch (e) {
    error.value = e instanceof ApiError && e.status === 429 ? 'Код уже отправлен, подождите минуту' : 'Не удалось отправить код, попробуйте ещё раз'
  } finally {
    busy.value = false
  }
}

async function submitCode() {
  if (!phone.value || code.value.length !== 6) return
  busy.value = true
  error.value = ''
  try {
    await login(phone.value, code.value)
  } catch {
    error.value = 'Код не подошёл. Проверьте SMS или запросите новый'
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
  <BottomSheet :open="state.sheetOpen" title="Вход по номеру телефона" @close="closeSheet">
    <form v-if="step === 'phone'" class="login" @submit.prevent="sendCode">
      <p class="login__lead">Пришлём SMS с кодом. Пароль не нужен — номер и есть ваш аккаунт.</p>
      <label class="field">
        <span class="field__label">Телефон</span>
        <input v-model="phoneInput" class="input" type="tel" inputmode="tel" autocomplete="tel" placeholder="+7 707 123 45 67" />
      </label>
      <p v-if="error" class="error-text" role="alert">{{ error }}</p>
      <button class="btn btn--accent login__submit" :disabled="busy">Получить код</button>
    </form>

    <form v-else class="login" @submit.prevent="submitCode">
      <p class="login__lead">
        Код отправлен на <span class="mono">{{ phone }}</span>.
        <button type="button" class="link" @click="step = 'phone'">Изменить</button>
      </p>
      <label class="field">
        <span class="field__label">Код из SMS</span>
        <input v-model="code" class="input input--code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" placeholder="••••••" />
      </label>
      <p v-if="error" class="error-text" role="alert">{{ error }}</p>
      <button class="btn btn--accent login__submit" :disabled="busy || code.length !== 6">Войти</button>
      <button type="button" class="link login__resend" :disabled="resendIn > 0 || busy" @click="sendCode">
        {{ resendIn > 0 ? `Отправить снова через ${resendIn} с` : 'Отправить код снова' }}
      </button>
    </form>
  </BottomSheet>
</template>

<style scoped>
.login {
  display: grid;
  gap: var(--space-4);
}
.login__lead {
  color: var(--ink-2);
}
.login__submit {
  width: 100%;
  margin-top: var(--space-2);
}
.link {
  background: none;
  border: 0;
  padding: 0;
  color: var(--ink);
  text-decoration: underline;
  text-underline-offset: 0.2em;
  cursor: pointer;
}
.link[disabled] {
  color: var(--ink-3);
  text-decoration: none;
  cursor: default;
}
.login__resend {
  justify-self: center;
  font-size: var(--text-sm);
}
</style>
