package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/funster-a/dd/internal/catalog"
)

// seedStadium публикует событие на Центральном стадионе Алматы по шаблону
// (ADR 024, 025): футбол — 59 секторов и 23 804 места, концерт — фан-зоны
// на поле и сектора без закрытых за сценой. Цены — пример из шаблона. Для
// просмотра плана в браузере и для штурма стадиона.
func seedStadium(ctx context.Context, pool *pgxpool.Pool, args []string) error {
	fs := flag.NewFlagSet("stadium", flag.ContinueOnError)
	out := fs.String("out", "stadium.json", "куда записать событие")
	kind := fs.String("template", "football", "football или concert")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id, title, slug, maxTickets := "almaty-central-stadium", "Демо-матч на Центральном стадионе", "match", int32(6)
	if *kind == "concert" {
		// Как на концерте Ye в Алматы: не больше четырёх билетов в одни руки.
		id, title, slug, maxTickets = "almaty-central-stadium-concert", "Демо-концерт на Центральном стадионе", "concert", 4
	}
	tpl, err := catalog.GetSeatMapTemplate(id)
	if err != nil {
		return err
	}
	cat := catalog.NewService(pool)
	suffix := strings.ToLower(rand.Text()[:8])
	org, err := cat.CreateOrganizer(ctx, catalog.NewOrganizer{Name: "Демо-стадион", Slug: "stadium-" + suffix, OwnerEmail: "stadium-" + suffix + "@example.com"})
	if err != nil {
		return err
	}
	venue, err := cat.CreateVenue(ctx, org.ID, catalog.VenueInput{Name: tpl.VenueName, Address: tpl.Address, Timezone: "Asia/Almaty"})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(tpl.Layout)
	if err != nil {
		return err
	}
	sm, err := cat.CreateSeatMap(ctx, org.ID, venue.ID, catalog.SeatMapInput{Name: tpl.Name, Layout: raw})
	if err != nil {
		return err
	}
	starts := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Minute)
	refund := int32(24)
	ev, err := cat.CreateEvent(ctx, org.ID, catalog.EventInput{
		VenueID: venue.ID, Admission: "ticketed", SeatMapID: sm.ID, Slug: slug, Title: title,
		AgeRating: "0+", StartsAt: starts, EndsAt: starts.Add(2 * time.Hour), MaxTicketsPerBuyer: maxTickets, RefundDeadlineHours: &refund,
	})
	if err != nil {
		return err
	}
	if _, err := cat.SetPrices(ctx, org.ID, ev.ID, tpl.Prices); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `UPDATE events SET cover_image_key = 'loadtest/cover.jpg' WHERE id = $1`, ev.ID); err != nil {
		return err
	}
	res, err := cat.Publish(ctx, org.ID, ev.ID, time.Now())
	if err != nil {
		return err
	}
	type sector struct {
		Name string `json:"name"`
		Rows []int  `json:"rows"` // мест в каждом ряду
	}
	sectors := make([]sector, 0, len(tpl.Layout.Sections))
	for _, s := range tpl.Layout.Sections {
		if s.Kind != catalog.KindSeat {
			continue
		}
		sec := sector{Name: s.Name}
		for _, r := range s.Rows {
			sec.Rows = append(sec.Rows, len(r.Seats))
		}
		sectors = append(sectors, sec)
	}
	return writeJSON(*out, map[string]any{
		"event_id": ev.ID, "seats_total": res.Seats, "path": "/e/" + org.Slug + "/" + slug, "sectors": sectors,
	})
}
