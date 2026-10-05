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

var seatMapTemplates = sync.OnceValue(func() []SeatMapTemplate {
	l := almatyCentralLayout()
	sectors := func(nums ...[2]int) []string {
		var out []string
		for _, r := range nums {
			for n := r[0]; n <= r[1]; n++ {
				out = append(out, "Сектор "+strconv.Itoa(n))
			}
		}
		return out
	}
	return []SeatMapTemplate{{
		ID:        "almaty-central-stadium",
		Name:      "Центральный стадион Алматы — футбол",
		VenueName: "Центральный стадион",
		Address:   "Алматы, проспект Абая, 48",
		SeatCount: l.SeatCount(),
		Note: "Трибуны, нумерация секторов и вместимость — по открытым источникам. " +
			"Ряды и места в секторах приближённые: сверьте их со схемой владельца площадки перед продажей.",
		Layout: &l,
		Prices: []PriceInput{
			{Name: "Центр западной трибуны", PriceTiyn: 15_000_00, Sections: sectors([2]int{12, 15})},
			{Name: "Западная трибуна", PriceTiyn: 8_000_00, Sections: sectors([2]int{1, 11}, [2]int{16, 17})},
			{Name: "Восточная трибуна и углы", PriceTiyn: 5_000_00, Sections: sectors([2]int{18, 19}, [2]int{30, 48}, [2]int{59, 60})},
			{Name: "За воротами", PriceTiyn: 3_000_00, Sections: sectors([2]int{20, 28}, [2]int{49, 58})},
		},
	}}
})

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
