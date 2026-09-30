// Время события хранится в UTC (CLAUDE.md, правило 6), а организатор вводит
// его как местное время площадки. Поле <input type="datetime-local"> даёт
// «настенное» время без пояса: здесь его переводят в UTC и обратно.

function offsetMinutes(utc: Date, tz: string): number {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: tz,
    hourCycle: 'h23',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).formatToParts(utc)
  const get = (t: string) => Number(parts.find((p) => p.type === t)?.value)
  const asUtc = Date.UTC(get('year'), get('month') - 1, get('day'), get('hour'), get('minute'), get('second'))
  return Math.round((asUtc - utc.getTime()) / 60000)
}

// wallToUtc: «2026-10-13T19:00» в поясе площадки → ISO-строка UTC.
export function wallToUtc(wall: string, tz: string): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(wall)
  if (!m) return null
  const [, y, mo, d, h, mi] = m.map(Number) as [number, number, number, number, number, number]
  const naive = Date.UTC(y, mo - 1, d, h, mi)
  // Два шага: смещение у границы перехода на летнее время может отличаться.
  let utc = naive - offsetMinutes(new Date(naive), tz) * 60000
  utc = naive - offsetMinutes(new Date(utc), tz) * 60000
  return new Date(utc).toISOString().replace('.000Z', 'Z')
}

// utcToWall: ISO-строка UTC → значение для datetime-local в поясе площадки.
export function utcToWall(iso: string, tz: string): string {
  const d = new Date(iso)
  const local = new Date(d.getTime() + offsetMinutes(d, tz) * 60000)
  return local.toISOString().slice(0, 16)
}

// Часовые пояса Казахстана и соседей — первыми в списке.
export const COMMON_TIMEZONES = ['Asia/Almaty', 'Asia/Aqtobe', 'Asia/Aqtau', 'Asia/Oral', 'Asia/Qostanay', 'Asia/Tashkent', 'Asia/Bishkek', 'Europe/Moscow', 'UTC']
