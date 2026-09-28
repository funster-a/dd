package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/funster-a/dd/internal/catalog/catalogdb"
)

// SeatMapInput — схема зала от организатора.
type SeatMapInput struct {
	Name   string          `json:"name"`
	Layout json.RawMessage `json:"layout"`
}

// SeatMap — сохранённая схема зала.
type SeatMap struct {
	ID        string    `json:"id"`
	VenueID   string    `json:"venue_id"`
	Name      string    `json:"name"`
	Layout    Layout    `json:"layout"`
	SeatCount int       `json:"seat_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateSeatMap проверяет и сохраняет схему зала для площадки организатора.
func (s *Service) CreateSeatMap(ctx context.Context, organizerID, venueID string, in SeatMapInput) (SeatMap, error) {
	name := strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(name); n == 0 || n > 200 {
		return SeatMap{}, &ValidationError{Field: "name", Message: "must be 1-200 characters"}
	}
	layout, err := ParseLayout(in.Layout)
	if err != nil {
		return SeatMap{}, err
	}
	// Сохраняем нормализованную схему, а не исходный текст.
	doc, err := json.Marshal(layout)
	if err != nil {
		return SeatMap{}, fmt.Errorf("encode layout: %w", err)
	}
	m, err := s.q.CreateSeatMap(ctx, catalogdb.CreateSeatMapParams{
		OrganizerID: organizerID, VenueID: venueID, Name: name, Layout: doc,
	})
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && (pgErr.Code == "23503" || pgErr.Code == "22P02") {
		// Составной внешний ключ (organizer_id, venue_id): площадки нет
		// или она чужая — для организатора это одно и то же.
		return SeatMap{}, ErrNotFound
	}
	if err != nil {
		return SeatMap{}, fmt.Errorf("create seat map: %w", err)
	}
	return seatMapFrom(m)
}

// GetSeatMap возвращает схему зала организатора.
func (s *Service) GetSeatMap(ctx context.Context, organizerID, id string) (SeatMap, error) {
	m, err := s.q.GetSeatMap(ctx, catalogdb.GetSeatMapParams{OrganizerID: organizerID, ID: id})
	if err != nil {
		return SeatMap{}, notFound(err, "get seat map")
	}
	return seatMapFrom(m)
}

// ListSeatMaps возвращает схемы площадки организатора.
func (s *Service) ListSeatMaps(ctx context.Context, organizerID, venueID string) ([]SeatMap, error) {
	if _, err := s.GetVenue(ctx, organizerID, venueID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListSeatMaps(ctx, catalogdb.ListSeatMapsParams{OrganizerID: organizerID, VenueID: venueID})
	if err != nil {
		return nil, fmt.Errorf("list seat maps: %w", err)
	}
	out := make([]SeatMap, 0, len(rows))
	for _, r := range rows {
		m, err := seatMapFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func seatMapFrom(m catalogdb.SeatMap) (SeatMap, error) {
	var l Layout
	if err := json.Unmarshal(m.Layout, &l); err != nil {
		return SeatMap{}, fmt.Errorf("decode stored layout %s: %w", m.ID, err)
	}
	return SeatMap{
		ID: m.ID, VenueID: m.VenueID, Name: m.Name, Layout: l, SeatCount: l.SeatCount(),
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}, nil
}
