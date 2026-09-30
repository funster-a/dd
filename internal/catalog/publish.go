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
	priceOf := make(map[string]string, len(links))
	for _, l := range links {
		priceOf[l.Section] = l.PriceCategoryID
	}
	seats, err := seatRows(organizerID, eventID, layout, priceOf)
	if err != nil {
		return 0, err
	}
	n, err := q.InsertEventSeats(ctx, seats)
	if err != nil {
		return 0, fmt.Errorf("insert event seats: %w", err)
	}
	return n, nil
}

// seatRows разворачивает схему в строки мест. Виртуальные места входной
// зоны нумеруются 1..capacity и не имеют ряда.
func seatRows(organizerID, eventID string, l Layout, priceOf map[string]string) ([]catalogdb.InsertEventSeatsParams, error) {
	rows := make([]catalogdb.InsertEventSeatsParams, 0, l.SeatCount())
	for _, sec := range l.Sections {
		price, ok := priceOf[sec.Name]
		if !ok {
			return nil, &PreconditionError{Code: "prices_incomplete", Message: fmt.Sprintf("section %q has no price", sec.Name)}
		}
		if sec.Kind == KindGeneral {
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
