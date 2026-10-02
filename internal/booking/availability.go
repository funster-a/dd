package booking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/booking/bookingdb"
)

// Availability — что сейчас можно купить. Схема зала с позициями мест —
// на публичной странице события (catalog); здесь только то, что меняется
// каждую секунду.
type Availability struct {
	// Taken — места с рядом, которые проданы или удерживаются.
	Taken []SeatRef `json:"taken"`
	// General — сколько виртуальных мест свободно во входных зонах.
	General []GeneralAvailability `json:"general"`
	// ServiceFeeBps — ставка сервисного сбора с покупателя в сотых долях
	// процента: сайт показывает сбор в корзине до оформления (ADR 019).
	ServiceFeeBps int32 `json:"service_fee_bps"`
}

// GeneralAvailability — свободные места входной зоны.
type GeneralAvailability struct {
	Section   string `json:"section"`
	Available int32  `json:"available"`
}

// GetAvailability возвращает занятость мест опубликованного события в JSON.
// Одновременные запросы по одному событию идут в базу одним запросом.
func (s *Service) GetAvailability(ctx context.Context, eventID string) ([]byte, error) {
	if uuid.Validate(eventID) != nil {
		return nil, ErrNotFound
	}
	v, err, _ := s.group.Do("availability:"+eventID, func() (any, error) {
		return s.loadAvailability(context.WithoutCancel(ctx), eventID, time.Now())
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func (s *Service) loadAvailability(ctx context.Context, eventID string, now time.Time) ([]byte, error) {
	status, err := s.q.GetPublishedEventStatus(ctx, eventID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "published") {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load event: %w", err)
	}
	taken, err := s.q.ListTakenSeats(ctx, bookingdb.ListTakenSeatsParams{EventID: eventID, Now: now})
	if err != nil {
		return nil, fmt.Errorf("list taken seats: %w", err)
	}
	general, err := s.q.CountGeneralAvailable(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("count general seats: %w", err)
	}
	a := Availability{Taken: make([]SeatRef, len(taken)), General: make([]GeneralAvailability, len(general)), ServiceFeeBps: s.feeBps}
	for i, t := range taken {
		a.Taken[i] = SeatRef{Section: t.Section, Row: deref(t.RowLabel), Seat: t.SeatLabel}
	}
	for i, g := range general {
		a.General[i] = GeneralAvailability{Section: g.Section, Available: g.Available}
	}
	return json.Marshal(a)
}
