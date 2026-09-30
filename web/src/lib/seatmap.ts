import type { Availability, Layout, PriceCategory, SeatRef } from '@/api/types'

// Модель схемы зала для отрисовки в SVG. Если у всех мест сектора есть
// координаты из редактора, место рисуется там; иначе ряды раскладываются
// автоматически: сектор за сектором, ряд под рядом, места по центру.

export const PITCH = 22 // шаг мест
export const RADIUS = 8
const ROW_GAP = 6
const SECTION_GAP = 44
const LABEL_W = 28

export interface MapSeat {
  key: string
  ref: SeatRef
  x: number
  y: number
  category: number // номер цвета категории, 1..6
  priceTiyn: number
}

export interface MapSection {
  name: string
  x: number
  y: number
  width: number
  rowLabels: { label: string; y: number }[]
  seats: MapSeat[]
}

export interface GeneralZone {
  name: string
  capacity: number
  category: number
  priceTiyn: number
}

export interface SeatMap {
  width: number
  height: number
  stageY: number
  sections: MapSection[]
  zones: GeneralZone[]
  // Категории по убыванию цены: для легенды и цвета мест.
  categories: { name: string; priceTiyn: number; color: number }[]
}

export const seatKey = (r: SeatRef): string => `${r.section}\u001f${r.row}\u001f${r.seat}`

export function buildSeatMap(layout: Layout, prices: PriceCategory[]): SeatMap {
  const byPrice = [...prices].sort((a, b) => b.price_tiyn - a.price_tiyn || a.name.localeCompare(b.name))
  const color = new Map<string, number>()
  const priceOf = new Map<string, number>()
  byPrice.forEach((p, i) => {
    for (const s of p.sections) {
      color.set(s, (i % 6) + 1)
      priceOf.set(s, p.price_tiyn)
    }
  })

  const seated = layout.sections.filter((s) => s.kind === 'seat')
  const widest = Math.max(1, ...seated.flatMap((s) => (s.rows ?? []).map((r) => r.seats.length)))
  const contentW = widest * PITCH
  const width = contentW + LABEL_W * 2

  const sections: MapSection[] = []
  let y = 64 // место под сцену
  for (const sec of seated) {
    const rows = sec.rows ?? []
    const all = rows.flatMap((r) => r.seats)
    const hasCoords = all.length > 0 && all.every((s) => typeof s.x === 'number' && typeof s.y === 'number')
    const cat = color.get(sec.name) ?? 1
    const price = priceOf.get(sec.name) ?? 0
    const out: MapSection = { name: sec.name, x: 0, y, width, rowLabels: [], seats: [] }
    y += 26 // подпись сектора

    if (hasCoords) {
      const xs = all.map((s) => s.x as number)
      const ys = all.map((s) => s.y as number)
      const minX = Math.min(...xs)
      const minY = Math.min(...ys)
      const spanX = Math.max(...xs) - minX || 1
      const scale = Math.min(1, contentW / spanX)
      for (const row of rows) {
        let rowY = 0
        for (const s of row.seats) {
          const sx = LABEL_W + ((s.x as number) - minX) * scale + (contentW - spanX * scale) / 2
          const sy = y + ((s.y as number) - minY) * scale + RADIUS
          rowY = sy
          out.seats.push(seat(sec.name, row.label, s.label, sx, sy, cat, price))
        }
        out.rowLabels.push({ label: row.label, y: rowY })
      }
      y += (Math.max(...ys) - minY) * scale + PITCH + SECTION_GAP
    } else {
      for (const row of rows) {
        const rowW = row.seats.length * PITCH
        const x0 = LABEL_W + (contentW - rowW) / 2 + PITCH / 2
        const cy = y + RADIUS
        row.seats.forEach((s, i) => out.seats.push(seat(sec.name, row.label, s.label, x0 + i * PITCH, cy, cat, price)))
        out.rowLabels.push({ label: row.label, y: cy })
        y += PITCH + ROW_GAP
      }
      y += SECTION_GAP - ROW_GAP
    }
    sections.push(out)
  }

  const zones: GeneralZone[] = layout.sections
    .filter((s) => s.kind === 'general')
    .map((s) => ({ name: s.name, capacity: s.capacity ?? 0, category: color.get(s.name) ?? 1, priceTiyn: priceOf.get(s.name) ?? 0 }))

  return {
    width,
    height: Math.max(y, 120),
    stageY: 20,
    sections,
    zones,
    categories: byPrice.map((p, i) => ({ name: p.name, priceTiyn: p.price_tiyn, color: (i % 6) + 1 })),
  }
}

function seat(section: string, row: string, label: string, x: number, y: number, category: number, priceTiyn: number): MapSeat {
  const ref = { section, row, seat: label }
  return { key: seatKey(ref), ref, x, y, category, priceTiyn }
}

export const takenSet = (a: Availability | null): Set<string> => new Set((a?.taken ?? []).map(seatKey))

// Корзина покупателя: выбранные места с рядом и количества во входных зонах.
export interface Cart {
  seats: Map<string, MapSeat>
  general: Map<string, number>
}

export const emptyCart = (): Cart => ({ seats: new Map(), general: new Map() })

export function cartCount(c: Cart): number {
  let n = c.seats.size
  for (const q of c.general.values()) n += q
  return n
}

export function cartTotal(c: Cart, zones: GeneralZone[]): number {
  let sum = 0
  for (const s of c.seats.values()) sum += s.priceTiyn
  for (const [name, q] of c.general) sum += (zones.find((z) => z.name === name)?.priceTiyn ?? 0) * q
  return sum
}

// toggleSeat выбирает или снимает место. Возвращает false, если выбрать
// нельзя: место занято или достигнут лимит билетов на покупателя.
export function toggleSeat(c: Cart, s: MapSeat, taken: Set<string>, limit: number): boolean {
  if (c.seats.has(s.key)) {
    c.seats.delete(s.key)
    return true
  }
  if (taken.has(s.key) || cartCount(c) >= limit) return false
  c.seats.set(s.key, s)
  return true
}

export function setZoneQuantity(c: Cart, zone: string, quantity: number, available: number, limit: number): number {
  const others = cartCount(c) - (c.general.get(zone) ?? 0)
  const q = Math.max(0, Math.min(quantity, available, limit - others))
  if (q === 0) c.general.delete(zone)
  else c.general.set(zone, q)
  return q
}
