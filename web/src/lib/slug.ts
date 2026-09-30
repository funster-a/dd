// Адрес события в ссылке: латиница, цифры и дефисы, 3–63 символа (ADR 007).
// Из русского и казахского названия — транслитерацией.
const MAP: Record<string, string> = {
  а: 'a', б: 'b', в: 'v', г: 'g', д: 'd', е: 'e', ё: 'e', ж: 'zh', з: 'z', и: 'i', й: 'y', к: 'k', л: 'l', м: 'm',
  н: 'n', о: 'o', п: 'p', р: 'r', с: 's', т: 't', у: 'u', ф: 'f', х: 'h', ц: 'ts', ч: 'ch', ш: 'sh', щ: 'sch',
  ъ: '', ы: 'y', ь: '', э: 'e', ю: 'yu', я: 'ya',
  ә: 'a', ғ: 'g', қ: 'q', ң: 'n', ө: 'o', ұ: 'u', ү: 'u', һ: 'h', і: 'i',
}

export const SLUG_RE = /^[a-z0-9]+(-[a-z0-9]+)*$/

export function slugify(title: string): string {
  const latin = [...title.toLowerCase()].map((c) => MAP[c] ?? c).join('')
  return latin
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63)
    .replace(/-+$/g, '')
}

export const slugValid = (s: string) => s.length >= 3 && s.length <= 63 && SLUG_RE.test(s)
