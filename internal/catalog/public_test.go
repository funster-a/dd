package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// memCache — кэш в памяти для тестов.
type memCache struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (c *memCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.data[key]
	return b, ok, nil
}

func (c *memCache) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data == nil {
		c.data = map[string][]byte{}
	}
	c.data[key] = value
	return nil
}

func (c *memCache) Delete(_ context.Context, keys ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		delete(c.data, k)
	}
	return nil
}

func newPublicEnv(t *testing.T) (*eventEnv, *memCache, string) {
	t.Helper()
	e := newEventEnv(t)
	cache := &memCache{}
	e.svc.cache = cache
	slug, err := e.svc.q.GetOrganizerSlug(t.Context(), e.org)
	if err != nil {
		t.Fatal(err)
	}
	return e, cache, slug
}

func TestPublicEvent(t *testing.T) {
	e, cache, orgSlug := newPublicEnv(t)
	ctx := t.Context()
	ev := e.draft(t)
	e.ready(t, ev)

	if _, err := e.svc.GetPublicEvent(ctx, orgSlug, ev.Slug); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draft: err = %v, want ErrNotFound", err)
	}
	if len(cache.data) != 0 {
		t.Fatal("not found must not be cached")
	}
	if _, err := e.svc.Publish(ctx, e.org, ev.ID, time.Now()); err != nil {
		t.Fatal(err)
	}

	b, err := e.svc.GetPublicEvent(ctx, orgSlug, ev.Slug)
	if err != nil {
		t.Fatal(err)
	}
	var pe PublicEvent
	if err := json.Unmarshal(b, &pe); err != nil {
		t.Fatal(err)
	}
	if pe.Title != ev.Title || pe.Venue.Name != "Клуб" || len(pe.Prices) != len(fullPrices) || len(pe.Layout.Sections) == 0 {
		t.Errorf("public event = %+v", pe)
	}
	if pe.CoverImageURL == "" || pe.CoverVideoURL != nil {
		t.Errorf("cover urls = %q, %v", pe.CoverImageURL, pe.CoverVideoURL)
	}
	if !pe.StartsAt.Equal(ev.StartsAt) || pe.StartsAt.Location() != time.UTC {
		t.Errorf("starts_at = %v, want %v in UTC", pe.StartsAt, ev.StartsAt)
	}

	// Второй запрос — из кэша, база не трогается.
	if _, err := e.svc.GetPublicEvent(ctx, orgSlug, ev.Slug); err != nil {
		t.Fatal(err)
	}
	if n := e.svc.publicLoads.Load(); n != 2 { // промах на черновике + одна сборка
		t.Errorf("loads = %d, want 2", n)
	}

	// Смена обложки сбрасывает кэш.
	up, err := e.svc.CreateUpload(ctx, e.org, ev.ID, UploadRequest{Kind: MediaCoverVideo, ContentType: "video/mp4", Size: 1000})
	if err != nil {
		t.Fatal(err)
	}
	e.store.put(up.Key, ObjectInfo{Size: 1000, ContentType: "video/mp4"})
	cur, err := e.svc.GetEvent(ctx, e.org, ev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetMedia(ctx, e.org, ev.ID, MediaInput{CoverImageKey: *cur.CoverImageKey, CoverVideoKey: &up.Key}); err != nil {
		t.Fatal(err)
	}
	b, err = e.svc.GetPublicEvent(ctx, orgSlug, ev.Slug)
	if err != nil {
		t.Fatal(err)
	}
	pe = PublicEvent{}
	if err := json.Unmarshal(b, &pe); err != nil {
		t.Fatal(err)
	}
	if pe.CoverVideoURL == nil {
		t.Error("cover video not visible after media change: cache not invalidated")
	}
}

// Холодный кэш и тысяча одновременных открытий страницы (старт продаж):
// в базу уходит один запрос.
func TestPublicEventColdCacheStampede(t *testing.T) {
	e, _, orgSlug := newPublicEnv(t)
	ctx := t.Context()
	ev := e.draft(t)
	e.ready(t, ev)
	if _, err := e.svc.Publish(ctx, e.org, ev.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	loadsBefore := e.svc.publicLoads.Load()

	const n = 200
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			<-start
			_, err := e.svc.GetPublicEvent(ctx, orgSlug, ev.Slug)
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Горутины, стартовавшие после заполнения кэша, в базу не идут вовсе;
	// одновременные промахи схлопываются в одну загрузку.
	if got := e.svc.publicLoads.Load() - loadsBefore; got != 1 {
		t.Errorf("database loads = %d, want 1", got)
	}
}

func TestPublicEventHTTP(t *testing.T) {
	e, _, orgSlug := newPublicEnv(t)
	ev := e.draft(t)
	e.ready(t, ev)
	h := e.svc.PublicRoutes()

	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/events/"+orgSlug+"/"+ev.Slug, nil))
		return rec
	}
	if rec := get(); rec.Code != http.StatusNotFound {
		t.Fatalf("draft: status = %d, want 404", rec.Code)
	}
	if _, err := e.svc.Publish(t.Context(), e.org, ev.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	rec := get()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=60" || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", rec.Header())
	}
}
