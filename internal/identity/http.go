package identity

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/funster-a/dd/internal/platform/auth"
	pgdb "github.com/funster-a/dd/internal/platform/db"
	"github.com/funster-a/dd/internal/platform/httpx"
)

// Routes — эндпоинты входа, монтируются под /v1/auth.
//
// Эти эндпоинты не используют ключи идемпотентности (правило 3): ответ на
// вход содержит токен сессии, и хранить его в таблице идемпотентности нельзя.
// Повтор безопасен и так — код одноразовый, повторный вход с тем же кодом
// получает 401, а не вторую сессию.
func (s *Service) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/codes", s.handleRequestCode)
	r.Post("/sessions", s.handleLogin)
	r.With(auth.Require(auth.KindBuyer, auth.KindOrganizer, auth.KindAdmin)).
		Delete("/session", s.handleLogout)
	return r
}

// Middleware читает заголовок Authorization: Bearer <token> и кладёт участника
// в контекст. Без заголовка запрос идёт дальше анонимным; с неверным или
// отозванным токеном — 401.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		sess, err := s.Authenticate(r.Context(), token)
		if errors.Is(err, ErrNoSession) {
			httpx.WriteError(w, http.StatusUnauthorized, "invalid_session", "session is invalid or expired")
			return
		}
		if errors.Is(err, ErrUnavailable) {
			// С репликой и Sentinel Redis недоступен 2–3 секунды (ADR 029):
			// пауза больше заставила бы покупателя ждать уже работающий Redis.
			w.Header().Set("Retry-After", "2")
			httpx.WriteError(w, http.StatusServiceUnavailable, "session_store_unavailable", "sign-in is temporarily unavailable, retry shortly")
			return
		}
		if err != nil {
			if pgdb.Unavailable(err) {
				httpx.WriteUnavailable(w, r, err)
				return
			}
			httpx.Logger(r.Context()).Error("authenticate", slog.Any("error", err))
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), sess.Principal)))
	})
}

// HandleMe отдаёт текущего участника; монтируется как GET /v1/me.
func HandleMe(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	httpx.WriteJSON(w, http.StatusOK, p)
}

type codeRequest struct {
	Kind  auth.Kind `json:"kind"`
	Phone string    `json:"phone,omitempty"`
	Email string    `json:"email,omitempty"`
}

func (c codeRequest) address() string {
	if c.Kind == auth.KindBuyer {
		return c.Phone
	}
	return c.Email
}

type loginRequest struct {
	codeRequest
	Code string `json:"code"`
}

type loginResponse struct {
	Token     string         `json:"token"`
	Principal auth.Principal `json:"principal"`
	ExpiresAt time.Time      `json:"expires_at"`
}

func (s *Service) handleRequestCode(w http.ResponseWriter, r *http.Request) {
	var req codeRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	err := s.RequestCode(r.Context(), req.Kind, req.address(), clientIP(r))
	if s.writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
		"status":               "sent",
		"resend_after_seconds": int(resendInterval.Seconds()),
	})
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	token, sess, err := s.Login(r.Context(), req.Kind, req.address(), req.Code)
	if s.writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, loginResponse{Token: token, Principal: sess.Principal, ExpiresAt: sess.ExpiresAt})
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	token, _ := bearerToken(r)
	if s.writeError(w, r, s.Logout(r.Context(), token)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeError переводит ошибку сервиса в HTTP-ответ; возвращает true, если ответ записан.
func (s *Service) writeError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrInvalidAddress):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_address",
			"kind must be buyer (phone in E.164) or organizer/admin (email)")
	case errors.Is(err, ErrTooManyRequests):
		w.Header().Set("Retry-After", "60")
		httpx.WriteError(w, http.StatusTooManyRequests, "too_many_requests", "too many code requests, try later")
	case errors.Is(err, ErrInvalidCode):
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_code", "code is invalid or expired")
	default:
		if pgdb.Unavailable(err) {
			httpx.WriteUnavailable(w, r, err)
			return true
		}
		httpx.Logger(r.Context()).Error("identity request failed", slog.Any("error", err))
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	}
	return true
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || token == "" {
		return "", false
	}
	return token, true
}

// clientIP — адрес клиента для лимитов. За nginx его подставляет
// httpx.RealIP по доверенному X-Forwarded-For (ADR 020).
func clientIP(r *http.Request) string { return httpx.ClientIP(r) }
