import { describe, expect, it } from 'vitest'
import { ApiError } from '@/api/client'
import { emptyForm, explain, formFromEvent, toInput, type EventForm } from './eventForm'

const valid = (): EventForm => ({
  ...emptyForm('v1'),
  title: 'Оркестр ветра',
  slug: 'orchestra',
  seat_map_id: 'm1',
  starts: '2026-10-13T19:00',
  ends: '2026-10-13T21:30',
})

describe('event form', () => {
  it('converts wall times of the venue to UTC', () => {
    const r = toInput(valid(), 'Asia/Almaty')
    expect('input' in r && r.input.starts_at).toBe('2026-10-13T14:00:00Z')
    expect('input' in r && r.input.ends_at).toBe('2026-10-13T16:30:00Z')
    expect('input' in r && r.input.sales_end_at).toBeNull()
  })

  it('round-trips an event', () => {
    const r = toInput(valid(), 'Asia/Almaty')
    if (!('input' in r)) throw new Error(r.error)
    const f = formFromEvent({ ...r.input, id: 'e', seat_map_id: 'm1', status: 'draft' } as never, 'Asia/Almaty')
    expect(f.starts).toBe('2026-10-13T19:00')
  })

  it('lets the organizer turn on the waiting room only with a sales start', () => {
    const no = toInput({ ...valid(), waiting_room: true }, 'Asia/Almaty')
    expect('error' in no && no.error).toMatch(/открываются продажи/)
    const yes = toInput({ ...valid(), waiting_room: true, salesStart: '2026-10-01T10:00' }, 'Asia/Almaty')
    expect('input' in yes && yes.input.waiting_room).toBe(true)
    const free = toInput({ ...valid(), admission: 'free_entry', waiting_room: true, salesStart: '2026-10-01T10:00' }, 'Asia/Almaty')
    expect('input' in free && free.input.waiting_room).toBe(false)
  })

  it('drops the seat map for free entry', () => {
    const r = toInput({ ...valid(), admission: 'free_entry' }, 'Asia/Almaty')
    expect('input' in r && r.input.seat_map_id).toBe('')
  })

  it('rejects inconsistent dates and missing fields', () => {
    const err = (f: Partial<EventForm>) => {
      const r = toInput({ ...valid(), ...f }, 'Asia/Almaty')
      return 'error' in r ? r.error : ''
    }
    expect(err({ ends: '2026-10-13T18:00' })).toMatch(/позже начала/)
    expect(err({ salesEnd: '2026-10-14T00:00' })).toMatch(/после окончания/)
    expect(err({ salesStart: '2026-10-10T10:00', salesEnd: '2026-10-09T10:00' })).toMatch(/закрываться позже/)
    expect(err({ title: ' ' })).toMatch(/название/)
    expect(err({ slug: 'Оркестр' })).toMatch(/латиница/)
    expect(err({ seat_map_id: '' })).toMatch(/схему/)
  })

  it('explains API errors in Russian', () => {
    expect(explain(new ApiError(409, 'slug_taken', 'x'))).toMatch(/занят/)
    expect(explain(new ApiError(400, 'invalid_starts_at', 'must be in the future'))).toBe('Проверьте поле «Начало»: must be in the future')
    expect(explain(new Error('boom'), 'fallback')).toBe('fallback')
  })
})
