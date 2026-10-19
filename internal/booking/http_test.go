package booking

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/funster-a/dd/internal/platform/auth"
	"github.com/funster-a/dd/internal/platform/idempotency"
)

func TestBookingHTTP(t *testing.T) {
	e := newEnv(t, false, 1, 2, 5)
	alice, bob := e.buyer(t), e.buyer(t)

	r := chi.NewRouter()
	// Покупатель из заголовка — вместо настоящей сессии identity.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id := r.Header.Get("X-Test-Buyer"); id != "" {
				r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindBuyer, SubjectID: id}))
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Route("/v1", e.svc.Register)
	srv := httptest.NewServer(r)
	defer srv.Close()

	do := func(method, path, buyer, key string, body any) (int, http.Header, []byte) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req, _ := http.NewRequestWithContext(t.Context(), method, srv.URL+"/v1"+path, &buf)
		if buyer != "" {
			req.Header.Set("X-Test-Buyer", buyer)
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
		return resp.StatusCode, resp.Header, data
	}
	orders := "/events/" + e.eventID + "/orders"
	body := map[string]any{
		"seats":   []map[string]string{{"section": "Партер", "row": "1", "seat": "1"}},
		"general": []map[string]any{{"section": "Фан-зона", "quantity": 2}},
		"email":   "alice@example.com",
	}

	if code, _, _ := do(http.MethodPost, orders, "", "k1", body); code != http.StatusUnauthorized {
		t.Errorf("anonymous create = %d, want 401", code)
	}
	if code, _, _ := do(http.MethodPost, orders, alice, "", body); code != http.StatusBadRequest {
		t.Errorf("create without Idempotency-Key = %d, want 400", code)
	}

	code, _, data := do(http.MethodPost, orders, alice, "k1", body)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, data)
	}
	var o Order
	if err := json.Unmarshal(data, &o); err != nil {
		t.Fatal(err)
	}
	if o.Status != "pending" || len(o.Items) != 3 || o.TotalTiyn != partPrice+2*fanPrice {
		t.Errorf("order = %+v", o)
	}
	// Повтор с тем же ключом — тот же заказ, а не второй.
	if code, _, again := do(http.MethodPost, orders, alice, "k1", body); code != http.StatusCreated || !bytes.Equal(again, data) {
		t.Errorf("replay = %d %s", code, again)
	}

	if code, _, data := do(http.MethodPost, orders, bob, "k2", body); code != http.StatusConflict || !bytes.Contains(data, []byte("seat_taken")) {
		t.Errorf("bob takes the same seat = %d %s", code, data)
	}

	code, hdr, data := do(http.MethodGet, "/events/"+e.eventID+"/availability", "", "", nil)
	if code != http.StatusOK || hdr.Get("Cache-Control") != "public, max-age=2" {
		t.Fatalf("availability = %d %v", code, hdr)
	}
	var a Availability
	if err := json.Unmarshal(data, &a); err != nil || len(a.Taken) != 1 || a.General[0].Available != 3 {
		t.Errorf("availability = %s, %v", data, err)
	}

	if code, _, _ := do(http.MethodGet, "/orders/"+o.ID, alice, "", nil); code != http.StatusOK {
		t.Errorf("alice get = %d", code)
	}
	if code, _, _ := do(http.MethodGet, "/orders/"+o.ID, bob, "", nil); code != http.StatusNotFound {
		t.Errorf("bob gets alice's order = %d, want 404", code)
	}
	if code, _, _ := do(http.MethodPost, "/orders/"+o.ID+"/cancel", bob, "c1", nil); code != http.StatusNotFound {
		t.Errorf("bob cancels alice's order = %d, want 404", code)
	}
	code, _, data = do(http.MethodPost, "/orders/"+o.ID+"/cancel", alice, "c1", nil)
	if code != http.StatusOK || !bytes.Contains(data, []byte(`"status":"cancelled"`)) {
		t.Errorf("cancel = %d %s", code, data)
	}
	if code, _, data := do(http.MethodPost, orders, bob, "k3", body); code != http.StatusCreated {
		t.Errorf("bob after cancel = %d %s", code, data)
	}

	tooMany := map[string]any{"general": []map[string]any{{"section": "Фан-зона", "quantity": 11}}, "email": "a@b.kz"}
	if code, _, data := do(http.MethodPost, orders, alice, "k4", tooMany); code != http.StatusUnprocessableEntity {
		t.Errorf("over limit = %d %s", code, data)
	}
	if code, _, _ := do(http.MethodGet, "/events/00000000-0000-7000-8000-000000000000/availability", "", "", nil); code != http.StatusNotFound {
		t.Errorf("unknown event availability = %d", code)
	}
}
