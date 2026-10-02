package identity

import (
	"sync"
	"time"
)

// sessionCache — недавно проверенные сессии в памяти процесса (ADR 018).
// Используется только при отказе Redis: пока Redis отвечает, каждая проверка
// идёт в него, и отзыв сессии действует сразу. Ключ — хеш токена, как в Redis.
type sessionCache struct {
	mu      sync.Mutex
	entries map[string]cachedSession
	// maxAge — насколько старой может быть последняя успешная проверка, чтобы
	// по ней пустить запрос без Redis.
	maxAge time.Duration
	// limit — сколько сессий держать; при переполнении вытесняются случайные.
	limit int
}

type cachedSession struct {
	sess       Session
	verifiedAt time.Time
}

func newSessionCache(maxAge time.Duration, limit int) *sessionCache {
	return &sessionCache{entries: make(map[string]cachedSession), maxAge: maxAge, limit: limit}
}

func (c *sessionCache) put(key string, s Session, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok && len(c.entries) >= c.limit {
		// Порядок обхода map случаен: вытесняется произвольная запись.
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = cachedSession{sess: s, verifiedAt: now}
}

// get возвращает сессию, если её проверяли не раньше maxAge назад и она
// не истекла сама.
func (c *sessionCache) get(key string, now time.Time) (Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return Session{}, false
	}
	if now.Sub(e.verifiedAt) > c.maxAge || !now.Before(e.sess.ExpiresAt) {
		delete(c.entries, key)
		return Session{}, false
	}
	return e.sess, true
}

func (c *sessionCache) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

func (c *sessionCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
