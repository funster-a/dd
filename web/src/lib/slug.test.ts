import { describe, expect, it } from 'vitest'
import { slugify, slugValid } from './slug'

describe('slugify', () => {
  it('transliterates Russian and Kazakh titles', () => {
    expect(slugify('Чехов. Чайка')).toBe('chehov-chayka')
    expect(slugify('Большой стендап-концерт 2026')).toBe('bolshoy-stendap-kontsert-2026')
    expect(slugify('Қазақ әні')).toBe('qazaq-ani')
    expect(slugify('Jazz Nights: Café')).toBe('jazz-nights-cafe')
  })

  it('produces only valid slugs', () => {
    expect(slugValid(slugify('  —  Оркестр ветра!!  '))).toBe(true)
    expect(slugify('x'.repeat(80)).length).toBe(63)
    expect(slugValid('ab')).toBe(false)
    expect(slugValid('a--b')).toBe(false)
  })
})
