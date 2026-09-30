import type { Layout, LayoutSection } from '@/api/types'

// Конструктор схемы зала: организатор описывает сектор параметрами — сколько
// рядов, мест в ряду, на сколько мест каждый следующий ряд шире («веер»), —
// а не рисует каждое место. Результат — Layout из ADR 005, его проверяет и
// сохраняет сервер.

export type RowLabels = 'numbers' | 'letters'

export interface SectionDraft {
  name: string
  kind: 'seat' | 'general'
  rows: number
  seatsPerRow: number
  growth: number // на сколько мест следующий ряд длиннее предыдущего
  rowLabels: RowLabels
  capacity: number // для входной зоны
}

export const LIMITS = { rows: 500, seats: 500, capacity: 50_000, sections: 200 } as const

// Буквы рядов без Ё, Й, Ъ, Ы, Ь — так принято в залах, чтобы не путать.
const LETTERS = 'АБВГДЕЖЗИКЛМНОПРСТУФХЦЧШЩЭЮЯ'

export function rowLabel(i: number, style: RowLabels): string {
  if (style === 'numbers') return String(i + 1)
  const n = LETTERS.length
  return i < n ? LETTERS[i]! : LETTERS[Math.floor(i / n) - 1]! + LETTERS[i % n]!
}

export const newSection = (n: number): SectionDraft => ({
  name: n === 1 ? 'Партер' : `Сектор ${n}`,
  kind: 'seat',
  rows: 10,
  seatsPerRow: 16,
  growth: 0,
  rowLabels: 'numbers',
  capacity: 200,
})

export function seatsInRow(d: SectionDraft, i: number): number {
  return Math.max(1, Math.min(LIMITS.seats, d.seatsPerRow + d.growth * i))
}

export function sectionSeats(d: SectionDraft): number {
  if (d.kind === 'general') return d.capacity
  let n = 0
  for (let i = 0; i < d.rows; i++) n += seatsInRow(d, i)
  return n
}

// validate возвращает текст ошибки по-русски или null.
export function validate(drafts: SectionDraft[]): string | null {
  if (drafts.length === 0) return 'Добавьте хотя бы один сектор'
  if (drafts.length > LIMITS.sections) return `Не больше ${LIMITS.sections} секторов`
  const names = new Set<string>()
  for (const d of drafts) {
    const name = d.name.trim()
    if (!name) return 'У каждого сектора должно быть название'
    if (names.has(name.toLowerCase())) return `Название «${name}» повторяется`
    names.add(name.toLowerCase())
    if (d.kind === 'general') {
      if (!Number.isInteger(d.capacity) || d.capacity < 1 || d.capacity > LIMITS.capacity)
        return `«${name}»: вместимость от 1 до ${LIMITS.capacity}`
      continue
    }
    if (!Number.isInteger(d.rows) || d.rows < 1 || d.rows > LIMITS.rows) return `«${name}»: рядов от 1 до ${LIMITS.rows}`
    if (!Number.isInteger(d.seatsPerRow) || d.seatsPerRow < 1 || d.seatsPerRow > LIMITS.seats)
      return `«${name}»: мест в ряду от 1 до ${LIMITS.seats}`
    if (!Number.isInteger(d.growth)) return `«${name}»: прирост мест — целое число`
  }
  return null
}

export function toLayout(drafts: SectionDraft[]): Layout {
  return {
    sections: drafts.map((d): LayoutSection => {
      const name = d.name.trim()
      if (d.kind === 'general') return { name, kind: 'general', capacity: d.capacity }
      return {
        name,
        kind: 'seat',
        rows: Array.from({ length: d.rows }, (_, i) => ({
          label: rowLabel(i, d.rowLabels),
          seats: Array.from({ length: seatsInRow(d, i) }, (_, k) => ({ label: String(k + 1) })),
        })),
      }
    }),
  }
}
