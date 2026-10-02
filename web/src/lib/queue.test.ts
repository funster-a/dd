import { describe, expect, it } from 'vitest'
import { queueView, waitText } from './queue'

describe('waitText', () => {
  it('rounds up to minutes and hours', () => {
    expect(waitText(5)).toBe('меньше минуты')
    expect(waitText(61)).toBe('2 минуты')
    expect(waitText(300)).toBe('5 минут')
    expect(waitText(3600)).toBe('1 час')
    expect(waitText(3600 + 90)).toBe('1 час 2 мин')
  })
})

describe('queueView', () => {
  it('lets the buyer through only when admitted or without a queue', () => {
    expect(queueView(null, true, '19:00').canBuy).toBe(false)
    expect(queueView({ state: 'waiting', position: 3, estimated_wait_seconds: 10 }, true, '19:00').canBuy).toBe(false)
    expect(queueView({ state: 'admitted' }, true, '19:00').canBuy).toBe(true)
    expect(queueView({ state: 'not_required' }, true, '19:00').canBuy).toBe(true)
  })

  it('counts people ahead after the start', () => {
    expect(queueView({ state: 'waiting', position: 1, estimated_wait_seconds: 1 }, true, '19:00').title).toBe('Вы следующий')
    expect(queueView({ state: 'waiting', position: 23, estimated_wait_seconds: 40 }, true, '19:00').title).toBe('Перед вами 22 человека')
  })

  it('explains the lottery before the start', () => {
    const v = queueView({ state: 'waiting', position: 7, estimated_wait_seconds: 900 }, false, '19:00')
    expect(v.text).toContain('19:00')
    expect(v.text).toContain('жребий')
  })
})
