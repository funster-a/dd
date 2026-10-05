// Package idempotency делает изменяющие эндпоинты идемпотентными по ключу
// запроса (CLAUDE.md, правило 3): клиент передаёт заголовок Idempotency-Key,
// первый запрос выполняется, повтор с тем же ключом получает сохранённый
// ответ, а не создаёт вторую сущность. Ключи хранятся в PostgreSQL (ADR 005).
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/funster-a/dd/internal/platform/auth"
	pgdb "github.com/funster-a/dd/internal/platform/db"
	"github.com/funster-a/dd/internal/platform/httpx"
	"github.com/funster-a/dd/internal/platform/idempotency/idempotencydb"
)

const (
	// Header — заголовок с ключом идемпотентности.
	Header = "Idempotency-Key"
	// ReplayedHeader выставляется в ответе, отданном из сохранённого.
	ReplayedHeader = "Idempotent-Replayed"

	maxKeyLen    = 255
	maxBodyBytes = 1 << 20
	// staleAfter — через сколько незавершённый ключ считается брошенным
	// (процесс упал посреди запроса) и может быть занят заново.
	staleAfter = time.Minute
)

// Снятие ключа после сбоя повторяется releaseEvery, пока не пройдёт
// releaseFor (ADR 028). Переменные — чтобы тест не ждал секундами.
var (
	releaseEvery = 500 * time.Millisecond
	releaseFor   = 30 * time.Second
)

// Middleware возвращает middleware идемпотентности.
//
// Ключ действует в области «кто вызывает + метод + маршрут»: одинаковые
// ключи разных пользователей или разных эндпоинтов не пересекаются.
func Middleware(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	q := idempotencydb.New(pool)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(Header)
			if key == "" || len(key) > maxKeyLen {
				httpx.WriteError(w, http.StatusBadRequest, "idempotency_key_required",
					"header "+Header+" with 1-255 characters is required")
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
			if err != nil {
				httpx.WriteError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			scope := scopeOf(r)
			hash := requestHash(r, body)
			log := httpx.Logger(r.Context()).With(slog.String("idempotency_scope", scope))

			// Тот же ключ уже выполняется в этом экземпляре: повтор пришёл
			// раньше, чем закончился первый запрос.
			if !inflight.enter(scope, key) {
				httpx.WriteError(w, http.StatusConflict, "request_in_progress", "request with this key is being processed, retry")
				return
			}
			defer inflight.leave(scope, key)

			claimed, err := claim(r, q, scope, key, hash)
			if err != nil {
				if pgdb.Unavailable(err) {
					// Ключ мог заняться, а подтверждение — потеряться вместе с
					// базой; тогда он остался бы «выполняется» за нами.
					releaseLater(context.WithoutCancel(r.Context()), q, scope, key, log)
					httpx.WriteUnavailable(w, r, err)
					return
				}
				log.Error("claim idempotency key", slog.Any("error", err))
				httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
				return
			}
			if !claimed {
				replay(w, r, q, scope, key, hash, log)
				return
			}

			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r.WithContext(WithKey(r.Context(), key)))

			// Контекст запроса мог закончиться; сохранить результат нужно всё равно.
			ctx := context.WithoutCancel(r.Context())
			if rec.status >= http.StatusInternalServerError {
				// Сбой на нашей стороне не фиксируем: клиент должен иметь возможность повторить.
				release(ctx, q, scope, key, log)
				return
			}
			if rec.truncated {
				// Слишком большой ответ не сохранить целиком: снимаем ключ, как при сбое.
				log.Warn("response too large to store for idempotency", slog.Int("limit", maxBodyBytes))
				release(ctx, q, scope, key, log)
				return
			}
			if err := q.Complete(ctx, idempotencydb.CompleteParams{
				Scope: scope, Key: key,
				ResponseStatus:      pgtype.Int4{Int32: int32(rec.status), Valid: true}, //nolint:gosec // HTTP-код помещается в int32
				ResponseBody:        rec.body.Bytes(),
				ResponseContentType: nonEmpty(rec.Header().Get("Content-Type")),
			}); err != nil {
				log.Error("complete idempotency key", slog.Any("error", err))
			}
		})
	}
}

type keyCtx struct{}

// KeyFrom — ключ идемпотентности запроса, который сейчас выполняется. Модуль
// пишет его в создаваемую сущность в той же транзакции, если повтор после
// неизвестного исхода фиксации нельзя распознать иначе (ADR 028).
func KeyFrom(ctx context.Context) string {
	k, _ := ctx.Value(keyCtx{}).(string)
	return k
}

// WithKey кладёт ключ в контекст; middleware делает это сам, функция нужна
// тестам модулей.
func WithKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, keyCtx{}, key)
}

// release снимает незавершённый ключ после сбоя. Частый сбой — сама база
// недоступна (переключение на реплику, ADR 028), и снять ключ сразу тоже не
// выходит; тогда снятие повторяется в фоне. Иначе ключ живого экземпляра
// висел бы «выполняется» до staleAfter, и повтор клиента минуту получал бы
// 409.
func release(ctx context.Context, q *idempotencydb.Queries, scope, key string, log *slog.Logger) {
	err := q.Release(ctx, idempotencydb.ReleaseParams{Scope: scope, Key: key})
	if err == nil {
		return
	}
	log.Warn("release idempotency key, will retry", slog.Any("error", err))
	releaseLater(ctx, q, scope, key, log)
}

// releaseLater снимает ключ этого экземпляра в фоне, каждые releaseEvery до
// releaseFor, пока база не ответит. Ключ, занятый тем временем другим
// экземпляром или снова выполняемый здесь, не трогается: снимается только
// свой ключ, который этот экземпляр сейчас не обрабатывает.
func releaseLater(ctx context.Context, q *idempotencydb.Queries, scope, key string, log *slog.Logger) {
	me := currentOwner()
	go func() {
		var err error
		for deadline := time.Now().Add(releaseFor); time.Now().Before(deadline); {
			time.Sleep(releaseEvery)
			if !inflight.enter(scope, key) {
				return // повтор клиента уже выполняется здесь и сам решит судьбу ключа
			}
			rctx, cancel := context.WithTimeout(ctx, releaseEvery)
			if me == nil {
				err = q.Release(rctx, idempotencydb.ReleaseParams{Scope: scope, Key: key})
			} else {
				err = q.ReleaseOwn(rctx, idempotencydb.ReleaseOwnParams{Scope: scope, Key: key, Owner: me})
			}
			cancel()
			inflight.leave(scope, key)
			if err == nil {
				return
			}
		}
		log.Error("release idempotency key", slog.Any("error", err))
	}()
}

// inflight — ключи, которые этот экземпляр сейчас обрабатывает. Незавершённый
// ключ с владельцем «этот экземпляр», которого здесь нет, брошен: занятие
// ключа зафиксировалось, а подтверждение потерялось, или ключ не удалось
// снять после сбоя (ADR 028).
var inflight = keySet{m: map[string]struct{}{}}

type keySet struct {
	mu sync.Mutex
	m  map[string]struct{}
}

func (s *keySet) enter(scope, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := scope + "\x00" + key
	if _, busy := s.m[k]; busy {
		return false
	}
	s.m[k] = struct{}{}
	return true
}

func (s *keySet) leave(scope, key string) {
	s.mu.Lock()
	delete(s.m, scope+"\x00"+key)
	s.mu.Unlock()
}

// claim занимает ключ; брошенный незавершённый ключ — занятый давно или
// экземпляром, который перестал отмечаться (ADR 027), — занимается заново.
func claim(r *http.Request, q *idempotencydb.Queries, scope, key string, hash []byte) (bool, error) {
	ctx := r.Context()
	me := currentOwner()
	n, err := q.Claim(ctx, idempotencydb.ClaimParams{Scope: scope, Key: key, RequestHash: hash, Owner: me})
	if err != nil || n == 1 {
		return n == 1, err
	}
	released, err := q.ReleaseStale(ctx, idempotencydb.ReleaseStaleParams{
		Scope: scope, Key: key,
		StaleBefore:   time.Now().Add(-staleAfter),
		Self:          me, // свой ключ, которого нет среди выполняемых, брошен
		DeadAfterSecs: deadAfter.Seconds(),
	})
	if err != nil || released == 0 {
		return false, err
	}
	n, err = q.Claim(ctx, idempotencydb.ClaimParams{Scope: scope, Key: key, RequestHash: hash, Owner: me})
	return n == 1, err
}

// replay отвечает на повтор: сохранённым ответом, 409 пока первый запрос
// выполняется или 422, если ключ использован для другого запроса.
func replay(w http.ResponseWriter, r *http.Request, q *idempotencydb.Queries,
	scope, key string, hash []byte, log *slog.Logger,
) {
	row, err := q.Get(r.Context(), idempotencydb.GetParams{Scope: scope, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		// Первый запрос упал с 5xx и снял ключ между нашими запросами.
		httpx.WriteError(w, http.StatusConflict, "request_in_progress", "request with this key is being processed, retry")
		return
	}
	if err != nil {
		if pgdb.Unavailable(err) {
			httpx.WriteUnavailable(w, r, err)
			return
		}
		log.Error("load idempotency key", slog.Any("error", err))
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if !bytes.Equal(row.RequestHash, hash) {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "idempotency_key_reused",
			"this key was already used for a different request")
		return
	}
	if row.CompletedAt == nil {
		httpx.WriteError(w, http.StatusConflict, "request_in_progress", "request with this key is being processed, retry")
		return
	}
	w.Header().Set(ReplayedHeader, "true")
	if row.ResponseContentType != nil {
		w.Header().Set("Content-Type", *row.ResponseContentType)
	}
	w.WriteHeader(int(row.ResponseStatus.Int32))
	_, _ = w.Write(row.ResponseBody)
}

func scopeOf(r *http.Request) string {
	who := "anonymous"
	if p, ok := auth.PrincipalFrom(r.Context()); ok {
		who = string(p.Kind) + ":" + p.SubjectID
	}
	route := r.URL.Path
	if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
		route = rc.RoutePattern()
	}
	return who + " " + r.Method + " " + route
}

// requestHash отличает «тот же запрос» от «другого запроса с тем же ключом».
func requestHash(r *http.Request, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(r.Method + " " + r.URL.RequestURI() + "\n"))
	h.Write([]byte(strconv.Itoa(len(body)) + "\n"))
	h.Write(body)
	return h.Sum(nil)
}

// recorder пропускает ответ клиенту и одновременно запоминает его.
type recorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        bytes.Buffer
	truncated   bool
}

func (r *recorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if r.body.Len()+len(b) <= maxBodyBytes {
		r.body.Write(b)
	} else {
		r.truncated = true
	}
	return r.ResponseWriter.Write(b)
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
