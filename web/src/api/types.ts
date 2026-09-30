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
}

export interface OrderItem {
  kind: 'seat' | 'general'
  section: string
  row: string | null
  seat: string
  price_tiyn: number
}

export type OrderStatus = 'pending' | 'paid' | 'expired' | 'cancelled' | 'partially_refunded' | 'refunded'

export interface Order {
  id: string
  event_id: string
  status: OrderStatus
  email: string
  total_tiyn: number
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
