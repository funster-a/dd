// Форматирование для людей. Деньги приходят в тиынах (целые числа),
// время — в UTC; показываем в часовом поясе площадки (CLAUDE.md, правило 6).

const nbsp = ' '

export function money(tiyn: number): string {
  const tenge = Math.trunc(tiyn / 100)
  const rest = Math.abs(tiyn % 100)
  const whole = new Intl.NumberFormat('ru-RU').format(tenge).replace(/\s/g, nbsp)
  return (rest ? `${whole},${String(rest).padStart(2, '0')}` : whole) + nbsp + '₸'
}

export function priceFrom(tiyn: number | null): string {
  if (tiyn === null) return 'Вход свободный'
  if (tiyn === 0) return 'Бесплатно'
  return 'от' + nbsp + money(tiyn)
}

function parts(iso: string, timeZone: string, opts: Intl.DateTimeFormatOptions): string {
  try {
    return new Intl.DateTimeFormat('ru-RU', { timeZone, ...opts }).format(new Date(iso))
  } catch {
    return new Intl.DateTimeFormat('ru-RU', opts).format(new Date(iso))
  }
}

export const dayMonth = (iso: string, tz: string) => parts(iso, tz, { day: 'numeric', month: 'long' })
export const weekday = (iso: string, tz: string) => parts(iso, tz, { weekday: 'short' })
export const time = (iso: string, tz: string) => parts(iso, tz, { hour: '2-digit', minute: '2-digit' })
export const fullDate = (iso: string, tz: string) =>
  parts(iso, tz, { weekday: 'long', day: 'numeric', month: 'long', hour: '2-digit', minute: '2-digit' })
export const shortDate = (iso: string, tz: string) => parts(iso, tz, { day: '2-digit', month: '2-digit' })

export function plural(n: number, one: string, few: string, many: string): string {
  const m10 = n % 10
  const m100 = n % 100
  if (m10 === 1 && m100 !== 11) return one
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few
  return many
}

export const tickets = (n: number) => `${n}${nbsp}${plural(n, 'билет', 'билета', 'билетов')}`

// Телефон в E.164 из того, что ввёл человек: 8 707 … → +7707…
export function normalizePhone(input: string): string | null {
  const digits = input.replace(/\D/g, '')
  if (digits.length === 11 && (digits.startsWith('8') || digits.startsWith('7'))) return '+7' + digits.slice(1)
  if (digits.length === 10 && digits.startsWith('7')) return '+7' + digits
  if (input.trim().startsWith('+') && digits.length >= 8 && digits.length <= 15) return '+' + digits
  return null
}

export const orderStatusLabel: Record<string, string> = {
  pending: 'Ждёт оплаты',
  paid: 'Оплачен',
  expired: 'Время вышло',
  cancelled: 'Отменён',
  partially_refunded: 'Частичный возврат',
  refunded: 'Возвращён',
}
