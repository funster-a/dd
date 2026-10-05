package idempotency_test

import (
	"crypto/sha256"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/funster-a/dd/internal/platform/auth"
	"github.com/funster-a/dd/internal/platform/db/dbtest"
	"github.com/funster-a/dd/internal/platform/idempotency"
)

type harness struct {
	srv   *httptest.Server
	db    *dbtest.DB
	calls atomic.Int64
	fail  atomic.Bool
	gate  chan struct{} // если не nil, обработчик ждёт его закрытия
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := dbtest.New(t)
	h := &harness{db: db}
	r := chi.NewRouter()
	// Участник из заголовка — чтобы проверить разделение ключей по пользователям.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if who := r.Header.Get("X-Test-User"); who != "" {
				r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindAdmin, SubjectID: who}))
			}
			next.ServeHTTP(w, r)
		})
	})
	r.With(idempotency.Middleware(db.Pool)).Post("/things", func(w http.ResponseWriter, r *http.Request) {
		n := h.calls.Add(1)
		if h.gate != nil {
			<-h.gate
		}
		if h.fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"n":`+strconv.FormatInt(n, 10)+`,"echo":`+string(body)+`}`)
	})
	h.srv = httptest.NewServer(r)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) post(t *testing.T, key, user, body string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.srv.URL+"/things", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set(idempotency.Header, key)
	}
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data), resp.Header
}

func TestReplayReturnsStoredResponse(t *testing.T) {
	h := newHarness(t)

	code1, body1, _ := h.post(t, "k1", "alice", `{"a":1}`)
	code2, body2, hdr := h.post(t, "k1", "alice", `{"a":1}`)

	if code1 != http.StatusCreated || code2 != http.StatusCreated {
		t.Fatalf("codes = %d, %d; want 201, 201", code1, code2)
	}
	if body1 != body2 {
		t.Errorf("replayed body = %s, want %s", body2, body1)
	}
	if hdr.Get(idempotency.ReplayedHeader) != "true" {
		t.Errorf("replay header missing")
	}
	if n := h.calls.Load(); n != 1 {
		t.Errorf("handler ran %d times, want 1", n)
	}
}

func TestKeyReuseWithDifferentBodyIsRejected(t *testing.T) {
	h := newHarness(t)
	h.post(t, "k1", "alice", `{"a":1}`)
	if code, _, _ := h.post(t, "k1", "alice", `{"a":2}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422", code)
	}
}

func TestKeyIsRequired(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.post(t, "", "alice", `{}`); code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", code)
	}
	if n := h.calls.Load(); n != 0 {
		t.Errorf("handler ran %d times, want 0", n)
	}
}

func TestKeysAreScopedPerUser(t *testing.T) {
	h := newHarness(t)
	h.post(t, "same", "alice", `{}`)
	h.post(t, "same", "bob", `{}`)
	if n := h.calls.Load(); n != 2 {
		t.Fatalf("handler ran %d times, want 2 (keys of different users are independent)", n)
	}
}

func TestServerErrorIsNotStored(t *testing.T) {
	h := newHarness(t)
	h.fail.Store(true)
	if code, _, _ := h.post(t, "k1", "alice", `{}`); code != http.StatusInternalServerError {
		t.Fatalf("first code = %d, want 500", code)
	}
	h.fail.Store(false)
	if code, _, _ := h.post(t, "k1", "alice", `{}`); code != http.StatusCreated {
		t.Fatalf("retry code = %d, want 201", code)
	}
	if n := h.calls.Load(); n != 2 {
		t.Errorf("handler ran %d times, want 2", n)
	}
}

// TestConcurrentRequestsExecuteOnce: одновременные запросы с одним ключом —
// обработчик выполняется ровно один раз, остальные получают 409 или повтор.
func TestConcurrentRequestsExecuteOnce(t *testing.T) {
	h := newHarness(t)
	h.gate = make(chan struct{})

	const n = 20
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		codes = map[int]int{}
	)
	start := make(chan struct{})
	for range n {
		wg.Go(func() {
			<-start
			code, _, _ := h.post(t, "race", "alice", `{}`)
			mu.Lock()
			codes[code]++
			mu.Unlock()
		})
	}
	close(start)
	// Держим первый запрос в обработчике, пока остальные гарантированно не придут.
	for h.calls.Load() == 0 {
		runtime.Gosched()
	}
	close(h.gate)
	wg.Wait()

	if got := h.calls.Load(); got != 1 {
		t.Fatalf("handler ran %d times, want exactly 1", got)
	}
	if codes[http.StatusCreated] < 1 || codes[http.StatusCreated]+codes[http.StatusConflict] != n {
		t.Fatalf("status codes = %v, want only 201 and 409", codes)
	}
}

// Экземпляр упал посреди запроса (ADR 027): его ключ остался незавершённым.
// Повтор с тем же ключом выполняется сразу, если владелец перестал
// отмечаться, и получает 409, если владелец жив и ещё работает.
func TestAbandonedKeyOfDeadOwner(t *testing.T) {
	h := newHarness(t)
	body := `{"x":1}`
	sum := sha256.Sum256([]byte("POST /things\n" + strconv.Itoa(len(body)) + "\n" + body))
	abandon := func(key, owner, seenAgo string) {
		t.Helper()
		if _, err := h.db.Pool.Exec(t.Context(),
			`INSERT INTO api_instances (id, seen_at) VALUES ($1, now() - $2::interval)`, owner, seenAgo); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.Pool.Exec(t.Context(),
			`INSERT INTO idempotency_keys (scope, key, request_hash, owner) VALUES ('anonymous POST /things', $1, $2, $3)`,
			key, sum[:], owner); err != nil {
			t.Fatal(err)
		}
	}

	abandon("k-dead", "api-dead", "10 seconds")
	if code, _, _ := h.post(t, "k-dead", "", body); code != http.StatusCreated || h.calls.Load() != 1 {
		t.Fatalf("dead owner: status %d, calls %d; want 201 and one call", code, h.calls.Load())
	}

	abandon("k-alive", "api-alive", "0 seconds")
	if code, _, _ := h.post(t, "k-alive", "", body); code != http.StatusConflict || h.calls.Load() != 1 {
		t.Fatalf("alive owner: status %d, calls %d; want 409 and no new call", code, h.calls.Load())
	}
}

func TestHeartbeatRegistersAndLeaves(t *testing.T) {
	db := dbtest.New(t)
	count := func() int {
		t.Helper()
		var n int
		if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM api_instances WHERE seen_at > now() - interval '5 seconds'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	stop, err := idempotency.Heartbeat(t.Context(), db.Pool.Config().ConnConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatal("instance is not registered")
	}
	// Корректная остановка снимает отметку: ключи экземпляра сразу свободны.
	stop()
	if count() != 0 {
		t.Fatal("instance is still registered after stop")
	}
}

// Свой брошенный ключ (ADR 028): занятие ключа зафиксировалось, а
// подтверждение потерялось вместе с базой, и запрос получил 503. Владелец —
// этот же живой экземпляр, но ключ он не обрабатывает, поэтому повтор
// выполняется сразу, а не ждёт минуту.
func TestAbandonedOwnKey(t *testing.T) {
	h := newHarness(t)
	stop, err := idempotency.Heartbeat(t.Context(), h.db.Pool.Config().ConnConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	var me string
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT id FROM api_instances`).Scan(&me); err != nil {
		t.Fatal(err)
	}
	body := `{"x":1}`
	sum := sha256.Sum256([]byte("POST /things\n" + strconv.Itoa(len(body)) + "\n" + body))
	if _, err := h.db.Pool.Exec(t.Context(),
		`INSERT INTO idempotency_keys (scope, key, request_hash, owner) VALUES ('anonymous POST /things', 'k-own', $1, $2)`,
		sum[:], me); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := h.post(t, "k-own", "", body); code != http.StatusCreated || h.calls.Load() != 1 {
		t.Fatalf("own abandoned key: status %d, calls %d; want 201 and one call", code, h.calls.Load())
	}
}
