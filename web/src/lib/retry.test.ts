import { describe, expect, it } from 'vitest'
import { isTemporary, retryDelayMs } from './retry'

describe('isTemporary', () => {
  it('retries outages and an in-flight key, not client errors', () => {
    expect(isTemporary(0)).toBe(true)
    expect(isTemporary(503)).toBe(true)
    expect(isTemporary(502)).toBe(true)
    expect(isTemporary(409, 'request_in_progress')).toBe(true)
    expect(isTemporary(409, 'seat_taken')).toBe(false)
    expect(isTemporary(500)).toBe(false)
    expect(isTemporary(400)).toBe(false)
  })
})

describe('retryDelayMs', () => {
  it('follows Retry-After, capped', () => {
    expect(retryDelayMs(0, '2')).toBe(2000)
    expect(retryDelayMs(3, '60')).toBe(5000)
  })
  it('backs off exponentially without Retry-After', () => {
    expect(retryDelayMs(0, null)).toBe(250)
    expect(retryDelayMs(2, null)).toBe(1000)
    expect(retryDelayMs(10, 'soon')).toBe(4000)
  })
})
