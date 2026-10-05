package catalog

import (
	"net/http"
	"strconv"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/funster-a/dd/internal/platform/httpx"
)

// SeatMapTemplate — готовая схема известной площадки (ADR 024). Организатор
// заводит по ней площадку и схему в один шаг, вместо того чтобы рисовать
// стадион на 24 тысячи мест вручную.
type SeatMapTemplate struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	VenueName string `json:"venue_name"`
	Address   string `json:"address"`
	SeatCount int    `json:"seat_count"`
	// Note — откуда взята схема и что в ней приближённо.
	Note string `json:"note"`
	// Layout и Prices есть только в ответе по одному шаблону.
	Layout *Layout `json:"layout,omitempty"`
	// Prices — пример деления на ценовые категории; цены организатор
	// задаёт сам.
	Prices []PriceInput `json:"prices,omitempty"`
}

func sectors(nums ...[2]int) []string {
	var out []string
	for _, r := range nums {
		for n := r[0]; n <= r[1]; n++ {
			out = append(out, "Сектор "+strconv.Itoa(n))
		}
	}
	return out
}

// sectorRows — одинаковый диапазон рядов в секторах first..last.
func sectorRows(first, last int, from, to string) []RowRange {
	var out []RowRange
	for _, s := range sectors([2]int{first, last}) {
		out = append(out, RowRange{Section: s, From: from, To: to})
	}
	return out
}

const almatyCentralNote = "Трибуны, нумерация секторов и вместимость — по открытым источникам. " +
	"Ряды и места в секторах приближённые: сверьте их со схемой владельца площадки перед продажей."

var seatMapTemplates = sync.OnceValue(func() []SeatMapTemplate {
	football := almatyCentralLayout()
	concert := almatyCentralConcertLayout()
	// Нижний ярус западной трибуны — 14 рядов; первые три у беговой дорожки
	// продаются дешевле, как на матчах «Кайрата» (ADR 025).
	lowRows := sectorRows(1, 9, "1", "3")
	upperRows := sectorRows(1, 9, "4", "14")
	return []SeatMapTemplate{
		{
			ID:        "almaty-central-stadium",
			Name:      "Центральный стадион Алматы — футбол",
			VenueName: "Центральный стадион",
			Address:   "Алматы, проспект Абая, 48",
			SeatCount: football.SeatCount(),
			Note:      almatyCentralNote,
			Layout:    &football,
			Prices: []PriceInput{
				{Name: "Центр западной трибуны", PriceTiyn: 15_000_00, Sections: sectors([2]int{12, 15})},
				{Name: "Западная трибуна", PriceTiyn: 8_000_00, Sections: sectors([2]int{10, 11}, [2]int{16, 17}), Rows: upperRows},
				{Name: "Восточная трибуна и углы", PriceTiyn: 5_000_00, Sections: sectors([2]int{18, 19}, [2]int{30, 48}, [2]int{59, 60})},
				{Name: "За воротами и ряды 1–3 запада", PriceTiyn: 3_000_00, Sections: sectors([2]int{20, 28}, [2]int{49, 58}), Rows: lowRows},
			},
		},
		{
			ID:        "almaty-central-stadium-concert",
			Name:      "Центральный стадион Алматы — концерт, сцена у северной трибуны",
			VenueName: "Центральный стадион",
			Address:   "Алматы, проспект Абая, 48",
			SeatCount: concert.SeatCount(),
			Note: almatyCentralNote + " Раскладка концерта — по образцу концерта Ye (Kanye West) 14–15 августа 2026: " +
				"фан-зоны Gold, Silver и Bronze на поле, сектора 21–25 за сценой не продаются. Вместимость фан-зон приближённая.",
			Layout: &concert,
			// Цены — категории того концерта: на поле 350, 260 и 180 тысяч, на
			// трибунах 280, 250, 220, 180, 150 и 120 тысяч тенге. Какие сектора
			// в какой категории, источники не публиковали: раскладка наша.
			Prices: []PriceInput{
				{Name: "Gold", PriceTiyn: 350_000_00, Sections: []string{"Gold"}},
				{Name: "Запад, центр", PriceTiyn: 280_000_00, Sections: sectors([2]int{12, 15})},
				{Name: "Silver", PriceTiyn: 260_000_00, Sections: []string{"Silver"}},
				{Name: "Запад, верхний ярус", PriceTiyn: 250_000_00, Sections: sectors([2]int{10, 11}, [2]int{16, 17})},
				{Name: "Запад, нижний ярус", PriceTiyn: 220_000_00, Rows: upperRows},
				{Name: "Bronze", PriceTiyn: 180_000_00, Sections: []string{"Bronze"}},
				{Name: "Восток, центр", PriceTiyn: 180_000_00, Sections: sectors([2]int{36, 42})},
				{Name: "Восток и север", PriceTiyn: 150_000_00, Sections: sectors([2]int{18, 20}, [2]int{26, 28}, [2]int{30, 35}, [2]int{43, 48})},
				{Name: "Юг и ряды 1–3 запада", PriceTiyn: 120_000_00, Sections: sectors([2]int{49, 60}), Rows: lowRows},
			},
		},
	}
})

// almatyCentralConcertLayout — тот же стадион с концертной раскладкой: сцена
// на поле у северной трибуны, за ней сектора 21–25 закрыты, на поле три
// фан-зоны без мест — чем ближе к сцене, тем дороже.
func almatyCentralConcertLayout() Layout {
	l := almatyCentralLayout()
	closed := map[string]bool{}
	for _, s := range sectors([2]int{21, 25}) {
		closed[s] = true
	}
	open := l.Sections[:0]
	for _, s := range l.Sections {
		if !closed[s.Name] {
			open = append(open, s)
		}
	}
	plan := *l.Plan
	f := plan.Field
	plan.Stage = &[4]float64{f[0] - 30, f[1] + 25, 36, f[3] - 50}
	zone := func(name string, x0, x1 float64, capacity int) Section {
		y0, y1 := f[1], f[1]+f[3]
		return Section{Name: name, Kind: KindGeneral, Stand: "Поле", Capacity: capacity,
			Outline: []Point{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}}
	}
	x := f[0]
	l.Plan = &plan
	open = append(open,
		zone("Gold", x, x+70, 3000),
		zone("Silver", x+70, x+170, 4000),
		zone("Bronze", x+170, x+f[2], 5000),
	)
	l.Sections = open
	return l
}

// SeatMapTemplates — список шаблонов без схем.
func SeatMapTemplates() []SeatMapTemplate {
	all := seatMapTemplates()
	out := make([]SeatMapTemplate, len(all))
	for i, t := range all {
		t.Layout, t.Prices = nil, nil
		out[i] = t
	}
	return out
}

// GetSeatMapTemplate — шаблон со схемой и примером цен.
func GetSeatMapTemplate(id string) (SeatMapTemplate, error) {
	for _, t := range seatMapTemplates() {
		if t.ID == id {
			return t, nil
		}
	}
	return SeatMapTemplate{}, ErrNotFound
}

func (s *Service) handleListTemplates(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"templates": SeatMapTemplates()})
}

func (s *Service) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := GetSeatMapTemplate(chi.URLParam(r, "templateID"))
	if writeError(w, r, err) {
		return
	}
	// Шаблоны вшиты в бинарь и меняются только с выпуском.
	w.Header().Set("Cache-Control", "private, max-age=3600")
	httpx.WriteJSON(w, http.StatusOK, t)
}
