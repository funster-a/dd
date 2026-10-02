// Типы ответов API платформы (README, ADR 007–014). Деньги — целые тиыны,
// время — строки ISO 8601 в UTC.

export interface Venue {
  name: string
  address: string
  timezone: string
  latitude: number | null
  longitude: number | null
}

export interface PriceCategory {
  id: string
  name: string
  price_tiyn: number
  currency: string
  sections: string[]
}

export interface LayoutSeat {
  label: string
  x?: number
  y?: number
}

export interface LayoutRow {
  label: string
  seats: LayoutSeat[]
}

export interface LayoutSection {
  name: string
  kind: 'seat' | 'general'
  rows?: LayoutRow[]
  capacity?: number
}

export interface Layout {
  sections: LayoutSection[]
}

export interface PublicEvent {
  id: string
  admission: 'ticketed' | 'free_entry'
  status: 'published' | 'cancelled'
  organizer_slug: string
  organizer_name: string
  slug: string
  title: string
  description: string
  age_rating: string
  starts_at: string
  ends_at: string
  sales_start_at: string | null
  sales_end_at: string | null
  max_tickets_per_buyer: number
  refund_deadline_hours: number
  cover_image_url: string
  cover_video_url: string | null
  venue: Venue
  prices: PriceCategory[]
  layout: Layout | null
}

export interface UpcomingEvent {
  id: string
  organizer_slug: string
  organizer_name: string
  slug: string
  title: string
  starts_at: string
  age_rating: string
  admission: 'ticketed' | 'free_entry'
  venue: string
  timezone: string
  cover_image_url: string
  min_price_tiyn: number | null
}

export interface SeatRef {
  section: string
  row: string
  seat: string
}

export interface Availability {
  taken: SeatRef[]
  general: { section: string; available: number }[]
  // Сервисный сбор с покупателя в сотых долях процента: 500 = 5 % (ADR 019).
  service_fee_bps: number
  // Окно очереди ожидания при старте продаж (ADR 020) или null.
  queue: { opens_at: string; closes_at: string } | null
}

// Место покупателя в очереди ожидания (ADR 020).
export interface QueueStatus {
  state: 'not_required' | 'not_open' | 'waiting' | 'admitted'
  position?: number
  estimated_wait_seconds?: number
  opens_at?: string
  sales_start_at?: string
}

export interface OrderItem {
  kind: 'seat' | 'general'
  section: string
  row: string | null
  seat: string
  price_tiyn: number
  fee_tiyn: number
}

export type OrderStatus = 'pending' | 'paid' | 'expired' | 'cancelled' | 'partially_refunded' | 'refunded'

export interface Order {
  id: string
  event_id: string
  status: OrderStatus
  email: string
  total_tiyn: number // к оплате: билеты и сервисный сбор
  fee_tiyn: number
  currency: string
  expires_at: string
  paid_at: string | null
  created_at: string
  items: OrderItem[]
}

export interface OrderSummary {
  id: string
  status: OrderStatus
  total_tiyn: number
  items: number
  created_at: string
  expires_at: string
  event_id: string
  event_slug: string
  event_title: string
  event_starts_at: string
  organizer_slug: string
  venue: string
  timezone: string
}

export interface Payment {
  id: string
  order_id: string
  status: string
  amount_tiyn: number
  currency: string
  payment_url: string
}

export interface Ticket {
  id: string
  status: 'issued' | 'used' | 'revoked'
  kind: 'seat' | 'general'
  section: string
  row: string | null
  seat: string
  url: string
}

export interface TicketView {
  status: 'issued' | 'used' | 'revoked'
  event: string
  age_rating: string
  starts_at: string
  ends_at: string
  venue: string
  address: string
  timezone: string
  kind: 'seat' | 'general'
  section: string
  row: string | null
  seat: string
  url: string
}

export interface LoginResponse {
  token: string
  expires_at: string
  principal: { kind: string; subject_id: string }
}

// Кабинет организатора (ADR 007, 009, 013, 014).

export type EventStatus = 'draft' | 'published' | 'cancelled'

export interface OrgVenue {
  id: string
  name: string
  address: string
  timezone: string
  latitude: number | null
  longitude: number | null
  created_at: string
  updated_at: string
}

export interface VenueInput {
  name: string
  address: string
  timezone: string
  latitude: number | null
  longitude: number | null
}

export interface OrgSeatMap {
  id: string
  venue_id: string
  name: string
  layout: Layout
  seat_count: number
  created_at: string
}

export interface OrgEvent {
  id: string
  venue_id: string
  admission: 'ticketed' | 'free_entry'
  seat_map_id: string | null
  slug: string
  title: string
  description: string
  age_rating: string
  status: EventStatus
  starts_at: string
  ends_at: string
  sales_start_at: string | null
  sales_end_at: string | null
  max_tickets_per_buyer: number
  refund_deadline_hours: number
  cover_image_key: string | null
  cover_video_key: string | null
  cover_image_url?: string
  cover_video_url?: string
  published_at: string | null
  created_at: string
  updated_at: string
}

export interface EventInput {
  venue_id: string
  admission: 'ticketed' | 'free_entry'
  seat_map_id: string
  slug: string
  title: string
  description: string
  age_rating: string
  starts_at: string
  ends_at: string
  sales_start_at: string | null
  sales_end_at: string | null
  max_tickets_per_buyer: number
  refund_deadline_hours: number
}

export interface PriceInput {
  name: string
  price_tiyn: number
  sections: string[]
}

export interface UploadTarget {
  key: string
  upload_url: string
  method: string
  headers: Record<string, string>
  expires_at: string
}

export interface EventReport {
  event: { id: string; title: string; status: EventStatus; admission: string; starts_at: string }
  seats: { capacity: number; sold: number; held: number; available: number; occupancy_permille: number }
  tickets: { active: number; used: number; refunded: number }
  money: {
    paid_orders: number
    gross_tiyn: number
    refunded_tiyn: number
    net_tiyn: number
    average_order_tiyn: number
    currency: string
  }
  categories: { name: string; price_tiyn: number; capacity: number; sold: number; revenue_tiyn: number }[]
  generated_at: string
}

export interface ScannerLink {
  id: string
  event_id: string
  name: string
  created_at: string
  revoked_at: string | null
  url?: string
  token?: string
}

// Сканер контролёра.

export interface ManifestTicket {
  id: string
  status: 'issued' | 'used' | 'revoked'
  section: string
  row: string | null
  seat: string
}

export interface Manifest {
  event: { id: string; title: string; starts_at: string; ends_at: string; venue: string; timezone: string }
  tickets: ManifestTicket[]
  generated_at: string
}

export type ScanOutcome = 'accepted' | 'duplicate' | 'revoked' | 'invalid' | 'wrong_event'

export interface ScanResult {
  client_scan_id: string
  result: ScanOutcome
  first_scanned_at?: string
  section?: string
  row?: string | null
  seat?: string
}

export interface OrgProfile {
  id: string
  name: string
  slug: string
}
