package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"github.com/funster-a/dd/internal/catalog"
	"github.com/funster-a/dd/internal/identity"
)

// seedBuyers создаёт покупателей с сессиями. Телефоны из диапазона +7 799…,
// которого нет у операторов. Сессии живут 30 дней — на всю серию прогонов.
func seedBuyers(ctx context.Context, pool *pgxpool.Pool, rdb goredis.Cmdable, args []string) error {
	fs := flag.NewFlagSet("buyers", flag.ContinueOnError)
	n := fs.Int("n", 5000, "сколько покупателей")
	out := fs.String("out", "buyers.json", "куда записать токены")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ident := identity.NewService(pool, rdb, identity.LogSender{}, nil, nil)
	tokens := make([]string, *n)
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	next := make(chan int)
	for range 32 {
		wg.Go(func() {
			for i := range next {
				t, err := ident.IssueBuyerSession(ctx, fmt.Sprintf("+7799%07d", i))
				if err != nil {
					select {
					case errs <- err:
					default:
					}
					return
				}
				tokens[i] = t
			}
		})
	}
	for i := range *n {
		next <- i
	}
	close(next)
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
	}
	return writeJSON(*out, map[string]any{"tokens": tokens})
}

// Seat — место для сценариев k6.
type Seat struct {
	Section string `json:"section"`
	Row     string `json:"row"`
	Seat    string `json:"seat"`
}

// seedEvent создаёт организатора, площадку, зал rows × seats одним сектором
// и публикует событие с открытыми продажами.
func seedEvent(ctx context.Context, pool *pgxpool.Pool, args []string) error {
	fs := flag.NewFlagSet("event", flag.ContinueOnError)
	rows := fs.Int("rows", 25, "рядов")
	perRow := fs.Int("seats", 40, "мест в ряду")
	out := fs.String("out", "event.json", "куда записать событие")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cat := catalog.NewService(pool)
	suffix := strings.ToLower(rand.Text()[:8])
	org, err := cat.CreateOrganizer(ctx, catalog.NewOrganizer{Name: "Load test", Slug: "load-" + suffix, OwnerEmail: "load-" + suffix + "@example.com"})
	if err != nil {
		return err
	}
	venue, err := cat.CreateVenue(ctx, org.ID, catalog.VenueInput{Name: "Зал", Timezone: "Asia/Almaty"})
	if err != nil {
		return err
	}
	layout := map[string]any{"sections": []any{map[string]any{"name": "Партер", "kind": "seat", "rows": hallRows(*rows, *perRow)}}}
	raw, err := json.Marshal(layout)
	if err != nil {
		return err
	}
	sm, err := cat.CreateSeatMap(ctx, org.ID, venue.ID, catalog.SeatMapInput{Name: "Зал", Layout: raw})
	if err != nil {
		return err
	}
	starts := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Minute)
	refund := int32(24)
	ev, err := cat.CreateEvent(ctx, org.ID, catalog.EventInput{
		VenueID: venue.ID, Admission: "ticketed", SeatMapID: sm.ID, Slug: "load", Title: "Нагрузочный прогон",
		AgeRating: "0+", StartsAt: starts, EndsAt: starts.Add(2 * time.Hour), MaxTicketsPerBuyer: 6, RefundDeadlineHours: &refund,
	})
	if err != nil {
		return err
	}
	if _, err := cat.SetPrices(ctx, org.ID, ev.ID, []catalog.PriceInput{{Name: "Партер", PriceTiyn: 500000, Sections: []string{"Партер"}}}); err != nil {
		return err
	}
	// Обложка для публикации обязательна; файл для прогона не нужен.
	if _, err := pool.Exec(ctx, `UPDATE events SET cover_image_key = 'loadtest/cover.jpg' WHERE id = $1`, ev.ID); err != nil {
		return err
	}
	res, err := cat.Publish(ctx, org.ID, ev.ID, time.Now())
	if err != nil {
		return err
	}
	return writeJSON(*out, map[string]any{
		"event_id": ev.ID, "seats_total": res.Seats, "rows": *rows, "seats_per_row": *perRow,
		"hot_seat": Seat{Section: "Партер", Row: "1", Seat: "1"},
	})
}

func hallRows(rows, perRow int) []any {
	out := make([]any, rows)
	for r := range rows {
		seats := make([]any, perRow)
		for s := range perRow {
			seats[s] = map[string]string{"label": strconv.Itoa(s + 1)}
		}
		out[r] = map[string]any{"label": strconv.Itoa(r + 1), "seats": seats}
	}
	return out
}
