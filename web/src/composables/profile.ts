import { ref } from 'vue'
import { orgApi } from '@/api/client'
import type { OrgProfile } from '@/api/types'

// Профиль организатора нужен шапке и публичным ссылкам — грузится один раз.
const profile = ref<OrgProfile | null>(null)
let loading: Promise<void> | null = null

export function useProfile() {
  if (!profile.value && !loading) {
    loading = orgApi
      .profile()
      .then((p) => {
        profile.value = p
      })
      .catch(() => {})
      .finally(() => {
        loading = null
      })
  }
  return profile
}

export function resetProfile() {
  profile.value = null
}
