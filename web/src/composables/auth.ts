import { computed, reactive } from 'vue'
import { api, getToken, onSessionExpired, setToken } from '@/api/client'

// Вход покупателя по телефону и SMS-коду (ADR 006). Экраны, которым нужен
// вход, вызывают requireLogin(): открывается окно входа, промис
// выполняется после успешного входа или отклоняется при закрытии.
const state = reactive({
  token: getToken(),
  sheetOpen: false,
})

let waiting: { resolve: () => void; reject: (e: Error) => void } | null = null

// Сервер не узнал сессию — например, после сброса данных: шапка снова
// предлагает войти, а requireLogin() открывает окно входа.
onSessionExpired((realm) => {
  if (realm === 'buyer') state.token = null
})

export function useAuth() {
  const authed = computed(() => !!state.token)

  function requireLogin(): Promise<void> {
    if (state.token) return Promise.resolve()
    state.sheetOpen = true
    return new Promise((resolve, reject) => {
      waiting = { resolve, reject }
    })
  }

  async function login(phone: string, code: string) {
    const res = await api.login(phone, code)
    setToken(res.token)
    state.token = res.token
    state.sheetOpen = false
    waiting?.resolve()
    waiting = null
  }

  function closeSheet() {
    state.sheetOpen = false
    waiting?.reject(new Error('login cancelled'))
    waiting = null
  }

  async function logout() {
    try {
      await api.logout()
    } catch {
      // сессия уже истекла — всё равно выходим
    }
    setToken(null)
    state.token = null
  }

  return { state, authed, requireLogin, login, closeSheet, logout, openSheet: () => (state.sheetOpen = true) }
}
