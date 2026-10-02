import type { QueueStatus } from '@/api/types'
import { plural } from './format'

// Тексты очереди ожидания (ADR 020). Чистые функции: экран события только
// показывает результат.

export function waitText(seconds: number): string {
  if (seconds < 60) return 'меньше минуты'
  const m = Math.ceil(seconds / 60)
  if (m < 60) return `${m} ${plural(m, 'минута', 'минуты', 'минут')}`
  const h = Math.floor(m / 60)
  const rest = m % 60
  const hours = `${h} ${plural(h, 'час', 'часа', 'часов')}`
  return rest ? `${hours} ${rest} мин` : hours
}

export interface QueueView {
  title: string
  text: string
  // Можно ли оформлять заказ.
  canBuy: boolean
}

// queueView — что показать покупателю. startTime — время старта продаж,
// уже в поясе площадки («19:00»).
export function queueView(st: QueueStatus | null, salesStarted: boolean, startTime: string): QueueView {
  if (!st) {
    return {
      title: 'Продажи пойдут через очередь',
      text: 'Встаньте в очередь, и когда подойдёт ваш черёд, вы сможете выбрать места и оформить заказ.',
      canBuy: false,
    }
  }
  switch (st.state) {
    case 'not_required':
      return { title: '', text: '', canBuy: true }
    case 'admitted':
      return { title: 'Ваша очередь', text: 'Выберите места и оформите заказ. Места не держатся, пока заказ не оформлен.', canBuy: true }
    case 'not_open':
      return { title: 'Очередь ещё не открыта', text: 'Встать в неё можно будет за 15 минут до старта продаж.', canBuy: false }
  }
  if (!salesStarted) {
    return {
      title: 'Вы в очереди',
      text: `Продажи откроются в ${startTime}. Порядок среди всех, кто пришёл до старта, решит жребий — торопиться не нужно. Не закрывайте страницу.`,
      canBuy: false,
    }
  }
  const ahead = Math.max(0, (st.position ?? 1) - 1)
  const title = ahead === 0 ? 'Вы следующий' : `Перед вами ${ahead} ${plural(ahead, 'человек', 'человека', 'человек')}`
  return {
    title,
    text: `Примерно ${waitText(st.estimated_wait_seconds ?? 0)}. Не закрывайте страницу — очередь сама пропустит вас к покупке.`,
    canBuy: false,
  }
}
