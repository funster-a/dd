import type {
  SeatMapTemplate,
  Availability,
  EventInput,
  EventReport,
  LoginResponse,
  Manifest,
  OrgEvent,
  OrgProfile,
  OrgSeatMap,
  OrgVenue,
  PriceCategory,
  PriceInput,
  ScanResult,
  ScannerLink,
  UploadTarget,
  VenueInput,
  Layout,
  Order,
  OrderSummary,
  Payment,
  PublicEvent,
  QueueStatus,
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

// Сессии покупателя и организатора хранятся раздельно: вход в кабинет не
// выбрасывает из покупок и наоборот.
export type Realm = 'buyer' | 'org'
const TOKEN_KEYS: Record<Realm, string> = { buyer: 'dd.session', org: 'dd.org.session' }

export function getToken(realm: Realm = 'buyer'): string | null {
  try {
    return localStorage.getItem(TOKEN_KEYS[realm])
  } catch {
    return null
  }
}

export function setToken(token: string | null, realm: Realm = 'buyer'): void {
  try {
    if (token) localStorage.setItem(TOKEN_KEYS[realm], token)
    else localStorage.removeItem(TOKEN_KEYS[realm])
  } catch {
    // приватный режим браузера: сессия живёт до перезагрузки
  }
}

// Подписчики на истечение сессии: экран входа кабинета открывается сам.
const expiredListeners = new Set<(realm: Realm) => void>()
export function onSessionExpired(fn: (realm: Realm) => void): () => void {
  expiredListeners.add(fn)
  return () => expiredListeners.delete(fn)
}

interface RequestOptions {
  method?: string
  body?: unknown
  // Изменяющие запросы идемпотентны по ключу (ADR 008): повтор после обрыва
  // связи с тем же ключом не создаёт второй заказ.
  idempotencyKey?: string
  auth?: boolean
  realm?: Realm
  headers?: Record<string, string>
}

async function send(path: string, opts: RequestOptions): Promise<Response> {
  const headers: Record<string, string> = { Accept: 'application/json', ...opts.headers }
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  if (opts.idempotencyKey) headers['Idempotency-Key'] = opts.idempotencyKey
  const realm = opts.realm ?? 'buyer'
  const token = getToken(realm)
  if (opts.auth !== false && token && !headers.Authorization) headers.Authorization = `Bearer ${token}`
  return fetch(path, {
    method: opts.method ?? 'GET',
    headers,
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
  })
}

async function failure(res: Response, realm: Realm): Promise<ApiError> {
  const data = await res.json().catch(() => null)
  const err = data?.error ?? {}
  if (res.status === 401 && err.code === 'invalid_session') {
    setToken(null, realm)
    expiredListeners.forEach((fn) => fn(realm))
  }
  return new ApiError(res.status, err.code ?? 'http_' + res.status, err.message ?? res.statusText)
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const res = await send(path, opts)
  if (!res.ok) throw await failure(res, opts.realm ?? 'buyer')
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

// Запросы кабинета: сессия организатора.
const org = <T>(path: string, opts: RequestOptions = {}) => request<T>('/v1/organizer' + path, { ...opts, realm: 'org' })

export const newKey = (): string => crypto.randomUUID()

export const api = {
  upcoming: () => request<UpcomingEvent[]>('/v1/public/events', { auth: false }),
  event: (org: string, slug: string) =>
    request<PublicEvent>(`/v1/public/events/${encodeURIComponent(org)}/${encodeURIComponent(slug)}`, { auth: false }),
  // section — места только этого сектора, summary — только сводка по
  // секторам: так план стадиона не грузит 24 тысячи мест (ADR 024).
  availability: (eventId: string, q: { section?: string; summary?: boolean } = {}) => {
    const p = new URLSearchParams()
    if (q.summary) p.set('view', 'summary')
    else if (q.section) p.set('section', q.section)
    const qs = p.size ? `?${p}` : ''
    return request<Availability>(`/v1/events/${eventId}/availability${qs}`, { auth: false })
  },

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
  // Встать в очередь ожидания или узнать своё место: повтор место не меняет.
  joinQueue: (eventId: string) => request<QueueStatus>(`/v1/events/${eventId}/queue`, { method: 'POST' }),
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

// Кабинет организатора.
export const orgApi = {
  requestCode: (email: string) =>
    request<{ resend_after_seconds: number }>('/v1/auth/codes', { method: 'POST', body: { kind: 'organizer', email }, auth: false }),
  login: (email: string, code: string) =>
    request<LoginResponse>('/v1/auth/sessions', { method: 'POST', body: { kind: 'organizer', email, code }, auth: false }),
  logout: () => request<void>('/v1/auth/session', { method: 'DELETE', realm: 'org' }),
  profile: () => org<OrgProfile>('/profile'),

  venues: () => org<{ venues: OrgVenue[] }>('/venues').then((r) => r.venues),
  venue: (id: string) => org<OrgVenue>(`/venues/${id}`),
  createVenue: (v: VenueInput, key: string) => org<OrgVenue>('/venues', { method: 'POST', body: v, idempotencyKey: key }),
  updateVenue: (id: string, v: VenueInput) => org<OrgVenue>(`/venues/${id}`, { method: 'PUT', body: v, idempotencyKey: newKey() }),
  seatMaps: (venueId: string) => org<{ seat_maps: OrgSeatMap[] }>(`/venues/${venueId}/seat-maps`).then((r) => r.seat_maps),
  seatMap: (id: string) => org<OrgSeatMap>(`/seat-maps/${id}`),
  seatMapTemplates: () => org<{ templates: SeatMapTemplate[] }>('/seat-map-templates').then((r) => r.templates),
  seatMapTemplate: (id: string) => org<SeatMapTemplate>(`/seat-map-templates/${id}`),
  createSeatMap: (venueId: string, name: string, layout: Layout, key: string) =>
    org<OrgSeatMap>(`/venues/${venueId}/seat-maps`, { method: 'POST', body: { name, layout }, idempotencyKey: key }),

  events: () => org<{ events: OrgEvent[] }>('/events').then((r) => r.events),
  event: (id: string) => org<OrgEvent>(`/events/${id}`),
  createEvent: (e: EventInput, key: string) => org<OrgEvent>('/events', { method: 'POST', body: e, idempotencyKey: key }),
  updateEvent: (id: string, e: EventInput) => org<OrgEvent>(`/events/${id}`, { method: 'PUT', body: e, idempotencyKey: newKey() }),
  prices: (id: string) => org<{ categories: PriceCategory[] }>(`/events/${id}/prices`).then((r) => r.categories),
  setPrices: (id: string, categories: PriceInput[]) =>
    org<{ categories: PriceCategory[] }>(`/events/${id}/prices`, { method: 'PUT', body: { categories }, idempotencyKey: newKey() }).then(
      (r) => r.categories,
    ),
  createUpload: (id: string, kind: 'cover_image' | 'cover_video', file: File) =>
    org<UploadTarget>(`/events/${id}/media/uploads`, {
      method: 'POST',
      body: { kind, content_type: file.type, size: file.size },
      idempotencyKey: newKey(),
    }),
  setMedia: (id: string, cover_image_key: string, cover_video_key: string | null) =>
    org<OrgEvent>(`/events/${id}/media`, { method: 'PUT', body: { cover_image_key, cover_video_key }, idempotencyKey: newKey() }),
  publish: (id: string, key: string) =>
    org<{ event: OrgEvent; seats: number }>(`/events/${id}/publish`, { method: 'POST', idempotencyKey: key }),
  cancel: (id: string, key: string) => org<OrgEvent>(`/events/${id}/cancel`, { method: 'POST', idempotencyKey: key }),

  report: (id: string) => org<EventReport>(`/events/${id}/report`),
  // CSV отдаётся только с токеном в заголовке, поэтому качаем через fetch.
  exportTickets: async (id: string): Promise<Blob> => {
    const res = await send(`/v1/organizer/events/${id}/report/tickets.csv`, { realm: 'org' })
    if (!res.ok) throw await failure(res, 'org')
    return res.blob()
  },
  scanners: (id: string) => org<ScannerLink[]>(`/events/${id}/scanners`),
  createScanner: (id: string, name: string) =>
    org<ScannerLink>(`/events/${id}/scanners`, { method: 'POST', body: { name }, idempotencyKey: newKey() }),
  revokeScanner: (scannerId: string) => org<void>(`/scanners/${scannerId}`, { method: 'DELETE' }),
}

// Загрузка файла прямо в хранилище по подписанной ссылке (ADR 009).
export async function uploadFile(target: UploadTarget, file: File, onProgress?: (share: number) => void): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open(target.method || 'PUT', target.upload_url)
    for (const [k, v] of Object.entries(target.headers ?? {})) xhr.setRequestHeader(k, v)
    if (!target.headers?.['Content-Type']) xhr.setRequestHeader('Content-Type', file.type)
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress?.(e.loaded / e.total)
    xhr.onload = () => (xhr.status >= 200 && xhr.status < 300 ? resolve() : reject(new ApiError(xhr.status, 'upload_failed', xhr.statusText)))
    xhr.onerror = () => reject(new ApiError(0, 'upload_failed', 'network error'))
    xhr.send(file)
  })
}

// Сканер контролёра: токен ссылки вместо сессии.
export const scannerApi = {
  manifest: (token: string) =>
    request<Manifest>('/v1/scanner/manifest', { auth: false, headers: { Authorization: `Scanner ${token}` } }),
  scan: (token: string, deviceId: string, scans: { client_scan_id: string; code: string; offline: boolean; scanned_at: string }[]) =>
    request<{ results: ScanResult[] }>('/v1/scanner/scans', {
      method: 'POST',
      body: { device_id: deviceId, scans },
      auth: false,
      headers: { Authorization: `Scanner ${token}` },
    }).then((r) => r.results),
}

// Токен билета — последний сегмент ссылки /t/{token}.
export const ticketToken = (url: string): string => url.slice(url.lastIndexOf('/') + 1)
