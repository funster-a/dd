package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/funster-a/dd/internal/platform/db/dbtest"
)

func ptr(f float64) *float64 { return &f }

// newTenant заводит организатора и возвращает его id.
func newTenant(t *testing.T, svc *Service) string {
	t.Helper()
	org, err := svc.CreateOrganizer(t.Context(), newOrganizer())
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

const hallLayout = `{"sections":[
	{"name":"Партер","kind":"seat","rows":[{"label":"1","seats":[{"label":"1"},{"label":"2"}]}]},
	{"name":"Танцпол","kind":"general","capacity":100}
]}`

func TestVenueLifecycle(t *testing.T) {
	db := dbtest.New(t)
	svc := NewService(db.Pool)
	ctx := t.Context()
	org := newTenant(t, svc)

	v, err := svc.CreateVenue(ctx, org, VenueInput{
		Name: " Парк Горького ", Address: "Алматы, ул. Гоголя, 1", Latitude: ptr(43.2567), Longitude: ptr(76.9286),
	})
	if err != nil {
		t.Fatalf("CreateVenue() error = %v", err)
	}
	if v.Name != "Парк Горького" || v.Timezone != defaultTimezone || *v.Latitude != 43.2567 {
		t.Fatalf("venue = %+v", v)
	}

	v2, err := svc.UpdateVenue(ctx, org, v.ID, VenueInput{Name: "Парк", Timezone: "Asia/Aqtobe"})
	if err != nil {
		t.Fatalf("UpdateVenue() error = %v", err)
	}
	if v2.Name != "Парк" || v2.Timezone != "Asia/Aqtobe" || v2.Latitude != nil || !v2.UpdatedAt.After(v.UpdatedAt) {
		t.Fatalf("updated venue = %+v", v2)
	}

	got, err := svc.GetVenue(ctx, org, v.ID)
	if err != nil || got.Name != "Парк" {
		t.Fatalf("GetVenue() = %+v, %v", got, err)
	}
	list, err := svc.ListVenues(ctx, org)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListVenues() = %+v, %v", list, err)
	}
}

func TestVenueValidation(t *testing.T) {
	db := dbtest.New(t)
	svc := NewService(db.Pool)
	org := newTenant(t, svc)

	tests := map[string]struct {
		in    VenueInput
		field string
	}{
		"empty name":      {VenueInput{Name: " "}, "name"},
		"bad timezone":    {VenueInput{Name: "A", Timezone: "Mars/Olympus"}, "timezone"},
		"local timezone":  {VenueInput{Name: "A", Timezone: "Local"}, "timezone"},
		"latitude only":   {VenueInput{Name: "A", Latitude: ptr(43)}, "latitude"},
		"latitude range":  {VenueInput{Name: "A", Latitude: ptr(91), Longitude: ptr(0)}, "latitude"},
		"longitude range": {VenueInput{Name: "A", Latitude: ptr(0), Longitude: ptr(-181)}, "longitude"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := svc.CreateVenue(t.Context(), org, tt.in)
			if v, ok := errors.AsType[*ValidationError](err); !ok || v.Field != tt.field {
				t.Fatalf("err = %v, want validation error on %s", err, tt.field)
			}
		})
	}
}

// TestTenantIsolationInCatalog: организатор видит и меняет только свои
// площадки и схемы; чужие отвечают «не найдено», как несуществующие.
func TestTenantIsolationInCatalog(t *testing.T) {
	db := dbtest.New(t)
	svc := NewService(db.Pool)
	ctx := t.Context()
	a, b := newTenant(t, svc), newTenant(t, svc)

	venueA, err := svc.CreateVenue(ctx, a, VenueInput{Name: "Зал A"})
	if err != nil {
		t.Fatal(err)
	}
	mapA, err := svc.CreateSeatMap(ctx, a, venueA.ID, SeatMapInput{Name: "Основная", Layout: json.RawMessage(hallLayout)})
	if err != nil {
		t.Fatal(err)
	}

	checks := map[string]func(context.Context) error{
		"get venue": func(ctx context.Context) error { _, err := svc.GetVenue(ctx, b, venueA.ID); return err },
		"update venue": func(ctx context.Context) error {
			_, err := svc.UpdateVenue(ctx, b, venueA.ID, VenueInput{Name: "X"})
			return err
		},
		"list seat maps": func(ctx context.Context) error { _, err := svc.ListSeatMaps(ctx, b, venueA.ID); return err },
		"get seat map":   func(ctx context.Context) error { _, err := svc.GetSeatMap(ctx, b, mapA.ID); return err },
		"create seat map": func(ctx context.Context) error {
			_, err := svc.CreateSeatMap(ctx, b, venueA.ID, SeatMapInput{Name: "X", Layout: json.RawMessage(hallLayout)})
			return err
		},
		"malformed id": func(ctx context.Context) error { _, err := svc.GetVenue(ctx, a, "not-a-uuid"); return err },
		"malformed venue": func(ctx context.Context) error {
			_, err := svc.CreateSeatMap(ctx, a, "nope", SeatMapInput{Name: "X", Layout: json.RawMessage(hallLayout)})
			return err
		},
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(t.Context()); !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
		})
	}

	if list, err := svc.ListVenues(ctx, b); err != nil || len(list) != 0 {
		t.Fatalf("organizer B sees venues %+v, err %v", list, err)
	}
	// Данные A не изменились.
	if v, _ := svc.GetVenue(ctx, a, venueA.ID); v.Name != "Зал A" {
		t.Fatalf("venue A changed: %+v", v)
	}
}

func TestSeatMapLifecycle(t *testing.T) {
	db := dbtest.New(t)
	svc := NewService(db.Pool)
	ctx := t.Context()
	org := newTenant(t, svc)
	venue, err := svc.CreateVenue(ctx, org, VenueInput{Name: "Клуб"})
	if err != nil {
		t.Fatal(err)
	}

	m, err := svc.CreateSeatMap(ctx, org, venue.ID, SeatMapInput{Name: "Концертная", Layout: json.RawMessage(hallLayout)})
	if err != nil {
		t.Fatalf("CreateSeatMap() error = %v", err)
	}
	if m.SeatCount != 102 || m.VenueID != venue.ID {
		t.Fatalf("seat map = %+v", m)
	}

	got, err := svc.GetSeatMap(ctx, org, m.ID)
	if err != nil || got.SeatCount != 102 || len(got.Layout.Sections) != 2 {
		t.Fatalf("GetSeatMap() = %+v, %v", got, err)
	}
	list, err := svc.ListSeatMaps(ctx, org, venue.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSeatMaps() = %+v, %v", list, err)
	}

	_, err = svc.CreateSeatMap(ctx, org, venue.ID, SeatMapInput{Name: "Битая", Layout: json.RawMessage(`{"sections":[]}`)})
	if v, ok := errors.AsType[*ValidationError](err); !ok || v.Field != "layout.sections" {
		t.Fatalf("invalid layout: err = %v", err)
	}
}
