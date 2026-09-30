// Service worker сканера (ADR 016): экран контролёра открывается и без сети.
// Кэшируется только оболочка приложения — HTML и файлы сборки. API (/v1) не
// кэшируется никогда: данные сканера лежат в localStorage и синхронизируются
// своим кодом.
const CACHE = 'dd-scanner-v1'

self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  )
})

// Страница сканера присылает адреса своих файлов: так в кэш попадают и
// лениво загруженные части, которых нет в HTML.
self.addEventListener('message', (e) => {
  if (e.data?.type !== 'precache' || !Array.isArray(e.data.urls)) return
  const urls = e.data.urls.filter((u) => typeof u === 'string' && new URL(u, self.location.origin).origin === self.location.origin)
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(urls)).catch(() => {}))
})

self.addEventListener('fetch', (e) => {
  const req = e.request
  const url = new URL(req.url)
  if (req.method !== 'GET' || url.origin !== self.location.origin || url.pathname.startsWith('/v1/')) return

  // Файлы сборки с хешем не меняются: сначала кэш.
  if (url.pathname.startsWith('/assets/')) {
    e.respondWith(
      caches.match(req).then(
        (hit) =>
          hit ||
          fetch(req).then((res) => {
            if (res.ok) caches.open(CACHE).then((c) => c.put(req, res.clone()))
            return res
          }),
      ),
    )
    return
  }

  // Страница сканера: сначала сеть (свежая сборка), без сети — из кэша.
  if (req.mode === 'navigate' && url.pathname === '/scan') {
    e.respondWith(
      fetch(req)
        .then((res) => {
          if (res.ok) caches.open(CACHE).then((c) => c.put('/scan', res.clone()))
          return res
        })
        .catch(() => caches.match('/scan').then((hit) => hit || Response.error())),
    )
  }
})
