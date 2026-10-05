package catalog

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestAlmatyCentralLayout(t *testing.T) {
	tpl, err := GetSeatMapTemplate("almaty-central-stadium")
	if err != nil {
		t.Fatal(err)
	}
	l := *tpl.Layout
	if got := l.SeatCount(); got != almatyCentralSeats {
		t.Errorf("SeatCount() = %d, want %d", got, almatyCentralSeats)
	}
	// Схема проходит ту же проверку, что схема от организатора.
	raw, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseLayout(raw); err != nil {
		t.Fatalf("ParseLayout(template) error = %v", err)
	}
	if len(l.Sections) != 59 {
		t.Errorf("sections = %d, want 59 (1-28, 30-60)", len(l.Sections))
	}
	stands := map[string][]string{}
	for _, s := range l.Sections {
		stands[s.Stand] = append(stands[s.Stand], s.Name)
		if s.Name == "Сектор 29" {
			t.Error("sector 29 does not exist at the stadium")
		}
	}
	for stand, n := range map[string]int{"Западная трибуна": 17, "Северная трибуна": 11, "Восточная трибуна": 19, "Южная трибуна": 12} {
		if len(stands[stand]) != n {
			t.Errorf("%s: %d sectors, want %d", stand, len(stands[stand]), n)
		}
	}
	if !slices.Contains(stands["Западная трибуна"], "Сектор 12") || !slices.Contains(stands["Южная трибуна"], "Сектор 60") {
		t.Errorf("sector numbering is off: %v", stands)
	}

	// Пример цен покрывает каждый сектор ровно один раз — его можно
	// отправить в SetPrices как есть.
	if err := validatePrices(tpl.Prices, l); err != nil {
		t.Errorf("template prices: %v", err)
	}
}

func TestAlmatyCentralRowsGrowOutward(t *testing.T) {
	for _, s := range almatyCentralLayout().Sections {
		first, last := len(s.Rows[0].Seats), len(s.Rows[len(s.Rows)-1].Seats)
		// В закруглённых трибунах ряды длиннее с удалением от поля; на
		// прямых — почти равны (последний ряд восточной трибуны короче на
		// подгонку суммы).
		if last < first-2 {
			t.Errorf("%s: first row %d seats, last row %d", s.Name, first, last)
		}
		if first < 8 || last > 60 {
			t.Errorf("%s: implausible row sizes %d..%d", s.Name, first, last)
		}
	}
}

func TestAlmatyCentralConcert(t *testing.T) {
	tpl, err := GetSeatMapTemplate("almaty-central-stadium-concert")
	if err != nil {
		t.Fatal(err)
	}
	l := *tpl.Layout
	raw, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseLayout(raw); err != nil {
		t.Fatalf("ParseLayout(concert) error = %v", err)
	}
	if l.Plan.Stage == nil {
		t.Error("concert plan has no stage")
	}
	names := map[string]Section{}
	for _, s := range l.Sections {
		names[s.Name] = s
	}
	for _, closed := range []string{"Сектор 21", "Сектор 23", "Сектор 25"} {
		if _, ok := names[closed]; ok {
			t.Errorf("%s behind the stage is on sale", closed)
		}
	}
	zones := 0
	for _, z := range []string{"Gold", "Silver", "Bronze"} {
		s, ok := names[z]
		if !ok || s.Kind != KindGeneral || s.Stand != "Поле" {
			t.Errorf("fan zone %s = %+v", z, s)
		}
		zones += s.Capacity
	}
	football := almatyCentralLayout()
	closedSeats := 0
	for _, s := range football.Sections {
		if _, ok := names[s.Name]; !ok {
			for _, r := range s.Rows {
				closedSeats += len(r.Seats)
			}
		}
	}
	if got, want := l.SeatCount(), almatyCentralSeats-closedSeats+zones; got != want {
		t.Errorf("SeatCount() = %d, want %d", got, want)
	}
	if err := validatePrices(tpl.Prices, l); err != nil {
		t.Errorf("concert prices: %v", err)
	}
	// Первые три ряда нижнего яруса — в самой дешёвой категории.
	cheapest := tpl.Prices[len(tpl.Prices)-1]
	if cheapest.PriceTiyn != 120_000_00 || len(cheapest.Rows) != 9 || cheapest.Rows[0] != (RowRange{Section: "Сектор 1", From: "1", To: "3"}) {
		t.Errorf("cheapest category = %+v", cheapest)
	}
}
