// Деньги — целые тиыны (CLAUDE.md, правило 5). Организатор вводит тенге:
// «5000», «5 000», «4 999,50». Разбор без float: строкой.
export function parseTenge(input: string): number | null {
  const s = input.replace(/[\s ₸]/g, '').replace(',', '.')
  const m = /^(\d{1,9})(?:\.(\d{1,2}))?$/.exec(s)
  if (!m) return null
  const whole = Number(m[1])
  const frac = Number((m[2] ?? '').padEnd(2, '0'))
  return whole * 100 + frac
}

// tengeInput: тиыны → строка для поля ввода, без символа валюты.
export function tengeInput(tiyn: number): string {
  const whole = Math.trunc(tiyn / 100)
  const frac = tiyn % 100
  return frac ? `${whole},${String(frac).padStart(2, '0')}` : String(whole)
}
