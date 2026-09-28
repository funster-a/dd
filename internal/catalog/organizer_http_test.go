package catalog

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/funster-a/dd/internal/platform/auth"
	"github.com/funster-a/dd/internal/platform/db/dbtest"
	"github.com/funster-a/dd/internal/platform/idempotency"
)

func TestOrganizerHTTP(t *testing.T) {
	db := dbtest.New(t)
	svc := NewService(db.Pool)
	a, b := newTenant(t, svc), newTenant(t, svc)

	r := chi.NewRouter()
	// Участник из заголовков — вместо настоящей сессии identity.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if kind := r.Header.Get("X-Test-Kind"); kind != "" {
				r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{
					Kind: auth.Kind(kind), SubjectID: "member", OrganizerID: r.Header.Get("X-Test-Org"),
				}))
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Mount("/v1/organizer", svc.OrganizerRoutes())
	srv := httptest.NewServer(r)
	defer srv.Close()

	do := func(method, path, kind, org, key string, body any) (int, []byte) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req, _ := http.NewRequestWithContext(t.Context(), method, srv.URL+"/v1/organizer"+path, &buf)
		if kind != "" {
			req.Header.Set("X-Test-Kind", kind)
			req.Header.Set("X-Test-Org", org)
		}
		if key != "" {
			req.Header.Set(idempotency.Header, key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, data
	}

	venueIn := VenueInput{Name: "Клуб", Latitude: ptr(43.2), Longitude: ptr(76.9)}
	if code, _ := do(http.MethodPost, "/venues", "buyer", "", "k1", venueIn); code != http.StatusForbidden {
		t.Fatalf("buyer: code = %d, want 403", code)
	}
	if code, _ := do(http.MethodPost, "/venues", "organizer", a, "", venueIn); code != http.StatusBadRequest {
		t.Fatalf("no idempotency key: code = %d, want 400", code)
	}

	code, data := do(http.MethodPost, "/venues", "organizer", a, "k1", venueIn)
	if code != http.StatusCreated {
		t.Fatalf("create venue: code = %d, body %s", code, data)
	}
	var venue Venue
	_ = json.Unmarshal(data, &venue)

	code, data = do(http.MethodPost, "/venues/"+venue.ID+"/seat-maps", "organizer", a, "k2",
		map[string]any{"name": "Основная", "layout": json.RawMessage(hallLayout)})
	if code != http.StatusCreated {
		t.Fatalf("create seat map: code = %d, body %s", code, data)
	}
	var seatMap SeatMap
	_ = json.Unmarshal(data, &seatMap)
	if seatMap.SeatCount != 102 {
		t.Fatalf("seat_count = %d, want 102", seatMap.SeatCount)
	}

	code, data = do(http.MethodPost, "/venues/"+venue.ID+"/seat-maps", "organizer", a, "k3",
		map[string]any{"name": "Битая", "layout": map[string]any{"sections": []any{map[string]any{"name": "A", "kind": "vip"}}}})
	if code != http.StatusBadRequest || !bytes.Contains(data, []byte(`"invalid_layout.sections[0].kind"`)) {
		t.Fatalf("invalid layout: code = %d, body %s", code, data)
	}

	// Организатор B не видит данные A — ни площадку, ни схему.
	for _, path := range []string{"/venues/" + venue.ID, "/venues/" + venue.ID + "/seat-maps", "/seat-maps/" + seatMap.ID} {
		if code, _ := do(http.MethodGet, path, "organizer", b, "", nil); code != http.StatusNotFound {
			t.Errorf("GET %s as other organizer: code = %d, want 404", path, code)
		}
	}
	if code, _ := do(http.MethodGet, "/venues/"+venue.ID, "organizer", a, "", nil); code != http.StatusOK {
		t.Errorf("GET own venue: code = %d, want 200", code)
	}
}
