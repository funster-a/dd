import { reactive } from 'vue'

export interface ToastItem {
  id: number
  text: string
  tone: 'info' | 'error' | 'ok'
}

const items = reactive<ToastItem[]>([])
let seq = 0

export function useToast() {
  function show(text: string, tone: ToastItem['tone'] = 'info') {
    const id = ++seq
    items.push({ id, text, tone })
    setTimeout(() => {
      const i = items.findIndex((t) => t.id === id)
      if (i >= 0) items.splice(i, 1)
    }, 4200)
  }
  return { items, show }
}
