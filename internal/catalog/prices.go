package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/catalog/catalogdb"
)

// maxPriceTiyn — 100 млн тенге: защита от опечатки лишними нулями.
const maxPriceTiyn = 100_000_000_00

// PriceInput — ценовая категория: сектора схемы, которые ею продаются
// целиком, и диапазоны рядов в других секторах (ADR 025).
type PriceInput struct {
	Name      string     `json:"name"`
	PriceTiyn int64      `json:"price_tiyn"`
	Sections  []string   `json:"sections"`
	Rows      []RowRange `json:"rows,omitempty"`
}

// RowRange — ряды сектора с From по To включительно в порядке схемы.
type RowRange struct {
	Section string `json:"section"`
	From    string `json:"from"`
	To      string `json:"to"`
}

// PriceCategory — ценовая категория события. Деньги — целые тиыны (правило 5).
type PriceCategory struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	PriceTiyn int64      `json:"price_tiyn"`
	Currency  string     `json:"currency"`
	Sections  []string   `json:"sections"`
	Rows      []RowRange `json:"rows"`
}

// SetPrices заменяет цены черновика: каждое место схемы должно продаваться
// ровно одной категорией — через свой сектор целиком или через диапазон
// рядов.
func (s *Service) SetPrices(ctx context.Context, organizerID, eventID string, in []PriceInput) ([]PriceCategory, error) {
	var out []PriceCategory
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		ev, err := q.GetEventForUpdate(ctx, catalogdb.GetEventForUpdateParams{OrganizerID: organizerID, ID: eventID})
		if err != nil {
			return notFound(err, "load event")
		}
		if err := requireDraft(ev.Status); err != nil {
			return err
		}
		if ev.SeatMapID == nil {
			return &PreconditionError{Code: "free_entry", Message: "free-entry event has no tickets and no prices"}
		}
		layout, err := eventLayout(ctx, q, organizerID, *ev.SeatMapID)
		if err != nil {
			return err
		}
		if err := validatePrices(in, layout); err != nil {
			return err
		}

		if err := q.DeleteEventPrices(ctx, catalogdb.DeleteEventPricesParams{OrganizerID: organizerID, EventID: eventID}); err != nil {
			return fmt.Errorf("delete prices: %w", err)
		}
		out = make([]PriceCategory, 0, len(in))
		for _, p := range in {
			pc, err := q.CreatePriceCategory(ctx, catalogdb.CreatePriceCategoryParams{
				OrganizerID: organizerID, EventID: eventID, Name: p.Name, PriceTiyn: p.PriceTiyn,
			})
			if err != nil {
				return fmt.Errorf("create price category: %w", err)
			}
			for _, sec := range p.Sections {
				if err := q.CreateSectionPrice(ctx, catalogdb.CreateSectionPriceParams{
					OrganizerID: organizerID, EventID: eventID, Section: sec, PriceCategoryID: pc.ID,
				}); err != nil {
					return fmt.Errorf("assign section price: %w", err)
				}
			}
			for _, rr := range p.Rows {
				if err := q.CreateRowPrice(ctx, catalogdb.CreateRowPriceParams{
					OrganizerID: organizerID, EventID: eventID, Section: rr.Section,
					RowFrom: rr.From, RowTo: rr.To, PriceCategoryID: pc.ID,
				}); err != nil {
					return fmt.Errorf("assign row price: %w", err)
				}
			}
			rows := p.Rows
			if rows == nil {
				rows = []RowRange{}
			}
			out = append(out, PriceCategory{ID: pc.ID, Name: pc.Name, PriceTiyn: pc.PriceTiyn, Currency: pc.Currency, Sections: p.Sections, Rows: rows})
		}
		return nil
	})
	return out, err
}

// GetPrices возвращает цены события с секторами.
func (s *Service) GetPrices(ctx context.Context, organizerID, eventID string) ([]PriceCategory, error) {
	if _, err := s.GetEvent(ctx, organizerID, eventID); err != nil {
		return nil, err
	}
	cats, err := s.q.ListPriceCategories(ctx, catalogdb.ListPriceCategoriesParams{OrganizerID: organizerID, EventID: eventID})
	if err != nil {
		return nil, fmt.Errorf("list price categories: %w", err)
	}
	links, err := s.q.ListSectionPrices(ctx, catalogdb.ListSectionPricesParams{OrganizerID: organizerID, EventID: eventID})
	if err != nil {
		return nil, fmt.Errorf("list section prices: %w", err)
	}
	ranges, err := s.q.ListRowPrices(ctx, catalogdb.ListRowPricesParams{OrganizerID: organizerID, EventID: eventID})
	if err != nil {
		return nil, fmt.Errorf("list row prices: %w", err)
	}
	sections := make(map[string][]string, len(cats))
	for _, l := range links {
		sections[l.PriceCategoryID] = append(sections[l.PriceCategoryID], l.Section)
	}
	rows := make(map[string][]RowRange, len(cats))
	for _, r := range ranges {
		rows[r.PriceCategoryID] = append(rows[r.PriceCategoryID], RowRange{Section: r.Section, From: r.RowFrom, To: r.RowTo})
	}
	out := make([]PriceCategory, 0, len(cats))
	for _, c := range cats {
		secs, rr := sections[c.ID], rows[c.ID]
		if secs == nil {
			secs = []string{}
		}
		if rr == nil {
			rr = []RowRange{}
		}
		out = append(out, PriceCategory{ID: c.ID, Name: c.Name, PriceTiyn: c.PriceTiyn, Currency: c.Currency, Sections: secs, Rows: rr})
	}
	return out, nil
}

func validatePrices(in []PriceInput, layout Layout) error {
	if len(in) == 0 {
		return &ValidationError{Field: "categories", Message: "at least one price category is required"}
	}
	byName := make(map[string]*Section, len(layout.Sections))
	for i := range layout.Sections {
		byName[layout.Sections[i].Name] = &layout.Sections[i]
	}
	names := make(map[string]bool, len(in))
	assigned := make(map[string]bool, len(byName))
	// rowOwner[сектор][индекс ряда] — ряд уже продаётся какой-то категорией.
	rowOwner := make(map[string][]bool)
	for i := range in {
		p := &in[i]
		path := fmt.Sprintf("categories[%d]", i)
		p.Name = strings.TrimSpace(p.Name)
		if n := utf8.RuneCountInString(p.Name); n == 0 || n > 100 {
			return &ValidationError{Field: path + ".name", Message: "must be 1-100 characters"}
		}
		if names[p.Name] {
			return &ValidationError{Field: path + ".name", Message: "category names must be unique"}
		}
		names[p.Name] = true
		if p.PriceTiyn < 0 || p.PriceTiyn > maxPriceTiyn {
			return &ValidationError{Field: path + ".price_tiyn", Message: "must be a non-negative amount in tiyn, at most 100 000 000 KZT"}
		}
		if len(p.Sections) == 0 && len(p.Rows) == 0 {
			return &ValidationError{Field: path + ".sections", Message: "must list at least one section or row range"}
		}
		if p.Sections == nil {
			p.Sections = []string{}
		}
		for j, sec := range p.Sections {
			sec = strings.TrimSpace(sec)
			p.Sections[j] = sec
			field := fmt.Sprintf("%s.sections[%d]", path, j)
			if byName[sec] == nil {
				return &ValidationError{Field: field, Message: fmt.Sprintf("section %q is not in the seat map", sec)}
			}
			if assigned[sec] || rowOwner[sec] != nil {
				return &ValidationError{Field: field, Message: fmt.Sprintf("section %q already has a price", sec)}
			}
			assigned[sec] = true
		}
		for j := range p.Rows {
			rr := &p.Rows[j]
			field := fmt.Sprintf("%s.rows[%d]", path, j)
			rr.Section, rr.From, rr.To = strings.TrimSpace(rr.Section), strings.TrimSpace(rr.From), strings.TrimSpace(rr.To)
			sec := byName[rr.Section]
			if sec == nil {
				return &ValidationError{Field: field + ".section", Message: fmt.Sprintf("section %q is not in the seat map", rr.Section)}
			}
			if sec.Kind != KindSeat {
				return &ValidationError{Field: field + ".section", Message: fmt.Sprintf("section %q has no rows", rr.Section)}
			}
			if assigned[rr.Section] {
				return &ValidationError{Field: field + ".section", Message: fmt.Sprintf("section %q already has a price as a whole", rr.Section)}
			}
			from, to := rowIndex(sec, rr.From), rowIndex(sec, rr.To)
			if from < 0 || to < 0 {
				return &ValidationError{Field: field, Message: fmt.Sprintf("rows %q-%q are not in section %q", rr.From, rr.To, rr.Section)}
			}
			if from > to {
				return &ValidationError{Field: field, Message: "from must not come after to"}
			}
			owner := rowOwner[rr.Section]
			if owner == nil {
				owner = make([]bool, len(sec.Rows))
				rowOwner[rr.Section] = owner
			}
			for k := from; k <= to; k++ {
				if owner[k] {
					return &ValidationError{Field: field, Message: fmt.Sprintf("row %q of section %q already has a price", sec.Rows[k].Label, rr.Section)}
				}
				owner[k] = true
			}
		}
	}
	for _, sec := range layout.Sections {
		if assigned[sec.Name] {
			continue
		}
		owner := rowOwner[sec.Name]
		if owner == nil {
			return &ValidationError{Field: "categories", Message: fmt.Sprintf("section %q has no price", sec.Name)}
		}
		for k, ok := range owner {
			if !ok {
				return &ValidationError{Field: "categories", Message: fmt.Sprintf("row %q of section %q has no price", sec.Rows[k].Label, sec.Name)}
			}
		}
	}
	return nil
}

// rowIndex — номер ряда с подписью label в секторе или -1.
func rowIndex(sec *Section, label string) int {
	for i, r := range sec.Rows {
		if r.Label == label {
			return i
		}
	}
	return -1
}

func eventLayout(ctx context.Context, q *catalogdb.Queries, organizerID, seatMapID string) (Layout, error) {
	m, err := q.GetSeatMap(ctx, catalogdb.GetSeatMapParams{OrganizerID: organizerID, ID: seatMapID})
	if err != nil {
		return Layout{}, fmt.Errorf("load seat map: %w", err)
	}
	var l Layout
	if err := json.Unmarshal(m.Layout, &l); err != nil {
		return Layout{}, fmt.Errorf("decode seat map %s: %w", m.ID, err)
	}
	return l, nil
}
