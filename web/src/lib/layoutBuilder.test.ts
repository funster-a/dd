import { describe, expect, it } from 'vitest'
import { newSection, rowLabel, sectionSeats, toLayout, validate, type SectionDraft } from './layoutBuilder'

const seat = (over: Partial<SectionDraft> = {}): SectionDraft => ({ ...newSection(1), ...over })

describe('layout builder', () => {
  it('builds rows with fan growth', () => {
    const l = toLayout([seat({ rows: 3, seatsPerRow: 10, growth: 2 })])
    const rows = l.sections[0]!.rows!
    expect(rows.map((r) => r.seats.length)).toEqual([10, 12, 14])
    expect(rows[2]!.seats.at(-1)!.label).toBe('14')
    expect(sectionSeats(seat({ rows: 3, seatsPerRow: 10, growth: 2 }))).toBe(36)
  })

  it('never makes a row empty with negative growth', () => {
    const d = seat({ rows: 5, seatsPerRow: 3, growth: -2 })
    expect(toLayout([d]).sections[0]!.rows!.map((r) => r.seats.length)).toEqual([3, 1, 1, 1, 1])
  })

  it('labels rows with letters skipping ambiguous ones', () => {
    expect([0, 1, 5, 6, 8, 9].map((i) => rowLabel(i, 'letters')).join('')).toBe('АБЕЖИК')
    expect(rowLabel(28, 'letters')).toBe('АА')
    expect(rowLabel(0, 'numbers')).toBe('1')
  })

  it('emits general sections with capacity only', () => {
    const l = toLayout([seat({ name: ' Танцпол ', kind: 'general', capacity: 300 })])
    expect(l.sections[0]).toEqual({ name: 'Танцпол', kind: 'general', capacity: 300 })
  })

  it('validates names and sizes', () => {
    expect(validate([])).toMatch(/хотя бы один/)
    expect(validate([seat({ name: 'A' }), seat({ name: 'a' })])).toMatch(/повторяется/)
    expect(validate([seat({ name: '  ' })])).toMatch(/название/)
    expect(validate([seat({ rows: 0 })])).toMatch(/рядов/)
    expect(validate([seat({ seatsPerRow: 501 })])).toMatch(/мест в ряду/)
    expect(validate([seat({ kind: 'general', capacity: 0 })])).toMatch(/вместимость/)
    expect(validate([seat(), seat({ name: 'Балкон' })])).toBeNull()
  })
})
