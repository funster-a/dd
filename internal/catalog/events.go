package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/funster-a/dd/internal/catalog/catalogdb"
)

// Возрастные ограничения (spec.md).
var ageRatings = []string{"0+", "6+", "12+", "16+", "18+"}

// Значения по умолчанию для события (spec.md, правила возврата).
const (
	defaultMaxTicketsPerBuyer  = 10
	defaultRefundDeadlineHours = 24
	maxTicketsPerBuyerLimit    = 50
	maxRefundDeadlineHours     = 30 * 24
	maxDescriptionLen          = 10_000
)

// EventInput — данные события от организатора.
type EventInput struct {
	VenueID string `json:"venue_id"`
	// Admission — ticketed (билеты по схеме зала) или free_entry (свободный
	// вход без билетов). По умолчанию ticketed.
	Admission           string     `json:"admission"`
	SeatMapID           string     `json:"seat_map_id"`
	Slug                string     `json:"slug"`
	Title               string     `json:"title"`
	Description         string     `json:"description"`
	AgeRating           string     `json:"age_rating"`
	StartsAt            time.Time  `json:"starts_at"`
	EndsAt              time.Time  `json:"ends_at"`
	SalesStartAt        *time.Time `json:"sales_start_at"`
	SalesEndAt          *time.Time `json:"sales_end_at"`
	MaxTicketsPerBuyer  int32      `json:"max_tickets_per_buyer"`
	RefundDeadlineHours *int32     `json:"refund_deadline_hours"`
}

// Event — событие организатора.
type Event struct {
	ID                  string     `json:"id"`
	VenueID             string     `json:"venue_id"`
	Admission           string     `json:"admission"`
	SeatMapID           *string    `json:"seat_map_id"`
	Slug                string     `json:"slug"`
	Title               string     `json:"title"`
	Description         string     `json:"description"`
	AgeRating           string     `json:"age_rating"`
	Status              string     `json:"status"`
	StartsAt            time.Time  `json:"starts_at"`
	EndsAt              time.Time  `json:"ends_at"`
	SalesStartAt        *time.Time `json:"sales_start_at"`
	SalesEndAt          *time.Time `json:"sales_end_at"`
	MaxTicketsPerBuyer  int32      `json:"max_tickets_per_buyer"`
	RefundDeadlineHours int32      `json:"refund_deadline_hours"`
	CoverImageKey       *string    `json:"cover_image_key"`
	CoverVideoKey       *string    `json:"cover_video_key"`
	PublishedAt         *time.Time `json:"published_at"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// PreconditionError — действие невозможно в текущем состоянии события
// (например, публикация без обложки). HTTP 422.
type PreconditionError struct {
	Code    string
	Message string
}

func (e *PreconditionError) Error() string { return e.Message }

// CreateEvent создаёт черновик события.
func (s *Service) CreateEvent(ctx context.Context, organizerID string, in EventInput) (Event, error) {
	in, err := in.normalize()
	if err != nil {
		return Event{}, err
	}
	e, err := s.q.CreateEvent(ctx, catalogdb.CreateEventParams{
		OrganizerID: organizerID, VenueID: in.VenueID, SeatMapID: in.seatMap(), Admission: in.Admission,
		Slug: in.Slug, Title: in.Title, Description: in.Description, AgeRating: in.AgeRating,
		StartsAt: in.StartsAt, EndsAt: in.EndsAt, SalesStartAt: in.SalesStartAt, SalesEndAt: in.SalesEndAt,
		MaxTicketsPerBuyer: in.MaxTicketsPerBuyer, RefundDeadlineHours: *in.RefundDeadlineHours,
	})
	if err != nil {
		return Event{}, eventWriteError(err, "create event")
	}
	return eventFrom(e), nil
}

// UpdateEvent заменяет данные черновика. Опубликованное событие так не меняется:
// места уже сгенерированы по его схеме и ценам.
func (s *Service) UpdateEvent(ctx context.Context, organizerID, id string, in EventInput) (Event, error) {
	in, err := in.normalize()
	if err != nil {
		return Event{}, err
	}
	var out Event
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		cur, err := q.GetEventForUpdate(ctx, catalogdb.GetEventForUpdateParams{OrganizerID: organizerID, ID: id})
		if err != nil {
			return notFound(err, "load event")
		}
		if err := requireDraft(cur.Status); err != nil {
			return err
		}
		e, err := q.UpdateDraftEvent(ctx, catalogdb.UpdateDraftEventParams{
			OrganizerID: organizerID, ID: id, VenueID: in.VenueID, SeatMapID: in.seatMap(), Admission: in.Admission,
			Slug: in.Slug, Title: in.Title, Description: in.Description, AgeRating: in.AgeRating,
			StartsAt: in.StartsAt, EndsAt: in.EndsAt, SalesStartAt: in.SalesStartAt, SalesEndAt: in.SalesEndAt,
			MaxTicketsPerBuyer: in.MaxTicketsPerBuyer, RefundDeadlineHours: *in.RefundDeadlineHours,
		})
		if err != nil {
			return eventWriteError(err, "update event")
		}
		if deref(cur.SeatMapID) != deref(e.SeatMapID) {
			// Цены привязаны к секторам прежней схемы.
			if err := q.DeleteEventPrices(ctx, catalogdb.DeleteEventPricesParams{OrganizerID: organizerID, EventID: id}); err != nil {
				return fmt.Errorf("reset prices: %w", err)
			}
		}
		out = eventFrom(e)
		return nil
	})
	return out, err
}

// GetEvent возвращает событие организатора.
func (s *Service) GetEvent(ctx context.Context, organizerID, id string) (Event, error) {
	e, err := s.q.GetEvent(ctx, catalogdb.GetEventParams{OrganizerID: organizerID, ID: id})
	if err != nil {
		return Event{}, notFound(err, "get event")
	}
	return eventFrom(e), nil
}

// ListEvents возвращает события организатора, ближайшие по дате сверху.
func (s *Service) ListEvents(ctx context.Context, organizerID string) ([]Event, error) {
	rows, err := s.q.ListEvents(ctx, organizerID)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	out := make([]Event, 0, len(rows))
	for _, e := range rows {
		out = append(out, eventFrom(e))
	}
	return out, nil
}

func requireDraft(status string) error {
	switch status {
	case "draft":
		return nil
	case "published":
		return &ConflictError{Code: "event_published", Message: "published event cannot be changed this way"}
	default:
		return &ConflictError{Code: "event_cancelled", Message: "event is cancelled"}
	}
}

func (in EventInput) normalize() (EventInput, error) {
	in.VenueID = strings.TrimSpace(in.VenueID)
	in.SeatMapID = strings.TrimSpace(in.SeatMapID)
	if in.VenueID == "" {
		return in, &ValidationError{Field: "venue_id", Message: "is required"}
	}
	if in.Admission == "" {
		in.Admission = AdmissionTicketed
	}
	switch in.Admission {
	case AdmissionTicketed:
		if in.SeatMapID == "" {
			return in, &ValidationError{Field: "seat_map_id", Message: "is required for a ticketed event"}
		}
	case AdmissionFreeEntry:
		if in.SeatMapID != "" {
			return in, &ValidationError{Field: "seat_map_id", Message: "must be empty for a free-entry event"}
		}
	default:
		return in, &ValidationError{Field: "admission", Message: "must be ticketed or free_entry"}
	}
	in.Slug = strings.TrimSpace(in.Slug)
	if len(in.Slug) < 3 || len(in.Slug) > 63 || !slugPattern.MatchString(in.Slug) {
		return in, &ValidationError{Field: "slug", Message: "must be 3-63 characters: lowercase latin letters, digits and single hyphens"}
	}
	in.Title = strings.TrimSpace(in.Title)
	if n := utf8.RuneCountInString(in.Title); n == 0 || n > 200 {
		return in, &ValidationError{Field: "title", Message: "must be 1-200 characters"}
	}
	in.Description = strings.TrimSpace(in.Description)
	if utf8.RuneCountInString(in.Description) > maxDescriptionLen {
		return in, &ValidationError{Field: "description", Message: fmt.Sprintf("must be at most %d characters", maxDescriptionLen)}
	}
	if in.AgeRating == "" {
		in.AgeRating = "0+"
	}
	if !slices.Contains(ageRatings, in.AgeRating) {
		return in, &ValidationError{Field: "age_rating", Message: "must be one of 0+, 6+, 12+, 16+, 18+"}
	}
	if in.StartsAt.IsZero() {
		return in, &ValidationError{Field: "starts_at", Message: "is required"}
	}
	if !in.EndsAt.After(in.StartsAt) {
		return in, &ValidationError{Field: "ends_at", Message: "must be after starts_at"}
	}
	// Время хранится в UTC (CLAUDE.md, правило 6).
	in.StartsAt, in.EndsAt = in.StartsAt.UTC(), in.EndsAt.UTC()
	in.SalesStartAt, in.SalesEndAt = utcPtr(in.SalesStartAt), utcPtr(in.SalesEndAt)
	if in.SalesStartAt != nil && in.SalesEndAt != nil && !in.SalesEndAt.After(*in.SalesStartAt) {
		return in, &ValidationError{Field: "sales_end_at", Message: "must be after sales_start_at"}
	}
	if in.SalesEndAt != nil && in.SalesEndAt.After(in.EndsAt) {
		return in, &ValidationError{Field: "sales_end_at", Message: "must not be after ends_at"}
	}
	if in.MaxTicketsPerBuyer == 0 {
		in.MaxTicketsPerBuyer = defaultMaxTicketsPerBuyer
	}
	if in.MaxTicketsPerBuyer < 1 || in.MaxTicketsPerBuyer > maxTicketsPerBuyerLimit {
		return in, &ValidationError{Field: "max_tickets_per_buyer", Message: fmt.Sprintf("must be 1-%d", maxTicketsPerBuyerLimit)}
	}
	if in.RefundDeadlineHours == nil {
		d := int32(defaultRefundDeadlineHours)
		in.RefundDeadlineHours = &d
	}
	if *in.RefundDeadlineHours < 0 || *in.RefundDeadlineHours > maxRefundDeadlineHours {
		return in, &ValidationError{Field: "refund_deadline_hours", Message: fmt.Sprintf("must be 0-%d", maxRefundDeadlineHours)}
	}
	return in, nil
}

// Виды входа на событие.
const (
	AdmissionTicketed  = "ticketed"
	AdmissionFreeEntry = "free_entry" // свободный вход: без билетов и схемы зала
)

func (in EventInput) seatMap() *string {
	if in.SeatMapID == "" {
		return nil
	}
	return &in.SeatMapID
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// eventWriteError переводит ошибки ограничений при записи события.
func eventWriteError(err error, op string) error {
	switch uniqueConstraint(err) {
	case "events_organizer_id_slug_key":
		return &ConflictError{Code: "slug_taken", Message: "event with this slug already exists"}
	case "":
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && (pgErr.Code == "23503" || pgErr.Code == "22P02") {
		// Площадка или схема не найдена, чужая или схема другой площадки.
		return &ValidationError{Field: "seat_map_id", Message: "venue or seat map not found, or the seat map belongs to another venue"}
	}
	return fmt.Errorf("%s: %w", op, err)
}

func eventFrom(e catalogdb.Event) Event {
	return Event{
		ID: e.ID, VenueID: e.VenueID, Admission: e.Admission, SeatMapID: e.SeatMapID, Slug: e.Slug, Title: e.Title,
		Description: e.Description, AgeRating: e.AgeRating, Status: e.Status,
		StartsAt: e.StartsAt, EndsAt: e.EndsAt, SalesStartAt: e.SalesStartAt, SalesEndAt: e.SalesEndAt,
		MaxTicketsPerBuyer: e.MaxTicketsPerBuyer, RefundDeadlineHours: e.RefundDeadlineHours,
		CoverImageKey: e.CoverImageKey, CoverVideoKey: e.CoverVideoKey,
		PublishedAt: e.PublishedAt, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}
