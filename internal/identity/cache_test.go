package identity

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/funster-a/dd/internal/platform/auth"
)

// killRedis подменяет хранилище сессий неотвечающим адресом: соединение не
// устанавливается, а висит — как у упавшего узла.
func (e *env) killRedis(t *testing.T) {
	t.Helper()
	dead := goredis.NewClient(&goredis.Options{Addr: "10.255.255.1:6379"})
	t.Cleanup(func() { _ = dead.Close() })
	e.svc.sessions = sessions{rdb: dead}
}

func (e *env) session(t *testing.T) string {
	t.Helper()
	token, err := e.svc.IssueBuyerSession(t.Context(), randomPhone())
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// Пока Redis отвечает, кэш не мешает отзыву: сессия, удалённая в Redis
// (например, выход на другом экземпляре api), сразу перестаёт действовать.
func TestRevokedSessionFailsImmediatelyWhenRedisIsUp(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	token := e.session(t)
	if _, err := e.svc.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
	other := NewService(e.db.Pool, e.svc.sessions.rdb, nil, nil, nil)
	if err := other.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("revoked session: err = %v, want ErrNoSession", err)
	}
	if e.svc.cache.len() != 0 {
		t.Error("revoked session stays in cache")
	}
}

// Redis упал: недавно проверенная сессия проходит, незнакомая — быстрый
// ErrUnavailable, ожидание таймаута — один раз, дальше сразу.
func TestSessionsSurviveRedisOutage(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	known := e.session(t)
	unknown := e.session(t)
	want, err := e.svc.Authenticate(ctx, known)
	if err != nil {
		t.Fatal(err)
	}
	e.killRedis(t)

	start := time.Now()
	got, err := e.svc.Authenticate(ctx, known)
	if err != nil || got.Principal != want.Principal {
		t.Fatalf("known session during outage = %+v, %v", got, err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("first check during outage took %v", d)
	}
	start = time.Now()
	if _, err := e.svc.Authenticate(ctx, unknown); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unknown session during outage: err = %v, want ErrUnavailable", err)
	}
	if d := time.Since(start); d > 20*time.Millisecond {
		t.Fatalf("check with the gate open took %v, want immediate", d)
	}
	if _, err := e.svc.Authenticate(ctx, known); err != nil {
		t.Fatalf("known session with the gate open: %v", err)
	}
}

// Проверка старше sessionStaleMaxAge без Redis не принимается, как и
// истёкшая сессия.
func TestStaleCacheLimits(t *testing.T) {
	c := newSessionCache(time.Minute, 10)
	now := time.Now()
	s := Session{Principal: auth.Principal{Kind: auth.KindBuyer, SubjectID: "b"}, ExpiresAt: now.Add(time.Hour)}
	c.put("k", s, now)
	if _, ok := c.get("k", now.Add(59*time.Second)); !ok {
		t.Fatal("fresh entry rejected")
	}
	if _, ok := c.get("k", now.Add(61*time.Second)); ok {
		t.Fatal("entry older than maxAge accepted")
	}
	c.put("e", Session{ExpiresAt: now.Add(time.Second)}, now)
	if _, ok := c.get("e", now.Add(2*time.Second)); ok {
		t.Fatal("expired session accepted")
	}
	for i := range 25 {
		c.put(string(rune('a'+i)), s, now)
	}
	if c.len() > 10 {
		t.Fatalf("cache holds %d entries, limit 10", c.len())
	}
}

// Выход забывает сессию в кэше процесса: даже при отказе Redis она не
// пройдёт по старой проверке.
func TestLogoutForgetsCachedSession(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	token := e.session(t)
	if _, err := e.svc.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	e.killRedis(t)
	if _, err := e.svc.Authenticate(ctx, token); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("logged-out session during outage: err = %v, want ErrUnavailable", err)
	}
}

// HTTP: без Redis и без кэша — 503 с Retry-After, а не 500.
func TestMiddlewareReturns503WhenStoreIsDown(t *testing.T) {
	e := newEnv(t)
	token := e.session(t)
	e.killRedis(t)
	h := e.svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("code = %d, Retry-After = %q; want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
}
