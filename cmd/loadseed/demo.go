package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/funster-a/dd/internal/catalog"
	"github.com/funster-a/dd/internal/platform/config"
	"github.com/funster-a/dd/internal/platform/storage"
)

// Демо-данные для показа проекта: организатор и три опубликованных события,
// созданные тем же кодом каталога, что и кабинет, с настоящими обложками в
// хранилище. Повторный запуск ничего не дублирует: готовые события
// пропускаются, недоделанные доводятся до публикации.
//
//	docker compose run --rm demo

const (
	demoSlug  = "demo"
	demoEmail = "demo@example.com"
)

// demoFresh — сколько до начала должно оставаться у события, чтобы его можно
// было показать целиком: возврат закрывается за 24 часа до начала.
const demoFresh = 24 * time.Hour

// demoUpload загружает файл по ссылке из CreateUpload — так же, как браузер
// организатора.
type demoUpload func(ctx context.Context, t catalog.UploadTarget, contentType string, body []byte) error

type demoSpec struct {
	slug, title, description, age string
	venue, address, seatMap       string
	layout                        catalog.Layout
	prices                        []catalog.PriceInput
	startsIn                      time.Duration // от полуночи сегодняшнего дня по Алматы
	maxTickets                    int32
	cover                         [2]color.RGBA
}

// demoEvent — итог по событию для подсказки после запуска.
type demoEvent struct {
	Title    string
	Path     string
	Status   string
	Free     bool
	StartsAt time.Time
}

// demoStale — события, которые показать уже нельзя: отменены, прошли или
// начнутся меньше чем через сутки. Опубликованное событие не переносится,
// поэтому лечится только пересозданием данных.
func demoStale(events []demoEvent, now time.Time) []demoEvent {
	var out []demoEvent
	for _, e := range events {
		if e.Status != "published" || e.StartsAt.Sub(now) < demoFresh {
			out = append(out, e)
		}
	}
	return out
}

func demoSpecs() ([]demoSpec, error) {
	tpl, err := catalog.GetSeatMapTemplate("almaty-central-stadium-concert")
	if err != nil {
		return nil, err
	}
	hall := catalog.Layout{Sections: []catalog.Section{
		{Name: "Партер", Kind: catalog.KindSeat, Rows: demoRows(8, 12)},
		{Name: "Балкон", Kind: catalog.KindSeat, Rows: demoRows(3, 10)},
	}}
	open := catalog.Layout{Sections: []catalog.Section{{Name: "Свободная рассадка", Kind: catalog.KindGeneral, Capacity: 120}}}
	const day = 24 * time.Hour
	return []demoSpec{
		{
			slug: "standup", title: "Стендап-вечер", age: "16+",
			description: "Демо-событие в камерном зале: места с рядами, цена зависит от ряда.",
			venue:       "Камерный зал «Партер»", address: "Алматы", seatMap: "Камерный зал, 126 мест", layout: hall,
			prices: []catalog.PriceInput{
				{Name: "Первые ряды", PriceTiyn: 800000, Rows: []catalog.RowRange{{Section: "Партер", From: "1", To: "3"}}},
				{Name: "Партер", PriceTiyn: 500000, Rows: []catalog.RowRange{{Section: "Партер", From: "4", To: "8"}}},
				{Name: "Балкон", PriceTiyn: 300000, Sections: []string{"Балкон"}},
			},
			startsIn: 14*day + 19*time.Hour, maxTickets: 6,
			cover: [2]color.RGBA{{0x3b, 0x1f, 0x5c, 0xff}, {0xeb, 0x68, 0x34, 0xff}},
		},
		{
			slug: "stadium-concert", title: "Концерт на Центральном стадионе", age: "12+",
			description: "Демо-событие на большой площадке: план стадиона с секторами и фан-зонами, 33 509 мест.",
			venue:       tpl.VenueName, address: tpl.Address, seatMap: tpl.Name, layout: *tpl.Layout, prices: tpl.Prices,
			startsIn: 30*day + 20*time.Hour, maxTickets: 4,
			cover: [2]color.RGBA{{0x0b, 0x2a, 0x4a, 0xff}, {0x2a, 0x78, 0xd6, 0xff}},
		},
		{
			slug: "open-lecture", title: "Открытая лекция", age: "0+",
			description: "Демо-событие с бесплатными билетами: заказ оформляется сразу, без оплаты.",
			venue:       "Камерный зал «Партер»", address: "Алматы", seatMap: "Свободная рассадка, 120 мест", layout: open,
			prices:   []catalog.PriceInput{{Name: "Бесплатно", PriceTiyn: 0, Sections: []string{"Свободная рассадка"}}},
			startsIn: 7*day + 18*time.Hour + 30*time.Minute, maxTickets: 2,
			cover: [2]color.RGBA{{0x0f, 0x4d, 0x3a, 0xff}, {0x1b, 0xaf, 0x7a, 0xff}},
		},
	}, nil
}

func demoRows(rows, perRow int) []catalog.Row {
	out := make([]catalog.Row, rows)
	for r := range rows {
		seats := make([]catalog.Seat, perRow)
		for s := range perRow {
			seats[s] = catalog.Seat{Label: fmt.Sprint(s + 1)}
		}
		out[r] = catalog.Row{Label: fmt.Sprint(r + 1), Seats: seats}
	}
	return out
}

// seedDemo создаёт или достраивает демо-данные на момент now. Ошибка одного
// события не мешает остальным: они возвращаются вместе с общей ошибкой.
func seedDemo(ctx context.Context, cat *catalog.Service, upload demoUpload, now time.Time) (catalog.Organizer, []demoEvent, error) {
	specs, err := demoSpecs()
	if err != nil {
		return catalog.Organizer{}, nil, err
	}
	// База часовых поясов уже в бинаре: её подключает каталог (time/tzdata).
	almaty, err := time.LoadLocation("Asia/Almaty")
	if err != nil {
		return catalog.Organizer{}, nil, err
	}
	org, err := demoOrganizer(ctx, cat)
	if err != nil {
		return org, nil, err
	}
	existing, err := cat.ListEvents(ctx, org.ID)
	if err != nil {
		return org, nil, err
	}
	bySlug := make(map[string]catalog.Event, len(existing))
	for _, e := range existing {
		bySlug[e.Slug] = e
	}
	local := now.In(almaty)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, almaty)
	var out []demoEvent
	var errs []error
	for _, sp := range specs {
		ev, ok := bySlug[sp.slug]
		if !ok || ev.Status == "draft" {
			published, err := demoPublish(ctx, cat, upload, org.ID, sp, ev, ok, midnight.Add(sp.startsIn), now)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", sp.slug, err))
				continue
			}
			ev = published
		}
		out = append(out, demoEvent{Title: sp.title, Path: "/e/" + org.Slug + "/" + sp.slug, Status: ev.Status, Free: sp.prices[0].PriceTiyn == 0, StartsAt: ev.StartsAt})
	}
	return org, out, errors.Join(errs...)
}

func demoOrganizer(ctx context.Context, cat *catalog.Service) (catalog.Organizer, error) {
	o, err := cat.OrganizerBySlug(ctx, demoSlug)
	if !errors.Is(err, catalog.ErrNotFound) {
		return o, err
	}
	return cat.CreateOrganizer(ctx, catalog.NewOrganizer{Name: "Демо-организатор", Slug: demoSlug, OwnerEmail: demoEmail})
}

// demoPublish доводит событие до публикации: площадка, схема, черновик, цены,
// обложка. Площадка и схема ищутся по названию, чтобы повтор их не дублировал.
// Черновик прерванного запуска получает свежие даты: прежние могли пройти.
// Если обложка загрузилась, а прикрепить её не вышло, файл остаётся в
// хранилище без ссылки: для демо-данных это безвредно.
func demoPublish(ctx context.Context, cat *catalog.Service, upload demoUpload, orgID string, sp demoSpec, ev catalog.Event, exists bool, starts, now time.Time) (catalog.Event, error) {
	refund := int32(24)
	in := catalog.EventInput{
		Admission: "ticketed", Slug: sp.slug, Title: sp.title,
		Description: sp.description, AgeRating: sp.age, StartsAt: starts.UTC(), EndsAt: starts.Add(2 * time.Hour).UTC(),
		MaxTicketsPerBuyer: sp.maxTickets, RefundDeadlineHours: &refund,
	}
	if exists {
		if ev.SeatMapID == nil {
			return ev, fmt.Errorf("draft %s has no seat map; reset the demo data: docker compose down -v", sp.slug)
		}
		in.VenueID, in.SeatMapID = ev.VenueID, *ev.SeatMapID
		updated, err := cat.UpdateEvent(ctx, orgID, ev.ID, in)
		if err != nil {
			return ev, err
		}
		ev = updated
	} else {
		venue, err := demoVenue(ctx, cat, orgID, sp)
		if err != nil {
			return ev, err
		}
		sm, err := demoSeatMap(ctx, cat, orgID, venue.ID, sp)
		if err != nil {
			return ev, err
		}
		in.VenueID, in.SeatMapID = venue.ID, sm.ID
		if ev, err = cat.CreateEvent(ctx, orgID, in); err != nil {
			return ev, err
		}
	}
	if _, err := cat.SetPrices(ctx, orgID, ev.ID, sp.prices); err != nil {
		return ev, err
	}
	if ev.CoverImageKey == nil {
		img, err := demoCover(sp.cover)
		if err != nil {
			return ev, err
		}
		t, err := cat.CreateUpload(ctx, orgID, ev.ID, catalog.UploadRequest{Kind: catalog.MediaCoverImage, ContentType: "image/png", Size: int64(len(img))})
		if err != nil {
			return ev, err
		}
		if err := upload(ctx, t, "image/png", img); err != nil {
			return ev, err
		}
		if _, err := cat.SetMedia(ctx, orgID, ev.ID, catalog.MediaInput{CoverImageKey: t.Key}); err != nil {
			return ev, err
		}
	}
	res, err := cat.Publish(ctx, orgID, ev.ID, now)
	if err != nil {
		return ev, err
	}
	return res.Event, nil
}

func demoVenue(ctx context.Context, cat *catalog.Service, orgID string, sp demoSpec) (catalog.Venue, error) {
	venues, err := cat.ListVenues(ctx, orgID)
	if err != nil {
		return catalog.Venue{}, err
	}
	for _, v := range venues {
		if v.Name == sp.venue {
			return v, nil
		}
	}
	return cat.CreateVenue(ctx, orgID, catalog.VenueInput{Name: sp.venue, Address: sp.address, Timezone: "Asia/Almaty"})
}

func demoSeatMap(ctx context.Context, cat *catalog.Service, orgID, venueID string, sp demoSpec) (catalog.SeatMap, error) {
	maps, err := cat.ListSeatMaps(ctx, orgID, venueID)
	if err != nil {
		return catalog.SeatMap{}, err
	}
	for _, m := range maps {
		if m.Name == sp.seatMap {
			return m, nil
		}
	}
	raw, err := json.Marshal(sp.layout)
	if err != nil {
		return catalog.SeatMap{}, err
	}
	return cat.CreateSeatMap(ctx, orgID, venueID, catalog.SeatMapInput{Name: sp.seatMap, Layout: raw})
}

// demoCover рисует обложку 1200×630: градиент и полукруг сцены над рядами.
func demoCover(c [2]color.RGBA) ([]byte, error) {
	const w, h = 1200, 630
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		t := float64(y) / float64(h-1)
		mix := func(a, b uint8) uint8 { return uint8(float64(a)*(1-t) + float64(b)*t) }
		row := color.RGBA{mix(c[0].R, c[1].R), mix(c[0].G, c[1].G), mix(c[0].B, c[1].B), 0xff}
		for x := range w {
			img.SetRGBA(x, y, row)
		}
	}
	light := color.RGBA{0xff, 0xff, 0xff, 0xff}
	// Сцена.
	for y := 60; y < 200; y++ {
		for x := 360; x < 840; x++ {
			dx, dy := float64(x-600)/240, float64(y-200)/140
			if dx*dx+dy*dy <= 1 {
				img.SetRGBA(x, y, light)
			}
		}
	}
	// Ряды мест.
	for r := range 6 {
		y := 270 + r*56
		for s := range 14 + 2*r {
			x := 600 + (s-(14+2*r)/2)*48 + 24
			for dy := -9; dy <= 9; dy++ {
				for dx := -9; dx <= 9; dx++ {
					if dx*dx+dy*dy <= 81 && image.Pt(x+dx, y+dy).In(img.Rect) {
						img.SetRGBA(x+dx, y+dy, light)
					}
				}
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// demo — команда для docker compose run: подключает каталог к хранилищу и
// печатает, куда идти дальше.
func demo(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) error {
	store, err := storage.New(storage.Config{
		Endpoint: cfg.S3.Endpoint, PublicEndpoint: cfg.S3.PublicEndpoint,
		AccessKey: cfg.S3.AccessKey, SecretKey: cfg.S3.SecretKey, Bucket: cfg.S3.Bucket, Region: cfg.S3.Region,
	})
	if err != nil {
		return err
	}
	cat := catalog.NewService(pool, catalog.WithObjectStore(demoStore{c: store}))
	almaty, err := time.LoadLocation("Asia/Almaty")
	if err != nil {
		return err
	}
	now := time.Now()
	org, events, err := seedDemo(ctx, cat, httpUpload, now)
	if err != nil {
		return err
	}
	if stale := demoStale(events, now); len(stale) > 0 {
		fmt.Println("Демо-данные устарели:")
		for _, e := range stale {
			reason := "начинается меньше чем через сутки"
			switch {
			case e.Status == "cancelled":
				reason = "отменено"
			case e.Status != "published":
				reason = "не опубликовано"
			case !e.StartsAt.After(now):
				reason = "уже прошло"
			}
			fmt.Printf("  %s, %s — %s\n", e.Title, e.StartsAt.In(almaty).Format("02.01.2006 15:04"), reason)
		}
		// Каждая команда на своей строке: && не работает в старом PowerShell.
		fmt.Println("\nЗаведите их заново, по одной команде:")
		fmt.Println("  docker compose down -v")
		fmt.Println("  docker compose up -d --wait")
		fmt.Println("  docker compose run --rm demo")
		return errors.New("demo data is stale")
	}
	fmt.Printf("Демо-данные готовы. Сайт: %s\n\n", cfg.PublicBaseURL)
	for _, e := range events {
		note := ""
		if e.Free {
			note = " (бесплатно, без оплаты)"
		}
		fmt.Printf("  %s%s, %s\n    %s%s\n", e.Title, note, e.StartsAt.In(almaty).Format("02.01.2006"), cfg.PublicBaseURL, e.Path)
	}
	fmt.Printf("\nКабинет организатора: %s/org, вход по %s\n", cfg.PublicBaseURL, org.OwnerEmail)
	fmt.Println("Покупатель входит по любому номеру телефона.")
	fmt.Println("Коды входа не отправляются, а пишутся в лог api: docker compose logs api | grep one-time")
	return nil
}

// uploadClient — с таймаутом: зависшее хранилище не должно держать команду
// до общего дедлайна в десять минут.
var uploadClient = &http.Client{Timeout: 30 * time.Second}

func httpUpload(ctx context.Context, t catalog.UploadTarget, contentType string, body []byte) error {
	method := t.Method
	if method == "" {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(ctx, method, t.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := uploadClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("upload cover: %s: %s", resp.Status, msg)
	}
	return nil
}

// demoStore — клиент хранилища в интерфейсе каталога, как в cmd/api.
type demoStore struct{ c *storage.Client }

func (s demoStore) PresignUpload(ctx context.Context, key, _ string, ttl time.Duration) (string, error) {
	return s.c.PresignUpload(ctx, key, ttl)
}

func (s demoStore) URL(key string) string { return s.c.URL(key) }

func (s demoStore) Stat(ctx context.Context, key string) (catalog.ObjectInfo, error) {
	info, err := s.c.Stat(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return catalog.ObjectInfo{}, catalog.ErrObjectNotFound
	}
	if err != nil {
		return catalog.ObjectInfo{}, err
	}
	return catalog.ObjectInfo{Size: info.Size, ContentType: info.ContentType}, nil
}
