import { describe, expect, it } from 'vitest'
import type { Layout, PriceCategory } from '@/api/types'
import { buildSeatMap, cartCount, cartTotal, emptyCart, seatKey, setZoneQuantity, takenSet, toggleSeat } from './seatmap'

const layout: Layout = {
  sections: [
    {
      name: 'Партер',
      kind: 'seat',
      rows: [
        { label: '1', seats: [{ label: '1' }, { label: '2' }, { label: '3' }] },
        { label: '2', seats: [{ label: '1' }, { label: '2' }] },
      ],
    },
    { name: 'Фан-зона', kind: 'general', capacity: 100 },
  ],
}
const prices: PriceCategory[] = [
  { id: 'a', name: 'Фан', price_tiyn: 200000, currency: 'KZT', sections: ['Фан-зона'] },
  { id: 'b', name: 'Партер', price_tiyn: 500000, currency: 'KZT', sections: ['Партер'] },
]

describe('buildSeatMap', () => {
  const map = buildSeatMap(layout, prices)

  it('lays out every seat with its price and category', () => {
    expect(map.sections).toHaveLength(1)
    expect(map.sections[0]!.seats).toHaveLength(5)
    expect(map.sections[0]!.seats.every((s) => s.priceTiyn === 500000)).toBe(true)
    expect(map.zones).toEqual([{ name: 'Фан-зона', capacity: 100, category: 2, priceTiyn: 200000 }])
  })

  it('orders categories from the most expensive', () => {
    expect(map.categories.map((c) => c.name)).toEqual(['Партер', 'Фан'])
    expect(map.sections[0]!.seats[0]!.category).toBe(1)
  })

  it('centres shorter rows', () => {
    const [a, b] = [map.sections[0]!.seats[0]!, map.sections[0]!.seats[3]!]
    expect(b.x).toBeGreaterThan(a.x)
    expect(b.y).toBeGreaterThan(a.y)
  })

  it('uses editor coordinates when every seat has them', () => {
    const withCoords: Layout = {
      sections: [{ name: 'Партер', kind: 'seat', rows: [{ label: '1', seats: [{ label: '1', x: 0, y: 0 }, { label: '2', x: 40, y: 10 }] }] }],
    }
    const s = buildSeatMap(withCoords, prices).sections[0]!.seats
    expect(s[1]!.x - s[0]!.x).toBeCloseTo(40)
    expect(s[1]!.y - s[0]!.y).toBeCloseTo(10)
  })
})

describe('cart', () => {
  const map = buildSeatMap(layout, prices)
  const [s1, s2, s3] = map.sections[0]!.seats

  it('respects taken seats and the ticket limit', () => {
    const cart = emptyCart()
    const taken = takenSet({ taken: [s2!.ref], general: [] })
    expect(toggleSeat(cart, s1!, taken, 2)).toBe(true)
    expect(toggleSeat(cart, s2!, taken, 2)).toBe(false)
    expect(setZoneQuantity(cart, 'Фан-зона', 5, 100, 2)).toBe(1)
    expect(toggleSeat(cart, s3!, taken, 2)).toBe(false)
    expect(cartCount(cart)).toBe(2)
    expect(cartTotal(cart, map.zones)).toBe(700000)
    expect(toggleSeat(cart, s1!, taken, 2)).toBe(true) // снятие всегда можно
    expect(cartCount(cart)).toBe(1)
  })

  it('caps a zone by what is left', () => {
    const cart = emptyCart()
    expect(setZoneQuantity(cart, 'Фан-зона', 5, 3, 10)).toBe(3)
    expect(setZoneQuantity(cart, 'Фан-зона', 0, 3, 10)).toBe(0)
    expect(cart.general.size).toBe(0)
  })

  it('builds stable keys', () => {
    expect(seatKey({ section: 'A', row: '1', seat: '2' })).toBe(seatKey({ section: 'A', row: '1', seat: '2' }))
  })
})
