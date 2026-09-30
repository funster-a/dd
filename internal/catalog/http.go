package catalog

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

// AdminRoutes — эндпоинты админки платформы, монтируются под /v1/admin.
func (s *Service) AdminRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.Require(auth.KindAdmin))
	r.Get("/organizers", s.handleListOrganizers)
	r.With(idempotency.Middleware(s.pool)).Post("/organizers", s.handleCreateOrganizer)
	return r
}

// PublicRoutes — публичные страницы для покупателей, монтируются под
// /v1/public и не требуют входа.
func (s *Service) PublicRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/events/{organizerSlug}/{eventSlug}", s.handlePublicEvent)
	return r
}

func (s *Service) handlePublicEvent(w http.ResponseWriter, r *http.Request) {
	b, err := s.GetPublicEvent(r.Context(), chi.URLParam(r, "organizerSlug"), chi.URLParam(r, "eventSlug"))
	if writeError(w, r, err) {
		return
	}
	// Страница одинакова для всех — её можно держать в CDN и браузере.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b) //nolint:gosec // G705: тело — JSON из json.Marshal с типом application/json, не HTML

}

// OrganizerRoutes — кабинет организатора, монтируется под /v1/organizer.
// Организатор берётся только из сессии (ADR 006): идентификаторы в URL
// ищутся в пределах его данных, чужие записи отвечают 404.
//
// extra добавляет в кабинет маршруты других модулей (например, ссылки
// сканера из ticket): catalog их не знает, только монтирует.
func (s *Service) OrganizerRoutes(extra ...func(chi.Router)) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.Require(auth.KindOrganizer))
	for _, register := range extra {
		register(r)
	}
	idem := idempotency.Middleware(s.pool)

	r.Get("/venues", s.handleListVenues)
	r.With(idem).Post("/venues", s.handleCreateVenue)
	r.Get("/venues/{venueID}", s.handleGetVenue)
	r.With(idem).Put("/venues/{venueID}", s.handleUpdateVenue)

	r.Get("/venues/{venueID}/seat-maps", s.handleListSeatMaps)
	r.With(idem).Post("/venues/{venueID}/seat-maps", s.handleCreateSeatMap)
	r.Get("/seat-maps/{seatMapID}", s.handleGetSeatMap)

	r.Get("/events", s.handleListEvents)
	r.With(idem).Post("/events", s.handleCreateEvent)
	r.Get("/events/{eventID}", s.handleGetEvent)
	r.With(idem).Put("/events/{eventID}", s.handleUpdateEvent)
	r.Get("/events/{eventID}/prices", s.handleGetPrices)
	r.With(idem).Put("/events/{eventID}/prices", s.handleSetPrices)
	r.With(idem).Post("/events/{eventID}/media/uploads", s.handleCreateUpload)
	r.With(idem).Put("/events/{eventID}/media", s.handleSetMedia)
	r.With(idem).Post("/events/{eventID}/publish", s.handlePublish)
	r.With(idem).Post("/events/{eventID}/cancel", s.handleCancel)
	return r
}

func (s *Service) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	var in EventInput
	if !decode(w, r, &in) {
		return
	}
	e, err := s.CreateEvent(r.Context(), organizerID(r), in)
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, e)
}

func (s *Service) handleUpdateEvent(w http.ResponseWriter, r *http.Request) {
	var in EventInput
	if !decode(w, r, &in) {
		return
	}
	e, err := s.UpdateEvent(r.Context(), organizerID(r), chi.URLParam(r, "eventID"), in)
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

func (s *Service) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	e, err := s.GetEvent(r.Context(), organizerID(r), chi.URLParam(r, "eventID"))
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

func (s *Service) handleListEvents(w http.ResponseWriter, r *http.Request) {
	es, err := s.ListEvents(r.Context(), organizerID(r))
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"events": es})
}

func (s *Service) handleSetPrices(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Categories []PriceInput `json:"categories"`
	}
	if !decode(w, r, &in) {
		return
	}
	cats, err := s.SetPrices(r.Context(), organizerID(r), chi.URLParam(r, "eventID"), in.Categories)
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"categories": cats})
}

func (s *Service) handleGetPrices(w http.ResponseWriter, r *http.Request) {
	cats, err := s.GetPrices(r.Context(), organizerID(r), chi.URLParam(r, "eventID"))
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"categories": cats})
}

func (s *Service) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	var in UploadRequest
	if !decode(w, r, &in) {
		return
	}
	u, err := s.CreateUpload(r.Context(), organizerID(r), chi.URLParam(r, "eventID"), in)
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, u)
}

func (s *Service) handleSetMedia(w http.ResponseWriter, r *http.Request) {
	var in MediaInput
	if !decode(w, r, &in) {
		return
	}
	e, err := s.SetMedia(r.Context(), organizerID(r), chi.URLParam(r, "eventID"), in)
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

func (s *Service) handleCancel(w http.ResponseWriter, r *http.Request) {
	e, err := s.CancelEvent(r.Context(), organizerID(r), chi.URLParam(r, "eventID"))
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

func (s *Service) handlePublish(w http.ResponseWriter, r *http.Request) {
	res, err := s.Publish(r.Context(), organizerID(r), chi.URLParam(r, "eventID"), time.Now())
	if writeError(w, r, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// decode читает JSON-тело; при ошибке сам отвечает 400.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return false
	}
	return true
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
	if p, ok := errors.AsType[*PreconditionError](err); ok {
		httpx.WriteError(w, http.StatusUnprocessableEntity, p.Code, p.Message)
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
