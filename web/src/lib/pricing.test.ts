import { describe, expect, it } from 'vitest'
import type { Layout, PriceCategory } from '@/api/types'
import { assignRows, assignWhole, fromRowRanges, owners, priceLookup, toRowRanges, type DraftCategory } from './pricing'
import { buildSeatMap } from './seatmap'
import { buildPlan } from './plan'

const row = (label: string) => ({ label, seats: [{ label: '1' }, { label: '2' }] })
const layout: Layout = {
  plan: { width: 100, height: 100, field: [30, 30, 40, 40], field_label: 'Поле', stage: [20, 40, 8, 20] },
  sections: [
    { name: 'Сектор 1', kind: 'seat', outline: [[0, 80], [100, 80], [100, 100], [0, 100]], rows: ['1', '2', '3', '4'].map(row) },
    { name: 'Gold', kind: 'general', capacity: 50, stand: 'Поле', outline: [[30, 30], [50, 30], [50, 70], [30, 70]] },
  ],
}
const prices: PriceCategory[] = [
  { id: 'a', name: 'Gold', price_tiyn: 35_000_000, currency: 'KZT', sections: ['Gold'] },
  { id: 'b', name: 'Сектор', price_tiyn: 22_000_000, currency: 'KZT', sections: [], rows: [{ section: 'Сектор 1', from: '4', to: '4' }] },
  { id: 'c', name: 'Ряды 1–3', price_tiyn: 12_000_000, currency: 'KZT', sections: [], rows: [{ section: 'Сектор 1', from: '1', to: '3' }] },
]

describe('priceLookup', () => {
  const lk = priceLookup(layout, prices)
  it('prices seats by row and zones as a whole', () => {
    expect(lk.of('Сектор 1', '2')).toEqual({ color: 3, priceTiyn: 12_000_000 })
    expect(lk.of('Сектор 1', '4')).toEqual({ color: 2, priceTiyn: 22_000_000 })
    expect(lk.of('Gold')).toEqual({ color: 1, priceTiyn: 35_000_000 })
    expect(lk.of('Сектор 1')).toBeUndefined()
  })
  it('reports the price range of a split sector', () => {
    // Цвет — категория большинства рядов (три ряда из четырёх).
    expect(lk.range('Сектор 1')).toEqual({ min: 12_000_000, max: 22_000_000, color: 3 })
  })
  it('flows into the seat map and the plan', () => {
    const map = buildSeatMap(layout, prices, 'Сектор 1')
    const seats = map.sections[0]!.seats
    expect(seats.filter((s) => s.priceTiyn === 12_000_000)).toHaveLength(6)
    expect(seats.filter((s) => s.priceTiyn === 22_000_000)).toHaveLength(2)
    const plan = buildPlan(layout, prices, [{ section: 'Сектор 1', available: 8, total: 8 }], [{ section: 'Gold', available: 7 }])!
    expect(plan.stage).toEqual([20, 40, 8, 20])
    const [sector, gold] = plan.sectors
    expect(sector).toMatchObject({ priceTiyn: 12_000_000, priceVaries: true, kind: 'seat' })
    expect(gold).toMatchObject({ number: 'Gold', kind: 'general', available: 7, total: 50, priceVaries: false })
  })
})

describe('draft row ranges', () => {
  const draft = (): DraftCategory[] => [
    { name: 'A', price: '1', sections: ['Сектор 1'], rows: [] },
    { name: 'B', price: '2', sections: [], rows: [] },
  ]
  it('splits a whole sector without leaving rows unpriced', () => {
    const cats = draft()
    assignRows(cats, 1, 'Сектор 1', 4, 0, 2)
    expect(cats[0]).toMatchObject({ sections: [], rows: [{ section: 'Сектор 1', from: 3, to: 3 }] })
    expect(cats[1]).toMatchObject({ sections: [], rows: [{ section: 'Сектор 1', from: 0, to: 2 }] })
    expect(owners(cats, 'Сектор 1', 4)).toEqual([1, 1, 1, 0])
  })
  it('merges back to a whole sector when one category owns every row', () => {
    const cats = draft()
    assignRows(cats, 1, 'Сектор 1', 4, 0, 2)
    assignRows(cats, 1, 'Сектор 1', 4, 3, 3)
    expect(cats[1]).toMatchObject({ sections: ['Сектор 1'], rows: [] })
    expect(cats[0]).toMatchObject({ sections: [], rows: [] })
  })
  it('toggles a whole sector on and off', () => {
    const cats = draft()
    assignWhole(cats, 1, 'Сектор 1', 4)
    expect(cats[1]!.sections).toEqual(['Сектор 1'])
    assignWhole(cats, 1, 'Сектор 1', 4)
    expect(owners(cats, 'Сектор 1', 4)).toEqual([-1, -1, -1, -1])
  })
  it('converts between row labels and indexes', () => {
    const ranges = [{ section: 'Сектор 1', from: '2', to: '3' }]
    const d = fromRowRanges(layout, ranges)
    expect(d).toEqual([{ section: 'Сектор 1', from: 1, to: 2 }])
    expect(toRowRanges(layout, d)).toEqual(ranges)
  })
})
