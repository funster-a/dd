package catalog

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/funster-a/dd/internal/platform/auth"
	"github.com/funster-a/dd/internal/platform/httpx"
	"github.com/funster-a/dd/internal/platform/idempotency"
)

// AdminRoutes — эндпоинты админки платформы, монтируются под /v1/admin.
func (s *Service) AdminRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.Require(auth.KindAdmin))
	r.Get("/organizers", s.handleListOrganizers)
	r.With(idempotency.Middleware(s.pool)).Post("/organizers", s.handleCreateOrganizer)
	return r
}

func (s *Service) handleCreateOrganizer(w http.ResponseWriter, r *http.Request) {
	var in NewOrganizer
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	org, err := s.CreateOrganizer(r.Context(), in)
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, org)
}

func (s *Service) handleListOrganizers(w http.ResponseWriter, r *http.Request) {
	orgs, err := s.ListOrganizers(r.Context())
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"organizers": orgs})
}

// writeError переводит ошибку сервиса в HTTP-ответ; true — ответ записан.
func writeError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if v, ok := errors.AsType[*ValidationError](err); ok {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_"+v.Field, v.Error())
		return true
	}
	if c, ok := errors.AsType[*ConflictError](err); ok {
		httpx.WriteError(w, http.StatusConflict, c.Code, c.Message)
		return true
	}
	httpx.Logger(r.Context()).Error("catalog request failed", slog.Any("error", err))
	httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	return true
}
