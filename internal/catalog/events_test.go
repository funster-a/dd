package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/funster-a/dd/internal/platform/db/dbtest"
)

// fakeStore — хранилище в памяти: «загрузка» — запись в map.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string]ObjectInfo
}

func (f *fakeStore) PresignUpload(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	return "https://storage.test/" + key + "?signature=x", nil
}

func (f *fakeStore) Stat(_ context.Context, key string) (ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.objects[key]
	if !ok {
		return ObjectInfo{}, ErrObjectNotFound
	}
	return info, nil
}

func (f *fakeStore) URL(key string) string { return "https://cdn.test/" + key }

func (f *fakeStore) put(key string, info ObjectInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.objects == nil {
		f.objects = map[string]ObjectInfo{}
	}
	f.objects[key] = info
}

type eventEnv struct {
	svc     *Service
	store   *fakeStore
	org     string
	venue   Venue
	seatMap SeatMap
}

func newEventEnv(t *testing.T) *eventEnv {
	t.Helper()
	db := dbtest.New(t)
	store := &fakeStore{}
	svc := NewService(db.Pool, WithObjectStore(store))
	ctx := t.Context()
	org := newTenant(t, svc)
	venue, err := svc.CreateVenue(ctx, org, VenueInput{Name: "Клуб"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := svc.CreateSeatMap(ctx, org, venue.ID, SeatMapInput{Name: "Основная", Layout: json.RawMessage(hallLayout)})
	if err != nil {
		t.Fatal(err)
	}
	return &eventEnv{svc: svc, store: store, org: org, venue: venue, seatMap: m}
}

func (e *eventEnv) input(slug string) EventInput {
	start := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	return EventInput{
		VenueID: e.venue.ID, SeatMapID: e.seatMap.ID, Slug: slug, Title: "Стендап вечер",
		Description: "Большой концерт", AgeRating: "18+", StartsAt: start, EndsAt: start.Add(3 * time.Hour),
	}
}

func (e *eventEnv) draft(t *testing.T) Event {
	t.Helper()
	ev, err := e.svc.CreateEvent(t.Context(), e.org, e.input("event-"+suffix()))
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

var fullPrices = []PriceInput{
	{Name: "Партер", PriceTiyn: 1_500_000, Sections: []string{"Партер"}},
	{Name: "Танцпол", PriceTiyn: 800_000, Sections: []string{"Танцпол"}},
}

// ready доводит черновик до готовности к публикации: цены и обложка.
func (e *eventEnv) ready(t *testing.T, ev Event) {
	t.Helper()
	ctx := t.Context()
	if _, err := e.svc.SetPrices(ctx, e.org, ev.ID, clonePrices(fullPrices)); err != nil {
		t.Fatal(err)
	}
	up, err := e.svc.CreateUpload(ctx, e.org, ev.ID, UploadRequest{Kind: MediaCoverImage, ContentType: "image/jpeg", Size: 1000})
	if err != nil {
		t.Fatal(err)
	}
	e.store.put(up.Key, ObjectInfo{Size: 1000, ContentType: "image/jpeg"})
	if _, err := e.svc.SetMedia(ctx, e.org, ev.ID, MediaInput{CoverImageKey: up.Key}); err != nil {
		t.Fatal(err)
	}
}

func clonePrices(in []PriceInput) []PriceInput {
	out := make([]PriceInput, len(in))
	for i, p := range in {
		p.Sections = append([]string(nil), p.Sections...)
		out[i] = p
	}
	return out
}

func TestEventDraft(t *testing.T) {
	e := newEventEnv(t)
	ctx := t.Context()

	in := e.input("standup-night")
	in.StartsAt = in.StartsAt.In(time.FixedZone("ALMT", 5*3600))
	ev, err := e.svc.CreateEvent(ctx, e.org, in)
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	if _, offset := ev.StartsAt.Zone(); ev.Status != "draft" || offset != 0 || ev.StartsAt.Location() != time.UTC || ev.MaxTicketsPerBuyer != defaultMaxTicketsPerBuyer || ev.RefundDeadlineHours != defaultRefundDeadlineHours {
		t.Fatalf("event = %+v", ev)
	}

	upd := e.input("standup-night")
	upd.Title = "Стендап: второй вечер"
	got, err := e.svc.UpdateEvent(ctx, e.org, ev.ID, upd)
	if err != nil || got.Title != upd.Title {
		t.Fatalf("UpdateEvent() = %+v, %v", got, err)
	}

	if _, err := e.svc.CreateEvent(ctx, e.org, e.input("standup-night")); !isConflict(err, "slug_taken") {
		t.Fatalf("duplicate slug: err = %v", err)
	}
	if list, err := e.svc.ListEvents(ctx, e.org); err != nil || len(list) != 1 {
		t.Fatalf("ListEvents() = %d, %v", len(list), err)
	}
}

func TestEventValidation(t *testing.T) {
	e := newEventEnv(t)
	otherVenue, err := e.svc.CreateVenue(t.Context(), e.org, VenueInput{Name: "Другой зал"})
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		mutate func(*EventInput)
		field  string
	}{
		"no title":          {func(in *EventInput) { in.Title = " " }, "title"},
		"bad slug":          {func(in *EventInput) { in.Slug = "Концерт" }, "slug"},
		"ends before start": {func(in *EventInput) { in.EndsAt = in.StartsAt }, "ends_at"},
		"bad age":           {func(in *EventInput) { in.AgeRating = "21+" }, "age_rating"},
		"sales end reversed": {func(in *EventInput) {
			s, e := in.StartsAt.Add(-time.Hour), in.StartsAt.Add(-2*time.Hour)
			in.SalesStartAt, in.SalesEndAt = &s, &e
		}, "sales_end_at"},
		"too many tickets":   {func(in *EventInput) { in.MaxTicketsPerBuyer = 51 }, "max_tickets_per_buyer"},
		"map of other venue": {func(in *EventInput) { in.VenueID = otherVenue.ID }, "seat_map_id"},
		"malformed seat map": {func(in *EventInput) { in.SeatMapID = "nope" }, "seat_map_id"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			in := e.input("event-" + suffix())
			tt.mutate(&in)
			_, err := e.svc.CreateEvent(t.Context(), e.org, in)
			if v, ok := errors.AsType[*ValidationError](err); !ok || v.Field != tt.field {
				t.Fatalf("err = %v, want validation error on %s", err, tt.field)
			}
		})
	}
}

func TestSetPrices(t *testing.T) {
	e := newEventEnv(t)
	ev := e.draft(t)
	ctx := t.Context()

	cats, err := e.svc.SetPrices(ctx, e.org, ev.ID, clonePrices(fullPrices))
	if err != nil || len(cats) != 2 {
		t.Fatalf("SetPrices() = %+v, %v", cats, err)
	}
	// Замена целиком: одна категория на оба сектора.
	cats, err = e.svc.SetPrices(ctx, e.org, ev.ID, []PriceInput{{Name: "Единый", PriceTiyn: 500_000, Sections: []string{"Партер", "Танцпол"}}})
	if err != nil || len(cats) != 1 {
		t.Fatalf("replace prices = %+v, %v", cats, err)
	}
	got, err := e.svc.GetPrices(ctx, e.org, ev.ID)
	if err != nil || len(got) != 1 || len(got[0].Sections) != 2 {
		t.Fatalf("GetPrices() = %+v, %v", got, err)
	}

	bad := map[string][]PriceInput{
		"missing section": {{Name: "A", PriceTiyn: 1, Sections: []string{"Партер"}}},
		"unknown section": {{Name: "A", PriceTiyn: 1, Sections: []string{"Партер", "Танцпол", "Балкон"}}},
		"section twice":   {{Name: "A", PriceTiyn: 1, Sections: []string{"Партер", "Танцпол"}}, {Name: "B", PriceTiyn: 1, Sections: []string{"Партер"}}},
		"negative price":  {{Name: "A", PriceTiyn: -1, Sections: []string{"Партер", "Танцпол"}}},
		"duplicate name":  {{Name: "A", PriceTiyn: 1, Sections: []string{"Партер"}}, {Name: "A", PriceTiyn: 2, Sections: []string{"Танцпол"}}},
		"no categories":   {},
	}
	for name, in := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := e.svc.SetPrices(t.Context(), e.org, ev.ID, in); !isValidation(err) {
				t.Fatalf("err = %v, want validation error", err)
			}
		})
	}
	// Неудачная замена не испортила прежние цены.
	if got, _ := e.svc.GetPrices(ctx, e.org, ev.ID); len(got) != 1 || got[0].Name != "Единый" {
		t.Fatalf("prices after failed update = %+v", got)
	}
}

func TestMediaUpload(t *testing.T) {
	e := newEventEnv(t)
	ev := e.draft(t)
	ctx := t.Context()

	badReqs := map[string]UploadRequest{
		"unknown kind":  {Kind: "poster", ContentType: "image/png", Size: 10},
		"gif as cover":  {Kind: MediaCoverImage, ContentType: "image/gif", Size: 10},
		"image too big": {Kind: MediaCoverImage, ContentType: "image/png", Size: 5<<20 + 1},
		"video too big": {Kind: MediaCoverVideo, ContentType: "video/mp4", Size: 15<<20 + 1},
	}
	for name, req := range badReqs {
		t.Run(name, func(t *testing.T) {
			if _, err := e.svc.CreateUpload(t.Context(), e.org, ev.ID, req); !isValidation(err) {
				t.Fatalf("err = %v, want validation error", err)
			}
		})
	}

	img, err := e.svc.CreateUpload(ctx, e.org, ev.ID, UploadRequest{Kind: MediaCoverImage, ContentType: "image/webp", Size: 2000})
	if err != nil || !strings.HasSuffix(img.Key, ".webp") || img.Method != http.MethodPut {
		t.Fatalf("CreateUpload() = %+v, %v", img, err)
	}

	// Файл ещё не загружен.
	if _, err := e.svc.SetMedia(ctx, e.org, ev.ID, MediaInput{CoverImageKey: img.Key}); !isValidation(err) {
		t.Fatalf("not uploaded: err = %v", err)
	}
	// Загружено не то, что обещали.
	e.store.put(img.Key, ObjectInfo{Size: 2000, ContentType: "text/html"})
	if _, err := e.svc.SetMedia(ctx, e.org, ev.ID, MediaInput{CoverImageKey: img.Key}); !isValidation(err) {
		t.Fatalf("wrong type: err = %v", err)
	}
	// Ключ другого события.
	other := e.draft(t)
	if _, err := e.svc.SetMedia(ctx, e.org, other.ID, MediaInput{CoverImageKey: img.Key}); !isValidation(err) {
		t.Fatalf("foreign key: err = %v", err)
	}

	e.store.put(img.Key, ObjectInfo{Size: 2000, ContentType: "image/webp"})
	vid, err := e.svc.CreateUpload(ctx, e.org, ev.ID, UploadRequest{Kind: MediaCoverVideo, ContentType: "video/mp4", Size: 3 << 20})
	if err != nil {
		t.Fatal(err)
	}
	e.store.put(vid.Key, ObjectInfo{Size: 3 << 20, ContentType: "video/mp4"})
	got, err := e.svc.SetMedia(ctx, e.org, ev.ID, MediaInput{CoverImageKey: img.Key, CoverVideoKey: &vid.Key})
	if err != nil || *got.CoverImageKey != img.Key || *got.CoverVideoKey != vid.Key {
		t.Fatalf("SetMedia() = %+v, %v", got, err)
	}
	// Кабинет показывает превью по публичным адресам хранилища.
	if p := e.svc.withMediaURLs(got); p.CoverImageURL != e.store.URL(img.Key) || p.CoverVideoURL != e.store.URL(vid.Key) {
		t.Errorf("media URLs = %q, %q", p.CoverImageURL, p.CoverVideoURL)
	}
}

func TestPublish(t *testing.T) {
	e := newEventEnv(t)
	ctx := t.Context()
	now := time.Now()

	ev := e.draft(t)
	if _, err := e.svc.Publish(ctx, e.org, ev.ID, now); !isPrecondition(err, "cover_required") {
		t.Fatalf("without cover: err = %v", err)
	}
	e.ready(t, ev)
	if _, err := e.svc.Publish(ctx, e.org, ev.ID, ev.StartsAt.Add(time.Minute)); !isPrecondition(err, "starts_in_past") {
		t.Fatalf("after start: err = %v", err)
	}

	res, err := e.svc.Publish(ctx, e.org, ev.ID, now)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if res.Seats != 102 || res.Event.Status != "published" || res.Event.PublishedAt == nil {
		t.Fatalf("Publish() = %+v", res)
	}

	var seated, general, gaMax int
	err = e.svc.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE kind = 'seat'), count(*) FILTER (WHERE kind = 'general'),
		       max(seat_label::int) FILTER (WHERE kind = 'general')
		FROM event_seats WHERE event_id = $1 AND status = 'available'`, ev.ID).Scan(&seated, &general, &gaMax)
	if err != nil {
		t.Fatal(err)
	}
	if seated != 2 || general != 100 || gaMax != 100 {
		t.Fatalf("seats: seated=%d general=%d max GA label=%d", seated, general, gaMax)
	}

	// После публикации схема и цены зафиксированы.
	if _, err := e.svc.Publish(ctx, e.org, ev.ID, now); !isConflict(err, "event_published") {
		t.Fatalf("republish: err = %v", err)
	}
	if _, err := e.svc.UpdateEvent(ctx, e.org, ev.ID, e.input(ev.Slug)); !isConflict(err, "event_published") {
		t.Fatalf("update published: err = %v", err)
	}
	if _, err := e.svc.SetPrices(ctx, e.org, ev.ID, clonePrices(fullPrices)); !isConflict(err, "event_published") {
		t.Fatalf("reprice published: err = %v", err)
	}
}

func TestPublishRequiresAllPrices(t *testing.T) {
	e := newEventEnv(t)
	ev := e.draft(t)
	e.ready(t, ev)
	// Меняем схему события — цены прежней схемы сбрасываются.
	m2, err := e.svc.CreateSeatMap(t.Context(), e.org, e.venue.ID, SeatMapInput{Name: "Вторая", Layout: json.RawMessage(hallLayout)})
	if err != nil {
		t.Fatal(err)
	}
	in := e.input(ev.Slug)
	in.SeatMapID = m2.ID
	if _, err := e.svc.UpdateEvent(t.Context(), e.org, ev.ID, in); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Publish(t.Context(), e.org, ev.ID, time.Now()); !isPrecondition(err, "prices_incomplete") {
		t.Fatalf("err = %v, want prices_incomplete", err)
	}
}

// TestConcurrentPublish: одновременные публикации — успешна ровно одна,
// места созданы ровно один раз.
func TestConcurrentPublish(t *testing.T) {
	e := newEventEnv(t)
	ev := e.draft(t)
	e.ready(t, ev)

	const n = 10
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ok      int
		unknown []error
	)
	start := make(chan struct{})
	for range n {
		wg.Go(func() {
			<-start
			_, err := e.svc.Publish(context.Background(), e.org, ev.ID, time.Now())
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case isConflict(err, "event_published"):
			default:
				unknown = append(unknown, err)
			}
		})
	}
	close(start)
	wg.Wait()

	if len(unknown) > 0 {
		t.Fatalf("unexpected errors: %v", unknown)
	}
	if ok != 1 {
		t.Fatalf("%d successful publishes, want 1", ok)
	}
	var seats int
	if err := e.svc.pool.QueryRow(t.Context(), `SELECT count(*) FROM event_seats WHERE event_id = $1`, ev.ID).Scan(&seats); err != nil {
		t.Fatal(err)
	}
	if seats != 102 {
		t.Fatalf("seats = %d, want 102", seats)
	}
}

// TestPublishLargeHall: стадионная схема на 50 000 мест публикуется
// за разумное время благодаря COPY.
func TestPublishLargeHall(t *testing.T) {
	if testing.Short() {
		t.Skip("large hall in -short mode")
	}
	e := newEventEnv(t)
	ctx := t.Context()

	var b strings.Builder
	b.WriteString(`{"sections":[`)
	for s := range 100 {
		if s > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name":"Сектор %d","kind":"seat","rows":[`, s+1)
		for r := range 20 {
			if r > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"label":"%d","seats":[`, r+1)
			for st := range 25 {
				if st > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"label":"%d"}`, st+1)
			}
			b.WriteString("]}")
		}
		b.WriteString("]}")
	}
	b.WriteString("]}")

	m, err := e.svc.CreateSeatMap(ctx, e.org, e.venue.ID, SeatMapInput{Name: "Стадион", Layout: json.RawMessage(b.String())})
	if err != nil {
		t.Fatal(err)
	}
	in := e.input("stadium-" + suffix())
	in.SeatMapID = m.ID
	ev, err := e.svc.CreateEvent(ctx, e.org, in)
	if err != nil {
		t.Fatal(err)
	}
	sections := make([]string, 0, 100)
	for s := range 100 {
		sections = append(sections, fmt.Sprintf("Сектор %d", s+1))
	}
	if _, err := e.svc.SetPrices(ctx, e.org, ev.ID, []PriceInput{{Name: "Стандарт", PriceTiyn: 500_000, Sections: sections}}); err != nil {
		t.Fatal(err)
	}
	up, _ := e.svc.CreateUpload(ctx, e.org, ev.ID, UploadRequest{Kind: MediaCoverImage, ContentType: "image/png", Size: 10})
	e.store.put(up.Key, ObjectInfo{Size: 10, ContentType: "image/png"})
	if _, err := e.svc.SetMedia(ctx, e.org, ev.ID, MediaInput{CoverImageKey: up.Key}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	res, err := e.svc.Publish(ctx, e.org, ev.ID, time.Now())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if res.Seats != 50_000 {
		t.Fatalf("seats = %d, want 50000", res.Seats)
	}
	t.Logf("published 50 000 seats in %v", elapsed)
	if elapsed > 15*time.Second {
		t.Errorf("publishing took %v, want under 15s", elapsed)
	}
}

func isConflict(err error, code string) bool {
	c, ok := errors.AsType[*ConflictError](err)
	return ok && c.Code == code
}

func isPrecondition(err error, code string) bool {
	p, ok := errors.AsType[*PreconditionError](err)
	return ok && p.Code == code
}

func isValidation(err error) bool {
	_, ok := errors.AsType[*ValidationError](err)
	return ok
}
