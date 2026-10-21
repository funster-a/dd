package main

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/funster-a/dd/internal/catalog"
	"github.com/funster-a/dd/internal/platform/db/dbtest"
)

// memStore — хранилище в памяти: загрузка кладёт файл, Stat его находит.
type memStore struct {
	mu    sync.Mutex
	files map[string]catalog.ObjectInfo
}

func (m *memStore) PresignUpload(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	return "mem://" + key, nil
}

func (m *memStore) Stat(_ context.Context, key string) (catalog.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.files[key]
	if !ok {
		return catalog.ObjectInfo{}, catalog.ErrObjectNotFound
	}
	return info, nil
}

func (m *memStore) URL(key string) string { return "https://media.test/" + key }

func (m *memStore) upload(_ context.Context, t catalog.UploadTarget, contentType string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string]catalog.ObjectInfo{}
	}
	m.files[t.Key] = catalog.ObjectInfo{Size: int64(len(body)), ContentType: contentType}
	return nil
}

type demoCounts struct{ organizers, venues, seatMaps, events int }

func countDemo(t *testing.T, pool *pgxpool.Pool) demoCounts {
	t.Helper()
	var c demoCounts
	err := pool.QueryRow(t.Context(), `
		SELECT (SELECT count(*) FROM organizers WHERE slug = $1),
		       (SELECT count(*) FROM venues v JOIN organizers o ON o.id = v.organizer_id WHERE o.slug = $1),
		       (SELECT count(*) FROM seat_maps m JOIN venues v ON v.id = m.venue_id JOIN organizers o ON o.id = v.organizer_id WHERE o.slug = $1),
		       (SELECT count(*) FROM events e JOIN organizers o ON o.id = e.organizer_id WHERE o.slug = $1)`, demoSlug).
		Scan(&c.organizers, &c.venues, &c.seatMaps, &c.events)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// demoMedia — ключи обложек и цены демо-событий одной строкой для сравнения.
func demoMedia(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var out string
	err := pool.QueryRow(t.Context(), `
		SELECT coalesce(string_agg(e.slug || '=' || coalesce(e.cover_image_key, '') || ':' ||
		       (SELECT coalesce(string_agg(p.name || p.price_tiyn, ',' ORDER BY p.name), '') FROM price_categories p WHERE p.event_id = e.id),
		       ';' ORDER BY e.slug), '')
		FROM events e JOIN organizers o ON o.id = e.organizer_id WHERE o.slug = $1`, demoSlug).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDemoStale(t *testing.T) {
	now := time.Now()
	events := []demoEvent{
		{Path: "ok", Status: "published", StartsAt: now.Add(3 * 24 * time.Hour)},
		{Path: "soon", Status: "published", StartsAt: now.Add(12 * time.Hour)},
		{Path: "over", Status: "published", StartsAt: now.Add(-time.Hour)},
		{Path: "cancelled", Status: "cancelled", StartsAt: now.Add(10 * 24 * time.Hour)},
	}
	var got []string
	for _, e := range demoStale(events, now) {
		got = append(got, e.Path)
	}
	if len(got) != 3 || got[0] != "soon" || got[1] != "over" || got[2] != "cancelled" {
		t.Fatalf("stale = %v", got)
	}
}

func TestSeedDemo(t *testing.T) {
	pool := dbtest.New(t).Pool
	store := &memStore{}
	cat := catalog.NewService(pool, catalog.WithObjectStore(store))
	now := time.Now()

	org, events, err := seedDemo(t.Context(), cat, store.upload, now)
	if err != nil {
		t.Fatal(err)
	}
	if org.OwnerEmail != demoEmail {
		t.Fatalf("owner email = %q", org.OwnerEmail)
	}
	wantSeats := map[string]int{"/e/demo/standup": 126, "/e/demo/stadium-concert": 33509, "/e/demo/open-lecture": 120}
	if len(events) != len(wantSeats) {
		t.Fatalf("events = %+v", events)
	}
	for _, e := range events {
		if e.Status != "published" {
			t.Errorf("%s: status %s", e.Path, e.Status)
		}
		if e.Free != (e.Path == "/e/demo/open-lecture") {
			t.Errorf("%s: free = %v", e.Path, e.Free)
		}
	}
	rows, err := pool.Query(t.Context(), `
		SELECT '/e/demo/' || e.slug, count(s.id), e.cover_image_key IS NOT NULL, e.starts_at > $1
		FROM events e JOIN organizers o ON o.id = e.organizer_id LEFT JOIN event_seats s ON s.event_id = e.id
		WHERE o.slug = $2 GROUP BY e.slug, e.cover_image_key, e.starts_at`, now, demoSlug)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var path string
		var seats int
		var cover, future bool
		if err := rows.Scan(&path, &seats, &cover, &future); err != nil {
			t.Fatal(err)
		}
		if seats != wantSeats[path] || !cover || !future {
			t.Errorf("%s: seats %d (want %d), cover %v, in future %v", path, seats, wantSeats[path], cover, future)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Все три видны в афише сайта.
	upcoming, err := cat.ListUpcoming(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(upcoming) != 3 {
		t.Errorf("upcoming = %d events, want 3", len(upcoming))
	}

	// Повторный запуск ничего не создаёт и не трогает обложки и цены.
	before, media := countDemo(t, pool), demoMedia(t, pool)
	uploads := 0
	counting := func(ctx context.Context, tg catalog.UploadTarget, ct string, body []byte) error {
		uploads++
		return store.upload(ctx, tg, ct, body)
	}
	if _, again, err := seedDemo(t.Context(), cat, counting, now.Add(time.Minute)); err != nil || len(again) != 3 {
		t.Fatalf("second run: %v, %+v", err, again)
	}
	if after := countDemo(t, pool); after != before {
		t.Fatalf("second run changed data: %+v → %+v", before, after)
	}
	if uploads != 0 || demoMedia(t, pool) != media {
		t.Fatalf("second run: %d uploads, covers and prices %q → %q", uploads, media, demoMedia(t, pool))
	}
	if stale := demoStale(events, now); len(stale) != 0 {
		t.Fatalf("fresh demo reported stale: %+v", stale)
	}
	if before != (demoCounts{organizers: 1, venues: 2, seatMaps: 3, events: 3}) {
		t.Fatalf("counts = %+v", before)
	}
}

// Прерванный запуск — например, хранилище не ответило — следующий доводит до
// конца, не дублируя площадки и схемы.
func TestSeedDemoFinishesInterruptedRun(t *testing.T) {
	pool := dbtest.New(t).Pool
	store := &memStore{}
	cat := catalog.NewService(pool, catalog.WithObjectStore(store))
	now := time.Now()

	calls := 0
	flaky := func(ctx context.Context, tg catalog.UploadTarget, ct string, body []byte) error {
		calls++
		if calls == 2 {
			return errors.New("storage is down")
		}
		return store.upload(ctx, tg, ct, body)
	}
	if _, _, err := seedDemo(t.Context(), cat, flaky, now); err == nil {
		t.Fatal("interrupted run succeeded")
	}
	if c := countDemo(t, pool); c.events != 3 {
		t.Fatalf("after interrupted run: %+v", c)
	}
	// Стадион остался черновиком без обложки, остальные опубликованы.
	var status string
	var cover *string
	if err := pool.QueryRow(t.Context(), `SELECT status, cover_image_key FROM events WHERE slug = 'stadium-concert'`).Scan(&status, &cover); err != nil {
		t.Fatal(err)
	}
	if status != "draft" || cover != nil {
		t.Fatalf("stadium after interrupted run: %s, cover %v", status, cover)
	}
	// Следующий запуск — через 40 дней: дата черновика уже прошла, он получает
	// свежую и публикуется; события прежнего запуска к этому времени прошли.
	later := now.Add(40 * 24 * time.Hour)
	_, events, err := seedDemo(t.Context(), cat, store.upload, later)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Status != "published" {
			t.Errorf("%s: status %s", e.Path, e.Status)
		}
	}
	stale := demoStale(events, later)
	if len(stale) != 2 {
		t.Fatalf("stale = %+v, want standup and open-lecture", stale)
	}
	for _, e := range stale {
		if e.Path == "/e/demo/stadium-concert" {
			t.Fatalf("refreshed stadium reported stale: %+v", e)
		}
	}
	if c := countDemo(t, pool); c != (demoCounts{organizers: 1, venues: 2, seatMaps: 3, events: 3}) {
		t.Fatalf("counts = %+v", c)
	}
}

// Админский список организаторов — одна страница из 100 новых. Повторный
// запуск находит демо-организатора по адресу, сколько бы ни завели после него
// (нагрузочные серии заводят по организатору на прогон).
func TestSeedDemoFindsOrganizerAmongMany(t *testing.T) {
	pool := dbtest.New(t).Pool
	store := &memStore{}
	cat := catalog.NewService(pool, catalog.WithObjectStore(store))
	now := time.Now()
	if _, _, err := seedDemo(t.Context(), cat, store.upload, now); err != nil {
		t.Fatal(err)
	}
	for i := range 101 {
		n := strconv.Itoa(i)
		if _, err := cat.CreateOrganizer(t.Context(), catalog.NewOrganizer{Name: "Load " + n, Slug: "load-" + n, OwnerEmail: "load-" + n + "@example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, events, err := seedDemo(t.Context(), cat, store.upload, now.Add(time.Minute)); err != nil || len(events) != 3 {
		t.Fatalf("second run: %v, %+v", err, events)
	}
	if c := countDemo(t, pool); c != (demoCounts{organizers: 1, venues: 2, seatMaps: 3, events: 3}) {
		t.Fatalf("counts = %+v", c)
	}
}

func TestDemoCover(t *testing.T) {
	specs, err := demoSpecs()
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range specs {
		img, err := demoCover(sp.cover)
		if err != nil {
			t.Fatal(err)
		}
		// Лимит обложки в каталоге — 5 МБ (media.go).
		if len(img) == 0 || len(img) > 5<<20 {
			t.Errorf("%s: cover size %d", sp.slug, len(img))
		}
	}
}
