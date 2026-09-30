import { describe, expect, it } from 'vitest'
import { money, normalizePhone, plural, priceFrom, time } from './format'

describe('format', () => {
  it('formats tiyn as tenge without floats', () => {
    expect(money(500000)).toBe('5 000 ₸')
    expect(money(123456)).toBe('1 234,56 ₸')
    expect(money(0)).toBe('0 ₸')
  })

  it('describes prices', () => {
    expect(priceFrom(null)).toBe('Вход свободный')
    expect(priceFrom(0)).toBe('Бесплатно')
    expect(priceFrom(800000)).toContain('8 000')
  })

  it('shows time in the venue time zone', () => {
    expect(time('2026-10-29T13:20:00Z', 'Asia/Almaty')).toBe('18:20')
  })

  it('normalizes Kazakhstan phone numbers', () => {
    expect(normalizePhone('8 707 123 45 67')).toBe('+77071234567')
    expect(normalizePhone('+7 (707) 123-45-67')).toBe('+77071234567')
    expect(normalizePhone('12')).toBeNull()
  })

  it('pluralizes in Russian', () => {
    expect([1, 2, 5, 11, 21, 22].map((n) => plural(n, 'билет', 'билета', 'билетов'))).toEqual([
      'билет',
      'билета',
      'билетов',
      'билетов',
      'билет',
      'билета',
    ])
  })
})
