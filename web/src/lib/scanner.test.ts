import { describe, expect, it } from 'vitest'
import type { ManifestTicket } from '@/api/types'
import { offlineVerdict, placeText, ticketIdFromToken, tokenFromCode } from './scanner'

// Токен как у сервера: base64url без выравнивания от 16 байт id + 16 байт подписи.
function tokenFor(id: string): string {
  const bytes = id.replace(/-/g, '').match(/../g)!.map((h) => parseInt(h, 16))
  const all = [...bytes, ...Array.from({ length: 16 }, (_, i) => i * 7)]
  return btoa(String.fromCharCode(...all)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

const ID = '01a0f236-b2d2-7e33-bfec-bf223d46eb27'
const ticket = (status: ManifestTicket['status'], id = ID): ManifestTicket => ({ id, status, section: 'Партер', row: '3', seat: '11' })

describe('scanner codes', () => {
  it('extracts the token from a ticket link or a bare token', () => {
    const tok = tokenFor(ID)
    expect(tok).toHaveLength(43)
    expect(tokenFromCode(`https://partner.kz/t/${tok}`)).toBe(tok)
    expect(tokenFromCode(`http://localhost:8000/t/${tok}?utm=x`)).toBe(tok)
    expect(tokenFromCode(` ${tok} `)).toBe(tok)
    expect(tokenFromCode('https://example.com/other')).toBeNull()
    expect(tokenFromCode('hello')).toBeNull()
  })

  it('reads the ticket id from the token', () => {
    expect(ticketIdFromToken(tokenFor(ID))).toBe(ID)
    expect(ticketIdFromToken('!!!')).toBeNull()
  })
})

describe('offline verdict', () => {
  const code = `/t/${tokenFor(ID)}`
  it('accepts an issued ticket once, then reports a duplicate', () => {
    const tickets = new Map([[ID, ticket('issued')]])
    const passed = new Set<string>()
    expect(offlineVerdict(tickets, passed, code).result).toBe('accepted')
    passed.add(ID)
    expect(offlineVerdict(tickets, passed, code).result).toBe('duplicate')
  })

  it('rejects revoked, used, foreign and garbage codes', () => {
    expect(offlineVerdict(new Map([[ID, ticket('revoked')]]), new Set(), code).result).toBe('revoked')
    expect(offlineVerdict(new Map([[ID, ticket('used')]]), new Set(), code).result).toBe('duplicate')
    expect(offlineVerdict(new Map(), new Set(), code).result).toBe('invalid')
    expect(offlineVerdict(new Map([[ID, ticket('issued')]]), new Set(), 'WIFI:S:cafe;;').result).toBe('invalid')
  })

  it('formats the place', () => {
    expect(placeText(ticket('issued'))).toBe('Партер · ряд 3 · место 11')
    expect(placeText({ section: 'Танцпол', row: null, seat: '12' })).toBe('Танцпол')
  })
})
