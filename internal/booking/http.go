package booking

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/funster-a/dd/internal/platform/auth"
	"github.com/funster-a/dd/internal/platform/httpx"
	"github.com/funster-a/dd/internal/platform/idempotency"
)

// Register добавляет маршруты бронирования в роутер /v1:
//
//	GET  /events/{eventID}/availability — занятость мест, без входа
//	POST /events/{eventID}/orders       — заказ покупателя
//	GET  /orders/{orderID}              — заказ покупателя
//	POST /orders/{orderID}/cancel       — отмена неоплаченного заказа
func (s *Service) Register(r chi.Router) {
	r.Get("/events/{eventID}/availability", s.handleAvailability)
	r.Group(func(r chi.Router) {
		r.Use(auth.Require(auth.KindBuyer))
		idem := idempotency.Middleware(s.pool)
		r.With(idem).Post("/events/{eventID}/orders", s.handleCreateOrder)
		r.Get("/orders/{orderID}", s.handleGetOrder)
		r.With(idem).Post("/orders/{orderID}/cancel", s.handleCancelOrder)
	})
}

func (s *Service) handleAvailability(w http.ResponseWriter, r *http.Request) {
	b, err := s.GetAvailability(r.Context(), chi.URLParam(r, "eventID"))
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
	o, err := s.CreateOrder(r.Context(), buyerID(r), chi.URLParam(r, "eventID"), req, time.Now())
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, o)
}

func (s *Service) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	o, err := s.GetOrder(r.Context(), buyerID(r), chi.URLParam(r, "orderID"), time.Now())
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, o)
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
