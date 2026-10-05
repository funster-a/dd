package catalog

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/catalog/catalogdb"
)

// PublishResult — опубликованное событие и число сгенерированных мест.
type PublishResult struct {
	Event Event `json:"event"`
	Seats int64 `json:"seats"`
}

// Publish проверяет готовность черновика и одной транзакцией генерирует
// места из схемы зала (ADR 005) и переводит событие в published. У события
// со свободным входом мест нет: публикуется только страница.
//
// Строка события блокируется (FOR UPDATE), поэтому одновременные публикации
// выполняются по очереди: вторая увидит status = published и получит 409,
// а места не будут созданы дважды.
func (s *Service) Publish(ctx context.Context, organizerID, eventID string, now time.Time) (PublishResult, error) {
	var res PublishResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		ev, err := q.GetEventForUpdate(ctx, catalogdb.GetEventForUpdateParams{OrganizerID: organizerID, ID: eventID})
		if err != nil {
			return notFound(err, "load event")
		}
		if err := requireDraft(ev.Status); err != nil {
			return err
		}
		if ev.CoverImageKey == nil {
			return &PreconditionError{Code: "cover_required", Message: "upload a cover image before publishing"}
		}
		if !ev.StartsAt.After(now) {
			return &PreconditionError{Code: "starts_in_past", Message: "event must start in the future"}
		}

		var n int64
		if ev.SeatMapID != nil {
			if n, err = s.generateSeats(ctx, q, organizerID, eventID, *ev.SeatMapID); err != nil {
				return err
			}
		}
		// Свободный вход: мест и билетов нет, публикуется только страница.
		if err := q.MarkEventPublished(ctx, catalogdb.MarkEventPublishedParams{OrganizerID: organizerID, ID: eventID}); err != nil {
			return fmt.Errorf("mark published: %w", err)
		}
		published, err := q.GetEvent(ctx, catalogdb.GetEventParams{OrganizerID: organizerID, ID: eventID})
		if err != nil {
			return fmt.Errorf("reload event: %w", err)
		}
		res = PublishResult{Event: eventFrom(published), Seats: n}
		return nil
	})
	return res, err
}

// generateSeats создаёт места события из схемы зала по привязке секторов
// к ценам и возвращает их число.
func (s *Service) generateSeats(ctx context.Context, q *catalogdb.Queries, organizerID, eventID, seatMapID string) (int64, error) {
	layout, err := eventLayout(ctx, q, organizerID, seatMapID)
	if err != nil {
		return 0, err
	}
	links, err := q.ListSectionPrices(ctx, catalogdb.ListSectionPricesParams{OrganizerID: organizerID, EventID: eventID})
	if err != nil {
		return 0, fmt.Errorf("list section prices: %w", err)
	}
	ranges, err := q.ListRowPrices(ctx, catalogdb.ListRowPricesParams{OrganizerID: organizerID, EventID: eventID})
	if err != nil {
		return 0, fmt.Errorf("list row prices: %w", err)
	}
	pm := priceMap{sections: make(map[string]string, len(links)), rows: map[string]map[string]string{}}
	for _, l := range links {
		pm.sections[l.Section] = l.PriceCategoryID
	}
	for _, r := range ranges {
		pm.addRange(layout, r.Section, r.RowFrom, r.RowTo, r.PriceCategoryID)
	}
	seats, err := seatRows(organizerID, eventID, layout, pm)
	if err != nil {
		return 0, err
	}
	n, err := q.InsertEventSeats(ctx, seats)
	if err != nil {
		return 0, fmt.Errorf("insert event seats: %w", err)
	}
	return n, nil
}

// priceMap — категория места: сектор целиком или ряд сектора (ADR 025).
type priceMap struct {
	sections map[string]string
	rows     map[string]map[string]string // сектор → ряд → категория
}

func (pm priceMap) addRange(l Layout, section, from, to, category string) {
	for i := range l.Sections {
		sec := &l.Sections[i]
		if sec.Name != section {
			continue
		}
		a, b := rowIndex(sec, from), rowIndex(sec, to)
		if a < 0 || b < a {
			return // цены проверены при сохранении; схема черновика с тех пор не менялась
		}
		if pm.rows[section] == nil {
			pm.rows[section] = map[string]string{}
		}
		for k := a; k <= b; k++ {
			pm.rows[section][sec.Rows[k].Label] = category
		}
	}
}

func (pm priceMap) of(section, row string) (string, bool) {
	if c, ok := pm.sections[section]; ok {
		return c, true
	}
	c, ok := pm.rows[section][row]
	return c, ok
}

// seatRows разворачивает схему в строки мест. Виртуальные места входной
// зоны нумеруются 1..capacity и не имеют ряда.
func seatRows(organizerID, eventID string, l Layout, pm priceMap) ([]catalogdb.InsertEventSeatsParams, error) {
	rows := make([]catalogdb.InsertEventSeatsParams, 0, l.SeatCount())
	noPrice := func(what string) error {
		return &PreconditionError{Code: "prices_incomplete", Message: what + " has no price"}
	}
	for _, sec := range l.Sections {
		if sec.Kind == KindGeneral {
			price, ok := pm.of(sec.Name, "")
			if !ok {
				return nil, noPrice(fmt.Sprintf("section %q", sec.Name))
			}
			for i := 1; i <= sec.Capacity; i++ {
				rows = append(rows, catalogdb.InsertEventSeatsParams{
					OrganizerID: organizerID, EventID: eventID, PriceCategoryID: price,
					Kind: KindGeneral, Section: sec.Name, SeatLabel: strconv.Itoa(i),
				})
			}
			continue
		}
		for _, r := range sec.Rows {
			row := r.Label
			price, ok := pm.of(sec.Name, row)
			if !ok {
				return nil, noPrice(fmt.Sprintf("row %q of section %q", row, sec.Name))
			}
			for _, st := range r.Seats {
				rows = append(rows, catalogdb.InsertEventSeatsParams{
					OrganizerID: organizerID, EventID: eventID, PriceCategoryID: price,
					Kind: KindSeat, Section: sec.Name, RowLabel: &row, SeatLabel: st.Label,
				})
			}
		}
	}
	return rows, nil
}
