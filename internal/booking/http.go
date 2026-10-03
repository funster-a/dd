package booking

import (
	"errors"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/funster-a/dd/internal/platform/auth"
	"github.com/funster-a/dd/internal/platform/httpx"
	"github.com/funster-a/dd/internal/platform/idempotency"
)

// Register добавляет маршруты бронирования в роутер /v1:
//
//	GET  /events/{eventID}/availability — занятость мест, без входа; ?view=summary — только
//	                                     сводка по секторам, ?section= — места одного сектора (ADR 024)
//	POST /events/{eventID}/orders       — заказ покупателя
//	GET  /orders/{orderID}              — заказ покупателя
//	POST /orders/{orderID}/cancel       — отмена неоплаченного заказа
//	POST /events/{eventID}/queue        — встать в очередь ожидания или узнать своё место
//	GET  /me/orders                     — заказы покупателя
func (s *Service) Register(r chi.Router) {
	r.Get("/events/{eventID}/availability", s.handleAvailability)
	r.Group(func(r chi.Router) {
		r.Use(auth.Require(auth.KindBuyer))
		idem := idempotency.Middleware(s.pool)
		r.With(idem).Post("/events/{eventID}/orders", s.handleCreateOrder)
		r.Get("/orders/{orderID}", s.handleGetOrder)
		r.Get("/me/orders", s.handleListOrders)
		r.With(idem).Post("/orders/{orderID}/cancel", s.handleCancelOrder)
		// Ключ идемпотентности не нужен: повторный вызов место в очереди не меняет.
		r.Post("/events/{eventID}/queue", s.handleJoinQueue)
	})
}

func (s *Service) handleAvailability(w http.ResponseWriter, r *http.Request) {
	q := AvailabilityQuery{Section: r.URL.Query().Get("section"), Summary: r.URL.Query().Get("view") == "summary"}
	if utf8.RuneCountInString(q.Section) > 100 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "section is too long")
		return
	}
	b, err := s.GetAvailability(r.Context(), chi.URLParam(r, "eventID"), q)
	if writeError(w, r, err) {
		return
	}
	// Занятость меняется постоянно; короткий кэш CDN снимает пик старта продаж.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=2")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b) //nolint:gosec // G705: тело — JSON из json.Marshal с типом application/json, не HTML
}

func (s *Service) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	var req OrderRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	req.ClientIP = httpx.ClientIP(r)
	o, err := s.CreateOrder(r.Context(), buyerID(r), chi.URLParam(r, "eventID"), req, time.Now())
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, o)
}

func (s *Service) handleJoinQueue(w http.ResponseWriter, r *http.Request) {
	st, err := s.JoinQueue(r.Context(), buyerID(r), chi.URLParam(r, "eventID"), time.Now())
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Service) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	o, err := s.GetOrder(r.Context(), buyerID(r), chi.URLParam(r, "orderID"), time.Now())
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, o)
}

func (s *Service) handleListOrders(w http.ResponseWriter, r *http.Request) {
	list, err := s.ListOrders(r.Context(), buyerID(r))
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

func (s *Service) handleCancelOrder(w http.ResponseWriter, r *http.Request) {
	o, err := s.CancelOrder(r.Context(), buyerID(r), chi.URLParam(r, "orderID"), time.Now())
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, o)
}

func buyerID(r *http.Request) string {
	p, _ := auth.PrincipalFrom(r.Context())
	return p.SubjectID
}

func writeError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if v, ok := errors.AsType[*ValidationError](err); ok {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_"+v.Field, v.Error())
		return true
	}
	if p, ok := errors.AsType[*PreconditionError](err); ok {
		httpx.WriteError(w, http.StatusUnprocessableEntity, p.Code, p.Message)
		return true
	}
	if c, ok := errors.AsType[*ConflictError](err); ok {
		httpx.WriteError(w, http.StatusConflict, c.Code, c.Message)
		return true
	}
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "not found")
		return true
	}
	httpx.Logger(r.Context()).Error("booking request failed", slog.Any("error", err))
	httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	return true
}
