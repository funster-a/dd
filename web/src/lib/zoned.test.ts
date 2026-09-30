import { describe, expect, it } from 'vitest'
import { utcToWall, wallToUtc } from './zoned'
import { parseTenge, tengeInput } from './money'

describe('wall time in venue zone', () => {
  it('converts Almaty wall time to UTC and back', () => {
    const utc = wallToUtc('2026-10-13T19:00', 'Asia/Almaty')
    expect(utc).toBe('2026-10-13T14:00:00Z')
    expect(utcToWall(utc!, 'Asia/Almaty')).toBe('2026-10-13T19:00')
  })

  it('handles daylight saving shifts', () => {
    // Берлин: 29 марта 2026 переход на летнее время (UTC+2).
    expect(wallToUtc('2026-03-30T10:00', 'Europe/Berlin')).toBe('2026-03-30T08:00:00Z')
    expect(wallToUtc('2026-03-27T10:00', 'Europe/Berlin')).toBe('2026-03-27T09:00:00Z')
  })

  it('rejects malformed input', () => {
    expect(wallToUtc('13.10.2026 19:00', 'Asia/Almaty')).toBeNull()
  })
})

describe('tenge input', () => {
  it('parses to integer tiyn without float errors', () => {
    expect(parseTenge('5000')).toBe(500000)
    expect(parseTenge('5 000')).toBe(500000)
    expect(parseTenge('4 999,50')).toBe(499950)
    expect(parseTenge('0.1')).toBe(10)
    expect(parseTenge('19.99')).toBe(1999) // 19.99 * 100 во float дало бы 1998.999…
    expect(parseTenge('0')).toBe(0)
  })

  it('rejects garbage and negatives', () => {
    for (const bad of ['', 'abc', '-5', '1.234', '1e5']) expect(parseTenge(bad)).toBeNull()
  })

  it('formats back for editing', () => {
    expect(tengeInput(500000)).toBe('5000')
    expect(tengeInput(499950)).toBe('4999,50')
    expect(tengeInput(5)).toBe('0,05')
  })
})
