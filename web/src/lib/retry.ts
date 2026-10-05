// Повтор запроса при временном сбое сервера (ADR 028). Во время
// переключения базы api несколько секунд отвечает 503 с Retry-After;
// упавший экземпляр обрывает соединение (ADR 027). Повтор безопасен для
// чтения и для изменяющих запросов с ключом идемпотентности: сервер вернёт
// прежний результат, а не выполнит запрос второй раз.

// Сколько всего повторять: дольше покупатель ждать не станет.
export const RETRY_FOR_MS = 30_000

const TEMPORARY = new Set([502, 503, 504])

// Сбой, после которого стоит повторить. status 0 — соединение оборвалось;
// code — код ошибки из тела ответа: «запрос с этим ключом ещё выполняется».
export function isTemporary(status: number, code?: string): boolean {
  return status === 0 || TEMPORARY.has(status) || code === 'request_in_progress'
}

// Пауза перед повтором: сколько просит сервер (Retry-After, не больше 5 с),
// иначе 0,25 с, затем вдвое больше, не дольше 4 с.
export function retryDelayMs(attempt: number, retryAfter: string | null): number {
  const s = retryAfter === null ? NaN : Number(retryAfter)
  if (Number.isFinite(s) && s >= 0) return Math.min(s, 5) * 1000
  return Math.min(250 * 2 ** attempt, 4000)
}
