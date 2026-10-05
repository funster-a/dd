import type { Layout, LayoutSection, PriceCategory, RowRange } from '@/api/types'

// Цена места: сектор целиком или диапазон рядов (ADR 025). Категории
// нумеруются по убыванию цены — так же, как их цвета на схеме.

// PALETTE — сколько цветов категорий в теме (tokens.css, --cat-1…9).
export const PALETTE = 9

export interface PriceLookup {
  // Категории по убыванию цены: для легенды и цвета мест.
  categories: { name: string; priceTiyn: number; color: number }[]
  // Категория места: номер цвета и цена; ряд не нужен входной зоне.
  of(section: string, row?: string): { color: number; priceTiyn: number } | undefined
  // Самая низкая и самая высокая цена сектора: «от … ₸» на плане.
  range(section: string): { min: number; max: number; color: number } | undefined
}

export function sortByPrice(prices: PriceCategory[]): PriceCategory[] {
  return [...prices].sort((a, b) => b.price_tiyn - a.price_tiyn || a.name.localeCompare(b.name))
}

export function priceLookup(layout: Layout, prices: PriceCategory[]): PriceLookup {
  const byPrice = sortByPrice(prices)
  const whole = new Map<string, number>()
  const rows = new Map<string, Map<string, number>>()
  const sections = new Map(layout.sections.map((s) => [s.name, s]))
  byPrice.forEach((p, i) => {
    for (const s of p.sections) whole.set(s, i)
    for (const r of p.rows ?? []) {
      const labels = rowLabels(sections.get(r.section))
      const a = labels.indexOf(r.from)
      const b = labels.indexOf(r.to)
      if (a < 0 || b < a) continue
      const m = rows.get(r.section) ?? new Map<string, number>()
      for (let k = a; k <= b; k++) m.set(labels[k]!, i)
      rows.set(r.section, m)
    }
  })
  const info = (i: number) => ({ color: (i % PALETTE) + 1, priceTiyn: byPrice[i]!.price_tiyn })
  return {
    categories: byPrice.map((p, i) => ({ name: p.name, priceTiyn: p.price_tiyn, color: (i % PALETTE) + 1 })),
    of(section, row) {
      const w = whole.get(section)
      if (w !== undefined) return info(w)
      const r = row === undefined ? undefined : rows.get(section)?.get(row)
      return r === undefined ? undefined : info(r)
    },
    range(section) {
      const w = whole.get(section)
      if (w !== undefined) {
        const p = byPrice[w]!.price_tiyn
        return { min: p, max: p, color: (w % PALETTE) + 1 }
      }
      const m = rows.get(section)
      if (!m?.size) return undefined
      // Цвет сектора на плане — категория большинства его рядов.
      const count = new Map<number, number>()
      for (const i of m.values()) count.set(i, (count.get(i) ?? 0) + 1)
      const main = [...count.entries()].sort((a, b) => b[1] - a[1] || a[0] - b[0])[0]![0]
      const ps = [...new Set(m.values())].map((i) => byPrice[i]!.price_tiyn)
      return { min: Math.min(...ps), max: Math.max(...ps), color: (main % PALETTE) + 1 }
    },
  }
}

export const rowLabels = (s: LayoutSection | undefined): string[] => (s?.rows ?? []).map((r) => r.label)

// Черновик категорий в редакторе цен: ряды — индексы в порядке схемы.
export interface DraftRange {
  section: string
  from: number
  to: number
}
export interface DraftCategory {
  name: string
  price: string
  sections: string[]
  rows: DraftRange[]
}

// owners — какой категорией продаётся каждый ряд сектора (-1 — никакой).
export function owners(cats: DraftCategory[], section: string, rowCount: number): number[] {
  const out = Array<number>(rowCount).fill(-1)
  cats.forEach((c, i) => {
    if (c.sections.includes(section)) out.fill(i)
    for (const r of c.rows) if (r.section === section) for (let k = r.from; k <= r.to; k++) out[k] = i
  })
  return out
}

// setOwners записывает владельцев рядов сектора обратно в категории:
// сектор целиком, если все ряды у одной категории, иначе — диапазоны.
function setOwners(cats: DraftCategory[], section: string, own: number[]) {
  for (const c of cats) {
    c.sections = c.sections.filter((s) => s !== section)
    c.rows = c.rows.filter((r) => r.section !== section)
  }
  if (own.length && own.every((o) => o === own[0]) && own[0]! >= 0) {
    cats[own[0]!]!.sections.push(section)
    return
  }
  let start = 0
  for (let k = 1; k <= own.length; k++) {
    if (k === own.length || own[k] !== own[start]) {
      const o = own[start]!
      if (o >= 0) cats[o]!.rows.push({ section, from: start, to: k - 1 })
      start = k
    }
  }
}

// assignRows отдаёт ряды from..to сектора категории i; остальные ряды
// сектора остаются у прежних владельцев, поэтому покрытие не рвётся.
export function assignRows(cats: DraftCategory[], i: number, section: string, rowCount: number, from: number, to: number) {
  const own = owners(cats, section, rowCount)
  for (let k = Math.min(from, to); k <= Math.max(from, to); k++) own[k] = i
  setOwners(cats, section, own)
}

// assignWhole отдаёт сектор категории i целиком или забирает его, если он
// уже целиком у неё.
export function assignWhole(cats: DraftCategory[], i: number, section: string, rowCount: number) {
  const own = owners(cats, section, Math.max(rowCount, 1))
  const mine = own.every((o) => o === i)
  setOwners(cats, section, own.map(() => (mine ? -1 : i)))
}

export function toRowRanges(layout: Layout, rows: DraftRange[]): RowRange[] {
  return rows.map((r) => {
    const labels = rowLabels(layout.sections.find((s) => s.name === r.section))
    return { section: r.section, from: labels[r.from]!, to: labels[r.to]! }
  })
}

export function fromRowRanges(layout: Layout, rows: RowRange[] | undefined): DraftRange[] {
  return (rows ?? []).flatMap((r) => {
    const labels = rowLabels(layout.sections.find((s) => s.name === r.section))
    const from = labels.indexOf(r.from)
    const to = labels.indexOf(r.to)
    return from < 0 || to < from ? [] : [{ section: r.section, from, to }]
  })
}
