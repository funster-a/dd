package ticket

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"rsc.io/qr"

	"github.com/funster-a/dd/internal/platform/auth"
	"github.com/funster-a/dd/internal/platform/httpx"
	"github.com/funster-a/dd/internal/platform/idempotency"
)

// Register добавляет маршруты билетов в роутер /v1:
//
//	GET /orders/{orderID}/tickets — билеты заказа покупателя
//	GET /tickets/{token}          — билет по ссылке, без входа
//	GET /tickets/{token}/qr.png   — QR-код билета
func (s *Service) Register(r chi.Router) {
	r.With(auth.Require(auth.KindBuyer)).Get("/orders/{orderID}/tickets", s.handleOrderTickets)
	r.Get("/tickets/{token}", s.handleTicket)
	r.Get("/tickets/{token}/qr.png", s.handleQR)
}

func (s *Service) handleOrderTickets(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	ts, err := s.ForOrder(r.Context(), p.SubjectID, chi.URLParam(r, "orderID"))
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ts)
}

func (s *Service) handleTicket(w http.ResponseWriter, r *http.Request) {
	v, err := s.ByToken(r.Context(), chi.URLParam(r, "token"))
	if writeError(w, r, err) {
		return
	}
	w.Header().Set("Cache-Control", "no-store") // статус меняется при проходе
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (s *Service) handleQR(w http.ResponseWriter, r *http.Request) {
	v, err := s.ByToken(r.Context(), chi.URLParam(r, "token"))
	if writeError(w, r, err) {
		return
	}
	code, err := qr.Encode(v.URL, qr.M)
	if writeError(w, r, err) {
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=86400") // ссылка на билет не меняется
	_, _ = w.Write(code.PNG())
}

// HandlePage — страница билета /t/{token}: её открывает ссылка из письма
// и камера телефона при сканировании QR.
func (s *Service) HandlePage(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	v, err := s.ByToken(r.Context(), token)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "Билет не найден", http.StatusNotFound)
		return
	}
	if writeError(w, r, err) {
		return
	}
	loc, lerr := time.LoadLocation(v.Timezone)
	if lerr != nil {
		loc = time.UTC
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := pageTmpl.Execute(w, pageData{View: v, Token: token, Starts: v.StartsAt.In(loc).Format("02.01.2006 15:04")}); err != nil {
		httpx.Logger(r.Context()).Warn("render ticket page", slog.Any("error", err))
	}
}

func writeError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "not found")
		return true
	}
	if v, ok := errors.AsType[*ValidationError](err); ok {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_"+v.Field, v.Error())
		return true
	}
	httpx.Logger(r.Context()).Error("ticket request failed", slog.Any("error", err))
	httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	return true
}

type pageData struct {
	View   View
	Token  string
	Starts string
}

var pageTmpl = template.Must(template.New("ticket").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.View.Event}} — билет</title>
<style>body{font-family:system-ui,sans-serif;background:#f4f4f5;margin:0;padding:16px;color:#18181b}
.card{max-width:420px;margin:24px auto;background:#fff;border-radius:12px;padding:24px;box-shadow:0 1px 3px rgba(0,0,0,.1);text-align:center}
.muted{color:#71717a;font-size:14px}.seat{font-size:20px;font-weight:600;margin:12px 0}
img{width:240px;height:240px;image-rendering:pixelated}.used{color:#b91c1c;font-weight:600}</style></head>
<body><div class="card">
<h2>{{.View.Event}}</h2>
<div class="muted">{{.Starts}} · {{.View.Venue}}{{if .View.Address}}, {{.View.Address}}{{end}} · {{.View.AgeRating}}</div>
<div class="seat">{{.View.Section}}{{if .View.Row}}, ряд {{.View.Row}}, место {{.View.Seat}}{{end}}</div>
{{if eq .View.Status "issued"}}<img src="/v1/tickets/{{.Token}}/qr.png" alt="QR-код билета">
<p class="muted">Покажите QR-код на входе</p>
{{else if eq .View.Status "used"}}<p class="used">Билет уже использован</p>
{{else}}<p class="used">Билет аннулирован</p>{{end}}
</div></body></html>
`))

// RegisterOrganizer добавляет в кабинет организатора (/v1/organizer, вход
// уже проверен) управление ссылками сканера:
//
//	POST   /events/{eventID}/scanners — создать ссылку (токен показывается один раз)
//	GET    /events/{eventID}/scanners — ссылки события
//	DELETE /scanners/{scannerID}      — отозвать
func (s *Service) RegisterOrganizer(r chi.Router) {
	r.With(idempotency.Middleware(s.pool)).Post("/events/{eventID}/scanners", s.handleCreateScanner)
	r.Get("/events/{eventID}/scanners", s.handleListScanners)
	r.Delete("/scanners/{scannerID}", s.handleRevokeScanner)
}

func organizerID(r *http.Request) string {
	p, _ := auth.PrincipalFrom(r.Context())
	return p.OrganizerID
}

func (s *Service) handleCreateScanner(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	sc, err := s.CreateScanner(r.Context(), organizerID(r), chi.URLParam(r, "eventID"), in.Name)
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, sc)
}

func (s *Service) handleListScanners(w http.ResponseWriter, r *http.Request) {
	list, err := s.ListScanners(r.Context(), organizerID(r), chi.URLParam(r, "eventID"))
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

func (s *Service) handleRevokeScanner(w http.ResponseWriter, r *http.Request) {
	if writeError(w, r, s.RevokeScanner(r.Context(), organizerID(r), chi.URLParam(r, "scannerID"))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ScannerRoutes — API сканера контролёра, монтируется под /v1/scanner.
// Доступ по токену ссылки: заголовок Authorization: Scanner <токен>.
//
//	GET  /manifest — событие и все его билеты для работы без сети
//	POST /scans    — сканирования: одно онлайн или пачка после офлайна
func (s *Service) ScannerRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(s.scannerAuth)
	r.Get("/manifest", s.handleManifest)
	r.Post("/scans", s.handleScans)
	return r
}

type scannerCtxKey struct{}

func (s *Service) scannerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Scanner ")
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "scanner_required", "send Authorization: Scanner <token>")
			return
		}
		sess, err := s.ScannerByToken(r.Context(), strings.TrimSpace(token))
		if errors.Is(err, ErrScannerRevoked) {
			httpx.WriteError(w, http.StatusUnauthorized, "scanner_revoked", err.Error())
			return
		}
		if writeError(w, r, err) {
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), scannerCtxKey{}, sess)))
	})
}

func scannerFrom(r *http.Request) ScannerSession {
	sess, _ := r.Context().Value(scannerCtxKey{}).(ScannerSession)
	return sess
}

func (s *Service) handleManifest(w http.ResponseWriter, r *http.Request) {
	m, err := s.Manifest(r.Context(), scannerFrom(r), time.Now())
	if writeError(w, r, err) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, m)
}

func (s *Service) handleScans(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceID string      `json:"device_id"`
		Scans    []ScanInput `json:"scans"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	res, err := s.Scan(r.Context(), scannerFrom(r), in.DeviceID, in.Scans, time.Now())
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"results": res})
}
