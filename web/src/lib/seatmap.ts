import type { Availability, Layout, PriceCategory, SeatRef } from '@/api/types'
import { priceLookup } from './pricing'

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
  stageLabel: string
  sections: MapSection[]
  zones: GeneralZone[]
  // Категории по убыванию цены: для легенды и цвета мест.
  categories: { name: string; priceTiyn: number; color: number }[]
}

export const seatKey = (r: SeatRef): string => `${r.section}\u001f${r.row}\u001f${r.seat}`

// only — нарисовать один сектор: у большой площадки места показываются
// внутри выбранного на плане сектора (ADR 024). Пустая строка — ни одного
// сектора, только входные зоны и категории.
export function buildSeatMap(layout: Layout, prices: PriceCategory[], only?: string): SeatMap {
  // Цена — у сектора или у ряда (ADR 025).
  const lk = priceLookup(layout, prices)
  const seated = layout.sections.filter((s) => s.kind === 'seat' && (only === undefined || s.name === only))
  const widest = Math.max(1, ...seated.flatMap((s) => (s.rows ?? []).map((r) => r.seats.length)))
  const contentW = widest * PITCH
  const width = contentW + LABEL_W * 2

  const sections: MapSection[] = []
  let y = 64 // место под сцену
  for (const sec of seated) {
    const rows = sec.rows ?? []
    const all = rows.flatMap((r) => r.seats)
    const hasCoords = all.length > 0 && all.every((s) => typeof s.x === 'number' && typeof s.y === 'number')
    const priced = (row: string) => lk.of(sec.name, row) ?? { color: 1, priceTiyn: 0 }
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
          const p = priced(row.label)
          out.seats.push(seat(sec.name, row.label, s.label, sx, sy, p.color, p.priceTiyn))
        }
        out.rowLabels.push({ label: row.label, y: rowY })
      }
      y += (Math.max(...ys) - minY) * scale + PITCH + SECTION_GAP
    } else {
      for (const row of rows) {
        const rowW = row.seats.length * PITCH
        const x0 = LABEL_W + (contentW - rowW) / 2 + PITCH / 2
        const cy = y + RADIUS
        const p = priced(row.label)
        row.seats.forEach((s, i) => out.seats.push(seat(sec.name, row.label, s.label, x0 + i * PITCH, cy, p.color, p.priceTiyn)))
        out.rowLabels.push({ label: row.label, y: cy })
        y += PITCH + ROW_GAP
      }
      y += SECTION_GAP - ROW_GAP
    }
    sections.push(out)
  }

  const zones: GeneralZone[] = layout.sections
    .filter((s) => s.kind === 'general')
    .map((s) => {
      const p = lk.of(s.name)
      return { name: s.name, capacity: s.capacity ?? 0, category: p?.color ?? 1, priceTiyn: p?.priceTiyn ?? 0 }
    })

  return {
    width,
    height: Math.max(y, 120),
    stageY: 20,
    stageLabel: layout.plan?.field_label ? layout.plan.field_label.toUpperCase() : 'СЦЕНА',
    sections,
    zones,
    categories: lk.categories,
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

// serviceFee — сервисный сбор с билета, как его считает сервер (ADR 019):
// ставка в сотых долях процента, округление до тиына половиной вверх.
// Целые числа, без float (CLAUDE.md, правило 5). Итог заказа всё равно
// решает сервер — здесь только предпросмотр в корзине.
export function serviceFee(priceTiyn: number, bps: number): number {
  if (priceTiyn <= 0 || bps <= 0) return 0
  return Math.floor((priceTiyn * bps + 5000) / 10000)
}

// cartFee — сервисный сбор за все билеты корзины: считается с каждого
// билета отдельно, как на сервере, поэтому суммы совпадают до тиына.
export function cartFee(c: Cart, zones: GeneralZone[], bps: number): number {
  let sum = 0
  for (const s of c.seats.values()) sum += serviceFee(s.priceTiyn, bps)
  for (const [name, q] of c.general) sum += serviceFee(zones.find((z) => z.name === name)?.priceTiyn ?? 0, bps) * q
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
