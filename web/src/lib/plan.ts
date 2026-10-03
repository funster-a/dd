import type { Layout, PriceCategory, SectionAvailability } from '@/api/types'

// План большой площадки (ADR 024): сектора — многоугольники вокруг поля,
// цвет — ценовая категория, насыщенность — сколько мест осталось. Места
// рисуются только внутри выбранного сектора, поэтому стадион на 24 тысячи
// мест открывается так же быстро, как зал.

export interface PlanSector {
  name: string
  number: string // подпись на плане: «12» из «Сектор 12»
  stand: string
  points: string // атрибут points у <polygon>
  cx: number
  cy: number
  category: number
  priceTiyn: number
  available: number | null // null — занятость ещё не загружена
  total: number
}

export interface StadiumPlan {
  width: number
  height: number
  field: [number, number, number, number]
  fieldLabel: string
  sectors: PlanSector[]
}

export function buildPlan(layout: Layout, prices: PriceCategory[], counts: SectionAvailability[] | undefined): StadiumPlan | null {
  const plan = layout.plan
  if (!plan) return null
  const byPrice = [...prices].sort((a, b) => b.price_tiyn - a.price_tiyn || a.name.localeCompare(b.name))
  const color = new Map<string, number>()
  const priceOf = new Map<string, number>()
  byPrice.forEach((p, i) => {
    for (const s of p.sections) {
      color.set(s, (i % 6) + 1)
      priceOf.set(s, p.price_tiyn)
    }
  })
  const avail = new Map((counts ?? []).map((c) => [c.section, c]))

  const sectors: PlanSector[] = []
  for (const s of layout.sections) {
    if (!s.outline?.length) continue
    const total = s.kind === 'seat' ? (s.rows ?? []).reduce((n, r) => n + r.seats.length, 0) : (s.capacity ?? 0)
    const a = avail.get(s.name)
    const c = centroid(s.outline)
    sectors.push({
      name: s.name,
      number: s.name.match(/\d+\s*$/)?.[0].trim() ?? s.name,
      stand: s.stand ?? '',
      points: s.outline.map(([x, y]) => `${x},${y}`).join(' '),
      cx: c[0],
      cy: c[1],
      category: color.get(s.name) ?? 1,
      priceTiyn: priceOf.get(s.name) ?? 0,
      available: a ? a.available : counts ? 0 : null,
      total: a?.total ?? total,
    })
  }
  return { width: plan.width, height: plan.height, field: plan.field, fieldLabel: plan.field_label || 'Поле', sectors }
}

// centroid — центр тяжести многоугольника (для подписи сектора).
export function centroid(pts: [number, number][]): [number, number] {
  let a = 0
  let x = 0
  let y = 0
  for (let i = 0; i < pts.length; i++) {
    const [x0, y0] = pts[i] as [number, number]
    const [x1, y1] = pts[(i + 1) % pts.length] as [number, number]
    const k = x0 * y1 - x1 * y0
    a += k
    x += (x0 + x1) * k
    y += (y0 + y1) * k
  }
  if (Math.abs(a) < 1e-9) {
    return [pts.reduce((n, p) => n + p[0], 0) / pts.length, pts.reduce((n, p) => n + p[1], 0) / pts.length]
  }
  return [x / (3 * a), y / (3 * a)]
}

// fillLevel — сколько мест осталось, для насыщенности цвета: распроданный
// сектор серый, почти распроданный — бледный.
export function fillLevel(s: PlanSector): 'none' | 'few' | 'many' | 'unknown' {
  if (s.available === null) return 'unknown'
  if (s.available <= 0) return 'none'
  return s.available < Math.max(10, s.total * 0.1) ? 'few' : 'many'
}
