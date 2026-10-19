import { describe, expect, it } from 'vitest'
import { atMostTickets, money, normalizePhone, orderStatusText, plural, priceFrom, ticketPrice, time } from './format'

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

  it('calls a zero price free', () => {
    expect(ticketPrice(0)).toBe('Бесплатно')
    expect(ticketPrice(500000)).toBe(money(500000))
  })

  it('labels a free order as placed, not paid', () => {
    expect(orderStatusText('paid', 0)).toBe('Оформлен')
    expect(orderStatusText('paid', 500000)).toBe('Оплачен')
    expect(orderStatusText('refunded', 0)).toBe('Возвращён')
    expect(orderStatusText('pending', 500000)).toBe('Ждёт оплаты')
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

  it('states the ticket limit in the genitive', () => {
    expect([1, 2, 6, 11, 21].map((n) => atMostTickets(n).replace(/\u00a0/g, ' '))).toEqual([
      'Не больше 1 билета',
      'Не больше 2 билетов',
      'Не больше 6 билетов',
      'Не больше 11 билетов',
      'Не больше 21 билета',
    ])
  })
})
