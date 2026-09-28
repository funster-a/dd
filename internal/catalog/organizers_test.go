package catalog

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/funster-a/dd/internal/platform/auth"
	"github.com/funster-a/dd/internal/platform/db/dbtest"
	"github.com/funster-a/dd/internal/platform/idempotency"
)

func suffix() string { return strings.ToLower(rand.Text()[:8]) }

func newOrganizer() NewOrganizer {
	s := suffix()
	return NewOrganizer{Name: "Stand-up Club " + s, Slug: "standup-" + s, OwnerEmail: "owner-" + s + "@example.com"}
}

func TestCreateOrganizer(t *testing.T) {
	db := dbtest.New(t)
	svc := NewService(db.Pool)
	ctx := t.Context()

	in := newOrganizer()
	in.OwnerEmail = "  " + strings.ToUpper(in.OwnerEmail[:1]) + in.OwnerEmail[1:] + " "
	org, err := svc.CreateOrganizer(ctx, in)
	if err != nil {
		t.Fatalf("CreateOrganizer() error = %v", err)
	}
	if org.ID == "" || org.OwnerEmail != strings.ToLower(strings.TrimSpace(in.OwnerEmail)) {
		t.Fatalf("organizer = %+v", org)
	}

	list, err := svc.ListOrganizers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != org.ID || list[0].OwnerEmail != org.OwnerEmail {
		t.Fatalf("ListOrganizers() = %+v, want the created organizer", list)
	}
}

func TestCreateOrganizerConflicts(t *testing.T) {
	db := dbtest.New(t)
	svc := NewService(db.Pool)
	ctx := t.Context()
	first := newOrganizer()
	if _, err := svc.CreateOrganizer(ctx, first); err != nil {
		t.Fatal(err)
	}

	sameSlug := newOrganizer()
	sameSlug.Slug = first.Slug
	_, err := svc.CreateOrganizer(ctx, sameSlug)
	if c, ok := errors.AsType[*ConflictError](err); !ok || c.Code != "slug_taken" {
		t.Fatalf("same slug: err = %v, want slug_taken", err)
	}

	sameOwner := newOrganizer()
	sameOwner.OwnerEmail = first.OwnerEmail
	_, err = svc.CreateOrganizer(ctx, sameOwner)
	if c, ok := errors.AsType[*ConflictError](err); !ok || c.Code != "owner_email_taken" {
		t.Fatalf("same owner email: err = %v, want owner_email_taken", err)
	}

	// Транзакция откатилась: организатор без владельца не остался.
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM organizers WHERE slug = $1`, sameOwner.Slug).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("organizer %s exists after failed owner insert", sameOwner.Slug)
	}
}

func TestCreateOrganizerValidation(t *testing.T) {
	db := dbtest.New(t)
	svc := NewService(db.Pool)

	tests := map[string]struct {
		mutate func(*NewOrganizer)
		field  string
	}{
		"empty name":      {func(o *NewOrganizer) { o.Name = "  " }, "name"},
		"long name":       {func(o *NewOrganizer) { o.Name = strings.Repeat("я", 201) }, "name"},
		"upper slug":      {func(o *NewOrganizer) { o.Slug = "Standup" }, "slug"},
		"short slug":      {func(o *NewOrganizer) { o.Slug = "ab" }, "slug"},
		"double hyphen":   {func(o *NewOrganizer) { o.Slug = "stand--up" }, "slug"},
		"bad email":       {func(o *NewOrganizer) { o.OwnerEmail = "owner" }, "owner_email"},
		"email with name": {func(o *NewOrganizer) { o.OwnerEmail = "Owner <o@example.com>" }, "owner_email"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			in := newOrganizer()
			tt.mutate(&in)
			_, err := svc.CreateOrganizer(t.Context(), in)
			if v, ok := errors.AsType[*ValidationError](err); !ok || v.Field != tt.field {
				t.Fatalf("err = %v, want validation error on %s", err, tt.field)
			}
		})
	}
}

// TestConcurrentCreateSameSlug: одновременное создание организатора с одним
// slug — успешно ровно одно, остальные получают slug_taken.
func TestConcurrentCreateSameSlug(t *testing.T) {
	db := dbtest.New(t)
	svc := NewService(db.Pool)
	slug := "race-" + suffix()

	const n = 10
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		other   []error
	)
	start := make(chan struct{})
	for range n {
		wg.Go(func() {
			in := newOrganizer()
			in.Slug = slug
			<-start
			_, err := svc.CreateOrganizer(context.Background(), in)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				created++
				return
			}
			if c, ok := errors.AsType[*ConflictError](err); !ok || c.Code != "slug_taken" {
				other = append(other, err)
			}
		})
	}
	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other)
	}
	if created != 1 {
		t.Fatalf("created %d organizers with one slug, want 1", created)
	}
}

func TestAdminHTTP(t *testing.T) {
	db := dbtest.New(t)
	svc := NewService(db.Pool)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if kind := r.Header.Get("X-Test-Kind"); kind != "" {
				r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.Kind(kind), SubjectID: "admin@example.com"}))
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Mount("/v1/admin", svc.AdminRoutes())
	srv := httptest.NewServer(r)
	defer srv.Close()

	do := func(method, kind, key string, body any) (int, []byte) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req, _ := http.NewRequestWithContext(t.Context(), method, srv.URL+"/v1/admin/organizers", &buf)
		if kind != "" {
			req.Header.Set("X-Test-Kind", kind)
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

	in := newOrganizer()
	if code, _ := do(http.MethodPost, "", "k1", in); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: code = %d, want 401", code)
	}
	if code, _ := do(http.MethodPost, "organizer", "k1", in); code != http.StatusForbidden {
		t.Fatalf("organizer: code = %d, want 403", code)
	}
	if code, _ := do(http.MethodPost, "admin", "", in); code != http.StatusBadRequest {
		t.Fatalf("no idempotency key: code = %d, want 400", code)
	}

	code, first := do(http.MethodPost, "admin", "k1", in)
	if code != http.StatusCreated {
		t.Fatalf("create: code = %d, body %s", code, first)
	}
	// Повтор того же запроса (например, после обрыва сети) не создаёт второго организатора.
	code, again := do(http.MethodPost, "admin", "k1", in)
	if code != http.StatusCreated || !bytes.Equal(first, again) {
		t.Fatalf("replay: code = %d, body %s, want the first response", code, again)
	}

	code, data := do(http.MethodGet, "admin", "", nil)
	if code != http.StatusOK {
		t.Fatalf("list: code = %d", code)
	}
	var list struct {
		Organizers []Organizer `json:"organizers"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Organizers) != 1 {
		t.Fatalf("organizers = %d, want 1", len(list.Organizers))
	}

	if code, _ := do(http.MethodPost, "admin", "k2", NewOrganizer{Name: "x", Slug: "Bad Slug", OwnerEmail: "a@b.c"}); code != http.StatusBadRequest {
		t.Fatalf("invalid slug: code = %d, want 400", code)
	}
}
