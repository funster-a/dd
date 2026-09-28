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

// OrganizerRoutes — кабинет организатора, монтируется под /v1/organizer.
// Организатор берётся только из сессии (ADR 006): идентификаторы в URL
// ищутся в пределах его данных, чужие записи отвечают 404.
func (s *Service) OrganizerRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.Require(auth.KindOrganizer))
	idem := idempotency.Middleware(s.pool)

	r.Get("/venues", s.handleListVenues)
	r.With(idem).Post("/venues", s.handleCreateVenue)
	r.Get("/venues/{venueID}", s.handleGetVenue)
	r.With(idem).Put("/venues/{venueID}", s.handleUpdateVenue)

	r.Get("/venues/{venueID}/seat-maps", s.handleListSeatMaps)
	r.With(idem).Post("/venues/{venueID}/seat-maps", s.handleCreateSeatMap)
	r.Get("/seat-maps/{seatMapID}", s.handleGetSeatMap)
	return r
}

func organizerID(r *http.Request) string {
	p, _ := auth.PrincipalFrom(r.Context())
	return p.OrganizerID
}

func (s *Service) handleCreateVenue(w http.ResponseWriter, r *http.Request) {
	var in VenueInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	v, err := s.CreateVenue(r.Context(), organizerID(r), in)
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, v)
}

func (s *Service) handleUpdateVenue(w http.ResponseWriter, r *http.Request) {
	var in VenueInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	v, err := s.UpdateVenue(r.Context(), organizerID(r), chi.URLParam(r, "venueID"), in)
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (s *Service) handleGetVenue(w http.ResponseWriter, r *http.Request) {
	v, err := s.GetVenue(r.Context(), organizerID(r), chi.URLParam(r, "venueID"))
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (s *Service) handleListVenues(w http.ResponseWriter, r *http.Request) {
	vs, err := s.ListVenues(r.Context(), organizerID(r))
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"venues": vs})
}

func (s *Service) handleCreateSeatMap(w http.ResponseWriter, r *http.Request) {
	var in SeatMapInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	m, err := s.CreateSeatMap(r.Context(), organizerID(r), chi.URLParam(r, "venueID"), in)
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, m)
}

func (s *Service) handleGetSeatMap(w http.ResponseWriter, r *http.Request) {
	m, err := s.GetSeatMap(r.Context(), organizerID(r), chi.URLParam(r, "seatMapID"))
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m)
}

func (s *Service) handleListSeatMaps(w http.ResponseWriter, r *http.Request) {
	ms, err := s.ListSeatMaps(r.Context(), organizerID(r), chi.URLParam(r, "venueID"))
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"seat_maps": ms})
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
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "not found")
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
