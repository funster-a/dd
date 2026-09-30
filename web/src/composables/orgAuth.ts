import { computed, reactive } from 'vue'
import { getToken, onSessionExpired, orgApi, setToken } from '@/api/client'
import { resetProfile } from './profile'

// Сессия организатора (ADR 006): вход по коду на email. Отдельно от сессии
// покупателя — у них разные токены.
const EMAIL_KEY = 'dd.org.email'

function readEmail(): string {
  try {
    return localStorage.getItem(EMAIL_KEY) ?? ''
  } catch {
    return ''
  }
}

const state = reactive({ token: getToken('org'), email: readEmail() })

onSessionExpired((realm) => {
  if (realm === 'org') state.token = null
})

export function useOrgAuth() {
  const authed = computed(() => !!state.token)

  async function login(email: string, code: string) {
    const res = await orgApi.login(email, code)
    resetProfile()
    setToken(res.token, 'org')
    state.token = res.token
    state.email = email
    try {
      localStorage.setItem(EMAIL_KEY, email)
    } catch {
      // не страшно: email нужен только для подписи в меню
    }
  }

  async function logout() {
    try {
      await orgApi.logout()
    } catch {
      // сессия уже истекла
    }
    setToken(null, 'org')
    state.token = null
    // Следующий вход может быть другим организатором.
    resetProfile()
  }

  return { state, authed, login, logout }
}
