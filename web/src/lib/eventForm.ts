import type { EventInput, OrgEvent } from '@/api/types'
import { ApiError } from '@/api/client'
import { slugValid } from './slug'
import { utcToWall, wallToUtc } from './zoned'

// Форма события в кабинете: даты — местным временем площадки, как их видит
// организатор; в API уходят в UTC.
export interface EventForm {
  venue_id: string
  admission: 'ticketed' | 'free_entry'
  seat_map_id: string
  slug: string
  title: string
  description: string
  age_rating: string
  starts: string
  ends: string
  salesStart: string
  salesEnd: string
  max_tickets_per_buyer: number
  refund_deadline_hours: number
}

export const AGE_RATINGS = ['0+', '6+', '12+', '16+', '18+']

export function emptyForm(venueId = ''): EventForm {
  return {
    venue_id: venueId,
    admission: 'ticketed',
    seat_map_id: '',
    slug: '',
    title: '',
    description: '',
    age_rating: '12+',
    starts: '',
    ends: '',
    salesStart: '',
    salesEnd: '',
    max_tickets_per_buyer: 6,
    refund_deadline_hours: 24,
  }
}

export function formFromEvent(e: OrgEvent, tz: string): EventForm {
  return {
    venue_id: e.venue_id,
    admission: e.admission,
    seat_map_id: e.seat_map_id ?? '',
    slug: e.slug,
    title: e.title,
    description: e.description,
    age_rating: e.age_rating,
    starts: utcToWall(e.starts_at, tz),
    ends: utcToWall(e.ends_at, tz),
    salesStart: e.sales_start_at ? utcToWall(e.sales_start_at, tz) : '',
    salesEnd: e.sales_end_at ? utcToWall(e.sales_end_at, tz) : '',
    max_tickets_per_buyer: e.max_tickets_per_buyer,
    refund_deadline_hours: e.refund_deadline_hours,
  }
}

// toInput проверяет форму и собирает тело запроса. Ошибка — текст для
// организатора; окончательную проверку делает сервер.
export function toInput(f: EventForm, tz: string): { input: EventInput } | { error: string } {
  if (!f.venue_id) return { error: 'Выберите площадку' }
  if (!f.title.trim()) return { error: 'Укажите название' }
  if (!slugValid(f.slug)) return { error: 'Адрес страницы: 3–63 символа, латиница, цифры и дефисы' }
  if (f.admission === 'ticketed' && !f.seat_map_id) return { error: 'Выберите схему зала или свободный вход' }
  const starts = wallToUtc(f.starts, tz)
  const ends = wallToUtc(f.ends, tz)
  if (!starts) return { error: 'Укажите дату и время начала' }
  if (!ends) return { error: 'Укажите дату и время окончания' }
  if (ends <= starts) return { error: 'Окончание должно быть позже начала' }
  const salesStart = f.salesStart ? wallToUtc(f.salesStart, tz) : null
  const salesEnd = f.salesEnd ? wallToUtc(f.salesEnd, tz) : null
  if (salesStart && salesEnd && salesEnd <= salesStart) return { error: 'Продажи должны закрываться позже, чем открываются' }
  if (salesEnd && salesEnd > ends) return { error: 'Продажи не могут идти после окончания события' }
  if (!Number.isInteger(f.max_tickets_per_buyer) || f.max_tickets_per_buyer < 1) return { error: 'Лимит билетов на покупателя — от 1' }
  if (!Number.isInteger(f.refund_deadline_hours) || f.refund_deadline_hours < 0) return { error: 'Срок возврата — целое число часов от 0' }
  return {
    input: {
      venue_id: f.venue_id,
      admission: f.admission,
      seat_map_id: f.admission === 'ticketed' ? f.seat_map_id : '',
      slug: f.slug,
      title: f.title.trim(),
      description: f.description.trim(),
      age_rating: f.age_rating,
      starts_at: starts,
      ends_at: ends,
      sales_start_at: salesStart,
      sales_end_at: salesEnd,
      max_tickets_per_buyer: f.max_tickets_per_buyer,
      refund_deadline_hours: f.refund_deadline_hours,
    },
  }
}

const FIELD_LABELS: Record<string, string> = {
  title: 'Название',
  slug: 'Адрес страницы',
  description: 'Описание',
  age_rating: 'Возрастное ограничение',
  starts_at: 'Начало',
  ends_at: 'Окончание',
  sales_start_at: 'Начало продаж',
  sales_end_at: 'Конец продаж',
  max_tickets_per_buyer: 'Лимит билетов',
  refund_deadline_hours: 'Срок возврата',
  venue_id: 'Площадка',
  seat_map_id: 'Схема зала',
  admission: 'Формат входа',
}

const CODES: Record<string, string> = {
  slug_taken: 'Такой адрес страницы уже занят другим вашим событием',
  starts_in_past: 'Нельзя опубликовать событие, которое уже началось',
  cover_required: 'Сначала загрузите обложку',
  prices_incomplete: 'Назначьте цену каждому сектору схемы',
  event_published: 'Событие уже опубликовано: схема, цены и даты зафиксированы',
  event_cancelled: 'Событие отменено',
  free_entry: 'У события свободный вход — билеты и цены не нужны',
}

// explain переводит ошибку API в понятный текст.
export function explain(e: unknown, fallback = 'Не получилось сохранить. Попробуйте ещё раз'): string {
  if (!(e instanceof ApiError)) return fallback
  if (CODES[e.code]) return CODES[e.code]!
  if (e.code.startsWith('invalid_')) {
    const field = e.code.slice('invalid_'.length)
    const label = FIELD_LABELS[field] ?? FIELD_LABELS[field.split(/[.[]/)[0] ?? ''] ?? field
    return `Проверьте поле «${label}»: ${e.message}`
  }
  if (e.status === 404) return 'Не найдено — возможно, запись удалена'
  return fallback
}
