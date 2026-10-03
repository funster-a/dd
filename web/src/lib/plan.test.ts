import { describe, expect, it } from 'vitest'
import type { Layout, PriceCategory } from '@/api/types'
import { buildPlan, centroid, fillLevel } from './plan'
import { buildSeatMap } from './seatmap'

const row = (label: string, n: number) => ({ label, seats: Array.from({ length: n }, (_, i) => ({ label: String(i + 1) })) })
const layout: Layout = {
  plan: { width: 100, height: 80, field: [30, 20, 40, 40], field_label: 'Поле' },
  sections: [
    { name: 'Сектор 1', kind: 'seat', stand: 'Западная трибуна', outline: [[30, 62], [70, 62], [70, 78], [30, 78]], rows: [row('1', 10), row('2', 10)] },
    { name: 'Сектор 2', kind: 'seat', stand: 'Восточная трибуна', outline: [[30, 2], [70, 2], [70, 18], [30, 18]], rows: [row('1', 50)] },
  ],
}
const prices: PriceCategory[] = [
  { id: 'a', name: 'Запад', price_tiyn: 800000, currency: 'KZT', sections: ['Сектор 1'] },
  { id: 'b', name: 'Восток', price_tiyn: 500000, currency: 'KZT', sections: ['Сектор 2'] },
]

describe('buildPlan', () => {
  it('returns null for a hall without a plan', () => {
    expect(buildPlan({ sections: layout.sections }, prices, [])).toBeNull()
  })

  it('maps sectors, prices and availability', () => {
    const plan = buildPlan(layout, prices, [
      { section: 'Сектор 1', available: 1, total: 20 },
      { section: 'Сектор 2', available: 0, total: 50 },
    ])!
    expect(plan.fieldLabel).toBe('Поле')
    const [w, e] = plan.sectors
    expect(w).toMatchObject({ number: '1', stand: 'Западная трибуна', priceTiyn: 800000, category: 1, available: 1, total: 20 })
    expect(w!.cx).toBeCloseTo(50)
    expect(w!.cy).toBeCloseTo(70)
    expect(fillLevel(w!)).toBe('few')
    expect(fillLevel(e!)).toBe('none')
  })

  it('marks availability unknown until it is loaded', () => {
    const plan = buildPlan(layout, prices, undefined)!
    expect(plan.sectors[0]!.available).toBeNull()
    expect(plan.sectors[0]!.total).toBe(20)
    expect(fillLevel(plan.sectors[0]!)).toBe('unknown')
  })
})

describe('buildSeatMap for one sector', () => {
  it('draws only the selected sector under the field label', () => {
    const map = buildSeatMap(layout, prices, 'Сектор 2')
    expect(map.sections.map((s) => s.name)).toEqual(['Сектор 2'])
    expect(map.sections[0]!.seats).toHaveLength(50)
    expect(map.stageLabel).toBe('ПОЛЕ')
    expect(buildSeatMap(layout, prices, '').sections).toHaveLength(0)
  })
})

it('centroid of a square', () => {
  expect(centroid([[0, 0], [2, 0], [2, 2], [0, 2]])).toEqual([1, 1])
})
