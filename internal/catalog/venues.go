package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // часовые пояса внутри бинаря: образ distroless может их не содержать
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/catalog/catalogdb"
)

// VenueInput — данные площадки от организатора.
type VenueInput struct {
	Name      string   `json:"name"`
	Address   string   `json:"address"`
	Timezone  string   `json:"timezone"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

// Venue — площадка: любое место проведения (spec.md).
type Venue struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Address   string    `json:"address"`
	Timezone  string    `json:"timezone"`
	Latitude  *float64  `json:"latitude"`
	Longitude *float64  `json:"longitude"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const defaultTimezone = "Asia/Almaty"

// CreateVenue создаёт площадку организатора.
func (s *Service) CreateVenue(ctx context.Context, organizerID string, in VenueInput) (Venue, error) {
	in, err := in.normalize()
	if err != nil {
		return Venue{}, err
	}
	v, err := s.q.CreateVenue(ctx, catalogdb.CreateVenueParams{
		OrganizerID: organizerID, Name: in.Name, Address: in.Address,
		Timezone: in.Timezone, Latitude: in.Latitude, Longitude: in.Longitude,
	})
	if err != nil {
		return Venue{}, fmt.Errorf("create venue: %w", err)
	}
	return venueFrom(v), nil
}

// UpdateVenue заменяет данные площадки организатора.
func (s *Service) UpdateVenue(ctx context.Context, organizerID, id string, in VenueInput) (Venue, error) {
	in, err := in.normalize()
	if err != nil {
		return Venue{}, err
	}
	v, err := s.q.UpdateVenue(ctx, catalogdb.UpdateVenueParams{
		OrganizerID: organizerID, ID: id, Name: in.Name, Address: in.Address,
		Timezone: in.Timezone, Latitude: in.Latitude, Longitude: in.Longitude,
	})
	if err != nil {
		return Venue{}, notFound(err, "update venue")
	}
	return venueFrom(v), nil
}

// GetVenue возвращает площадку организатора.
func (s *Service) GetVenue(ctx context.Context, organizerID, id string) (Venue, error) {
	v, err := s.q.GetVenue(ctx, catalogdb.GetVenueParams{OrganizerID: organizerID, ID: id})
	if err != nil {
		return Venue{}, notFound(err, "get venue")
	}
	return venueFrom(v), nil
}

// ListVenues возвращает площадки организатора.
func (s *Service) ListVenues(ctx context.Context, organizerID string) ([]Venue, error) {
	rows, err := s.q.ListVenues(ctx, organizerID)
	if err != nil {
		return nil, fmt.Errorf("list venues: %w", err)
	}
	out := make([]Venue, 0, len(rows))
	for _, v := range rows {
		out = append(out, venueFrom(v))
	}
	return out, nil
}

func (in VenueInput) normalize() (VenueInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n == 0 || n > 200 {
		return in, &ValidationError{Field: "name", Message: "must be 1-200 characters"}
	}
	in.Address = strings.TrimSpace(in.Address)
	if utf8.RuneCountInString(in.Address) > 500 {
		return in, &ValidationError{Field: "address", Message: "must be at most 500 characters"}
	}
	in.Timezone = strings.TrimSpace(in.Timezone)
	if in.Timezone == "" {
		in.Timezone = defaultTimezone
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil || in.Timezone == "Local" {
		return in, &ValidationError{Field: "timezone", Message: "must be an IANA time zone, e.g. Asia/Almaty"}
	}
	if (in.Latitude == nil) != (in.Longitude == nil) {
		return in, &ValidationError{Field: "latitude", Message: "latitude and longitude must be set together"}
	}
	if in.Latitude != nil && (*in.Latitude < -90 || *in.Latitude > 90) {
		return in, &ValidationError{Field: "latitude", Message: "must be between -90 and 90"}
	}
	if in.Longitude != nil && (*in.Longitude < -180 || *in.Longitude > 180) {
		return in, &ValidationError{Field: "longitude", Message: "must be between -180 and 180"}
	}
	return in, nil
}

func venueFrom(v catalogdb.Venue) Venue {
	return Venue{
		ID: v.ID, Name: v.Name, Address: v.Address, Timezone: v.Timezone,
		Latitude: v.Latitude, Longitude: v.Longitude, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
}

// notFound превращает отсутствие строки и неверный UUID в ErrNotFound.
func notFound(err error, op string) error {
	if errors.Is(err, pgx.ErrNoRows) || invalidUUID(err) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}
