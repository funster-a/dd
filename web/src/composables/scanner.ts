import { computed, reactive } from 'vue'
import { ApiError, scannerApi } from '@/api/client'
import type { Manifest, ManifestTicket, ScanOutcome } from '@/api/types'
import { offlineVerdict, ticketIdFromToken, tokenFromCode } from '@/lib/scanner'

// Состояние сканера контролёра. Всё, что нужно без сети, лежит в
// localStorage: токен ссылки, список билетов, очередь офлайн-сканирований и
// проходы, засчитанные на этом устройстве. Перезагрузка страницы без связи
// ничего не теряет.

interface QueuedScan {
  client_scan_id: string
  code: string
  offline: true
  scanned_at: string
  local: ScanOutcome // что показал сканер без сети
}

export interface Shown {
  id: number
  result: ScanOutcome
  offline: boolean
  section?: string
  row?: string | null
  seat?: string
  firstAt?: string
}

const K = { link: 'dd.scanner', manifest: 'dd.scanner.manifest', queue: 'dd.scanner.queue', passed: 'dd.scanner.passed' }

function read<T>(key: string, fallback: T): T {
  try {
    const v = localStorage.getItem(key)
    return v ? (JSON.parse(v) as T) : fallback
  } catch {
    return fallback
  }
}
function write(key: string, v: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(v))
  } catch {
    // память переполнена — сканер работает, но без сохранения
  }
}

const link = read<{ token: string; deviceId: string } | null>(K.link, null)
const state = reactive({
  token: link?.token ?? '',
  deviceId: link?.deviceId ?? '',
  manifest: read<Manifest | null>(K.manifest, null),
  queue: read<QueuedScan[]>(K.queue, []),
  passed: new Set(read<string[]>(K.passed, [])),
  online: navigator.onLine,
  revoked: false,
  syncing: false,
  lastSync: '' as string,
  mismatches: 0, // офлайн пропустили, а сервер счёл повторным
  shown: null as Shown | null,
})

let shownId = 0
const tickets = computed(() => new Map((state.manifest?.tickets ?? []).map((t) => [t.id, t])))

function persist() {
  write(K.queue, state.queue)
  write(K.passed, [...state.passed])
}

// Отметить билет прошедшим в локальном списке: повторный скан без сети
// покажет «уже прошёл».
function markUsed(id: string) {
  state.passed.add(id)
  const t = state.manifest?.tickets.find((x) => x.id === id)
  if (t && t.status === 'issued') t.status = 'used'
}

export function useScanner() {
  // setLink: новая ссылка из адреса. Другой токен — другое событие или вход:
  // очередь прежней ссылки сначала отправляется, потом сбрасывается.
  async function setLink(token: string) {
    if (token === state.token) return
    if (state.token && state.queue.length) await sync().catch(() => {})
    state.token = token
    state.deviceId = `${navigator.platform || 'device'}-${crypto.randomUUID().slice(0, 8)}`
    state.manifest = null
    state.queue = []
    state.passed = new Set()
    state.revoked = false
    write(K.link, { token: state.token, deviceId: state.deviceId })
    write(K.manifest, null)
    persist()
  }

  async function refreshManifest() {
    if (!state.token) return
    try {
      const m = await scannerApi.manifest(state.token)
      // Проходы этого устройства, ещё не отправленные, не должны «воскреснуть».
      for (const t of m.tickets) if (state.passed.has(t.id) && t.status === 'issued') t.status = 'used'
      state.manifest = m
      state.online = true
      write(K.manifest, m)
    } catch (e) {
      handleError(e)
    }
  }

  function handleError(e: unknown) {
    if (e instanceof ApiError && (e.code === 'scanner_revoked' || e.code === 'not_found' || e.status === 401)) state.revoked = true
    else if (!(e instanceof ApiError)) state.online = false // сеть
  }

  function show(s: Omit<Shown, 'id'>) {
    state.shown = { ...s, id: ++shownId }
  }

  async function scan(code: string) {
    const scan = { client_scan_id: crypto.randomUUID(), code, scanned_at: new Date().toISOString() }
    if (state.online) {
      try {
        const [r] = await scannerApi.scan(state.token, state.deviceId, [{ ...scan, offline: false }])
        if (r) {
          const id = ticketIdFromToken(tokenFromCode(code) ?? '')
          if (id && (r.result === 'accepted' || r.result === 'duplicate')) markUsed(id)
          persist()
          show({ result: r.result, offline: false, section: r.section, row: r.row, seat: r.seat, firstAt: r.first_scanned_at })
          return
        }
      } catch (e) {
        handleError(e)
        if (state.revoked) return
      }
    }
    // Без сети решает список билетов; сервер узнает при синхронизации.
    const v = offlineVerdict(tickets.value, state.passed, code)
    if (v.result === 'accepted' && v.ticket) markUsed(v.ticket.id)
    state.queue.push({ ...scan, offline: true, local: v.result })
    persist()
    const t: Partial<ManifestTicket> = v.ticket ?? {}
    show({ result: v.result, offline: true, section: t.section, row: t.row, seat: t.seat })
  }

  // sync отправляет очередь пачками. Повторная отправка той же пачки после
  // обрыва безопасна: сервер узнаёт сканирования по client_scan_id.
  async function sync() {
    if (!state.token || state.syncing || state.queue.length === 0) return
    state.syncing = true
    try {
      while (state.queue.length) {
        const batch = state.queue.slice(0, 500)
        const results = await scannerApi.scan(
          state.token,
          state.deviceId,
          batch.map(({ local: _local, ...s }) => s),
        )
        const byId = new Map(results.map((r) => [r.client_scan_id, r]))
        for (const s of batch) if (s.local === 'accepted' && byId.get(s.client_scan_id)?.result !== 'accepted') state.mismatches++
        const sent = new Set(batch.map((s) => s.client_scan_id))
        state.queue = state.queue.filter((s) => !sent.has(s.client_scan_id))
        persist()
      }
      state.online = true
      state.lastSync = new Date().toISOString()
    } catch (e) {
      handleError(e)
    } finally {
      state.syncing = false
    }
  }

  const stats = computed(() => {
    const list = state.manifest?.tickets ?? []
    const active = list.filter((t) => t.status !== 'revoked').length
    const used = list.filter((t) => t.status === 'used').length
    return { active, used }
  })

  function forget() {
    state.token = ''
    state.manifest = null
    state.queue = []
    state.passed = new Set()
    write(K.link, null)
    write(K.manifest, null)
    persist()
  }

  return { state, stats, setLink, refreshManifest, scan, sync, forget, dismiss: () => (state.shown = null) }
}
