import type { ManifestTicket, ScanOutcome } from '@/api/types'

// Логика сканера контролёра (ADR 013). QR билета — ссылка /t/{токен},
// токен — base64url(16 байт id билета + 16 байт подписи). Секрета подписи
// у сканера нет: без сети он достаёт id и ищет билет в загруженном списке,
// а окончательное решение сервер принимает при синхронизации.

// tokenFromCode: из содержимого QR — ссылки или самого токена — токен.
export function tokenFromCode(code: string): string | null {
  const s = code.trim()
  const m = /\/t\/([A-Za-z0-9_-]+)\/?(?:[?#].*)?$/.exec(s)
  const token = m ? m[1]! : s
  return /^[A-Za-z0-9_-]{43}$/.test(token) ? token : null
}

// ticketIdFromToken: id билета (UUID) из токена или null.
export function ticketIdFromToken(token: string): string | null {
  let bin: string
  try {
    bin = atob(token.replace(/-/g, '+').replace(/_/g, '/') + '=')
  } catch {
    return null
  }
  if (bin.length !== 32) return null
  const hex = [...bin.slice(0, 16)].map((c) => c.charCodeAt(0).toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

export interface OfflineVerdict {
  result: ScanOutcome
  ticket?: ManifestTicket
}

// offlineVerdict решает без сети: по списку билетов и проходам, уже
// засчитанным на этом устройстве. Подделку с чужим id сервер отклонит при
// синхронизации; подобрать настоящий id нельзя — 74 случайных бита UUIDv7.
export function offlineVerdict(tickets: Map<string, ManifestTicket>, passed: Set<string>, code: string): OfflineVerdict {
  const token = tokenFromCode(code)
  const id = token && ticketIdFromToken(token)
  if (!id) return { result: 'invalid' }
  const ticket = tickets.get(id)
  if (!ticket) return { result: 'invalid' } // не этого события или вовсе не билет
  if (ticket.status === 'revoked') return { result: 'revoked', ticket }
  if (ticket.status === 'used' || passed.has(id)) return { result: 'duplicate', ticket }
  return { result: 'accepted', ticket }
}

export const OUTCOME_TEXT: Record<ScanOutcome, { title: string; tone: 'ok' | 'warn' | 'bad' }> = {
  accepted: { title: 'Проходите', tone: 'ok' },
  duplicate: { title: 'Уже прошёл', tone: 'warn' },
  revoked: { title: 'Билет возвращён', tone: 'bad' },
  invalid: { title: 'Не билет', tone: 'bad' },
  wrong_event: { title: 'Другое событие', tone: 'bad' },
}

export function placeText(t: { section?: string; row?: string | null; seat?: string }): string {
  if (!t.section) return ''
  return t.row ? `${t.section} · ряд ${t.row} · место ${t.seat}` : t.section
}
