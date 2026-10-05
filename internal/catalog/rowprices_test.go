package catalog

import (
	"encoding/json"
	"testing"
	"time"
)

// Сектор на четыре ряда по два места и входная зона.
const rowsLayout = `{"sections":[
	{"name":"Сектор 1","kind":"seat","rows":[
		{"label":"1","seats":[{"label":"1"},{"label":"2"}]},
		{"label":"2","seats":[{"label":"1"},{"label":"2"}]},
		{"label":"3","seats":[{"label":"1"},{"label":"2"}]},
		{"label":"4","seats":[{"label":"1"},{"label":"2"}]}]},
	{"name":"Сектор 2","kind":"seat","rows":[{"label":"1","seats":[{"label":"1"}]}]},
	{"name":"Танцпол","kind":"general","capacity":3}
]}`

func TestValidateRowPrices(t *testing.T) {
	l, err := ParseLayout(json.RawMessage(rowsLayout))
	if err != nil {
		t.Fatal(err)
	}
	ok := []PriceInput{
		{Name: "У дорожки", PriceTiyn: 100, Rows: []RowRange{{Section: "Сектор 1", From: "1", To: "2"}}},
		{Name: "Сектор", PriceTiyn: 200, Sections: []string{"Сектор 2"}, Rows: []RowRange{{Section: " Сектор 1 ", From: "3", To: "4"}}},
		{Name: "Танцпол", PriceTiyn: 300, Sections: []string{"Танцпол"}},
	}
	if err := validatePrices(ok, l); err != nil {
		t.Fatalf("valid row prices: %v", err)
	}
	if ok[1].Rows[0].Section != "Сектор 1" {
		t.Errorf("section is not trimmed: %q", ok[1].Rows[0].Section)
	}

	rest := PriceInput{Name: "Остальное", PriceTiyn: 1, Sections: []string{"Сектор 2", "Танцпол"}}
	rows := func(rr ...RowRange) []RowRange { return rr }
	r := func(from, to string) RowRange { return RowRange{Section: "Сектор 1", From: from, To: to} }
	bad := map[string][]PriceInput{
		"row not covered":  {{Name: "A", PriceTiyn: 1, Rows: rows(r("1", "3"))}, rest},
		"rows overlap":     {{Name: "A", PriceTiyn: 1, Rows: rows(r("1", "3"), r("3", "4"))}, rest},
		"reversed range":   {{Name: "A", PriceTiyn: 1, Rows: rows(r("4", "1"))}, rest},
		"unknown row":      {{Name: "A", PriceTiyn: 1, Rows: rows(r("1", "9"))}, rest},
		"unknown section":  {{Name: "A", PriceTiyn: 1, Sections: []string{"Сектор 1"}, Rows: rows(RowRange{Section: "Балкон", From: "1", To: "1"})}, rest},
		"whole and rows":   {{Name: "A", PriceTiyn: 1, Sections: []string{"Сектор 1"}, Rows: rows(r("1", "1"))}, rest},
		"nothing assigned": {{Name: "A", PriceTiyn: 1}, {Name: "B", PriceTiyn: 1, Sections: []string{"Сектор 1", "Сектор 2", "Танцпол"}}},
		"rows of general zone": {
			{Name: "A", PriceTiyn: 1, Sections: []string{"Сектор 1", "Сектор 2"}, Rows: rows(RowRange{Section: "Танцпол", From: "1", To: "1"})},
		},
	}
	for name, in := range bad {
		t.Run(name, func(t *testing.T) {
			if err := validatePrices(in, l); !isValidation(err) {
				t.Fatalf("err = %v, want validation error", err)
			}
		})
	}
}

func TestPublishRowPrices(t *testing.T) {
	e := newEventEnv(t)
	ctx := t.Context()
	m, err := e.svc.CreateSeatMap(ctx, e.org, e.venue.ID, SeatMapInput{Name: "По рядам", Layout: json.RawMessage(rowsLayout)})
	if err != nil {
		t.Fatal(err)
	}
	in := e.input("rows-" + suffix())
	in.SeatMapID = m.ID
	ev, err := e.svc.CreateEvent(ctx, e.org, in)
	if err != nil {
		t.Fatal(err)
	}
	cats, err := e.svc.SetPrices(ctx, e.org, ev.ID, []PriceInput{
		{Name: "У дорожки", PriceTiyn: 100, Rows: []RowRange{{Section: "Сектор 1", From: "1", To: "1"}}},
		{Name: "Сектор", PriceTiyn: 200, Sections: []string{"Сектор 2", "Танцпол"}, Rows: []RowRange{{Section: "Сектор 1", From: "2", To: "4"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.GetPrices(ctx, e.org, ev.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("GetPrices() = %+v, %v", got, err)
	}
	// Дороже — первой; у каждой категории свои ряды.
	if got[0].Name != "Сектор" || len(got[0].Rows) != 1 || got[0].Rows[0] != (RowRange{Section: "Сектор 1", From: "2", To: "4"}) {
		t.Errorf("rows of %q = %+v", got[0].Name, got[0].Rows)
	}
	if got[1].Name != "У дорожки" || len(got[1].Sections) != 0 || len(got[1].Rows) != 1 {
		t.Errorf("category %+v", got[1])
	}

	up, err := e.svc.CreateUpload(ctx, e.org, ev.ID, UploadRequest{Kind: MediaCoverImage, ContentType: "image/jpeg", Size: 1000})
	if err != nil {
		t.Fatal(err)
	}
	e.store.put(up.Key, ObjectInfo{Size: 1000, ContentType: "image/jpeg"})
	if _, err := e.svc.SetMedia(ctx, e.org, ev.ID, MediaInput{CoverImageKey: up.Key}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Publish(ctx, e.org, ev.ID, time.Now()); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	cheap := cats[0].ID
	if cats[0].Name != "У дорожки" {
		cheap = cats[1].ID
	}
	rows, err := e.svc.pool.Query(ctx, `SELECT coalesce(row_label, ''), count(*) FILTER (WHERE price_category_id = $2), count(*)
		FROM event_seats WHERE event_id = $1 AND section = 'Сектор 1' GROUP BY 1 ORDER BY 1`, ev.ID, cheap)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var row string
		var nCheap, n int
		if err := rows.Scan(&row, &nCheap, &n); err != nil {
			t.Fatal(err)
		}
		want := 0
		if row == "1" {
			want = n
		}
		if nCheap != want {
			t.Errorf("row %s: %d of %d seats in the cheap category, want %d", row, nCheap, n, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
