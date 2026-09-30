import type {
  Availability,
  LoginResponse,
  Order,
  OrderSummary,
  Payment,
  PublicEvent,
  SeatRef,
  Ticket,
  TicketView,
  UpcomingEvent,
} from './types'

// Ошибка API в формате {"error": {"code", "message"}} (ADR 007).
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message)
  }
}

const TOKEN_KEY = 'dd.session'

export function getToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY)
  } catch {
    return null
  }
}

export function setToken(token: string | null): void {
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token)
    else localStorage.removeItem(TOKEN_KEY)
  } catch {
    // приватный режим браузера: сессия живёт до перезагрузки
  }
}

interface RequestOptions {
  method?: string
  body?: unknown
  // Изменяющие запросы идемпотентны по ключу (ADR 008): повтор после обрыва
  // связи с тем же ключом не создаёт второй заказ.
  idempotencyKey?: string
  auth?: boolean
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  if (opts.idempotencyKey) headers['Idempotency-Key'] = opts.idempotencyKey
  const token = getToken()
  if (opts.auth !== false && token) headers.Authorization = `Bearer ${token}`

  const res = await fetch(path, {
    method: opts.method ?? 'GET',
    headers,
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
  })
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    const err = data?.error ?? {}
    if (res.status === 401 && err.code === 'invalid_session') setToken(null)
    throw new ApiError(res.status, err.code ?? 'http_' + res.status, err.message ?? res.statusText)
  }
  return data as T
}

export const newKey = (): string => crypto.randomUUID()

export const api = {
  upcoming: () => request<UpcomingEvent[]>('/v1/public/events', { auth: false }),
  event: (org: string, slug: string) =>
    request<PublicEvent>(`/v1/public/events/${encodeURIComponent(org)}/${encodeURIComponent(slug)}`, { auth: false }),
  availability: (eventId: string) => request<Availability>(`/v1/events/${eventId}/availability`, { auth: false }),

  requestCode: (phone: string) =>
    request<{ resend_after_seconds: number }>('/v1/auth/codes', { method: 'POST', body: { kind: 'buyer', phone }, auth: false }),
  login: (phone: string, code: string) =>
    request<LoginResponse>('/v1/auth/sessions', { method: 'POST', body: { kind: 'buyer', phone, code }, auth: false }),
  logout: () => request<void>('/v1/auth/session', { method: 'DELETE' }),

  createOrder: (
    eventId: string,
    body: { seats: SeatRef[]; general: { section: string; quantity: number }[]; email: string },
    key: string,
  ) => request<Order>(`/v1/events/${eventId}/orders`, { method: 'POST', body, idempotencyKey: key }),
  order: (id: string) => request<Order>(`/v1/orders/${id}`),
  cancelOrder: (id: string, key: string) =>
    request<Order>(`/v1/orders/${id}/cancel`, { method: 'POST', idempotencyKey: key }),
  myOrders: () => request<OrderSummary[]>('/v1/me/orders'),
  startPayment: (orderId: string, key: string) =>
    request<Payment>(`/v1/orders/${orderId}/payments`, { method: 'POST', idempotencyKey: key }),

  orderTickets: (orderId: string) => request<Ticket[]>(`/v1/orders/${orderId}/tickets`),
  ticket: (token: string) => request<TicketView>(`/v1/tickets/${encodeURIComponent(token)}`, { auth: false }),
  refund: (orderId: string, ticketIds: string[], key: string) =>
    request<{ amount_tiyn: number }>(`/v1/orders/${orderId}/refunds`, {
      method: 'POST',
      body: { ticket_ids: ticketIds },
      idempotencyKey: key,
    }),
}

// Токен билета — последний сегмент ссылки /t/{token}.
export const ticketToken = (url: string): string => url.slice(url.lastIndexOf('/') + 1)
