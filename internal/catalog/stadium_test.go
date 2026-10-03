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
