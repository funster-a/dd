package booking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/booking/bookingdb"
)

// Availability — что сейчас можно купить. Схема зала с позициями мест —
// на публичной странице события (catalog); здесь только то, что меняется
// каждую секунду.
type Availability struct {
	// Taken — места с рядом, которые проданы или удерживаются: все, только
	// сектора из запроса или ни одного (AvailabilityQuery).
	Taken []SeatRef `json:"taken"`
	// Sections — свободные и все места с рядом по секторам: план большой
	// площадки красит сектора по ним, не загружая места (ADR 024). В режиме
	// одного сектора — только его счётчик (ADR 026).
	Sections []SectionAvailability `json:"sections"`
	// General — сколько виртуальных мест свободно во входных зонах.
	General []GeneralAvailability `json:"general"`
	// ServiceFeeBps — ставка сервисного сбора с покупателя в сотых долях
	// процента: сайт показывает сбор в корзине до оформления (ADR 019).
	ServiceFeeBps int32 `json:"service_fee_bps"`
	// Queue — окно очереди ожидания при старте продаж (ADR 020) или null,
	// если очереди у события нет.
	Queue *QueueWindow `json:"queue"`
}

// QueueWindow — когда у события работает очередь ожидания.
type QueueWindow struct {
	OpensAt  time.Time `json:"opens_at"`
	ClosesAt time.Time `json:"closes_at"`
}

// SectionAvailability — свободные места сектора с рядами.
type SectionAvailability struct {
	Section   string `json:"section"`
	Available int32  `json:"available"`
	Total     int32  `json:"total"`
}

// AvailabilityQuery — какие занятые места вернуть. По умолчанию — все: так
// работает схема зала. План стадиона просит сводку без мест (Summary) и
// места одного открытого сектора (Section): на 24 тысячах мест полный
// список — сотни килобайт на каждый опрос.
type AvailabilityQuery struct {
	Section string
	Summary bool
}

func (q AvailabilityQuery) key() string {
	switch {
	case q.Summary:
		return "summary"
	case q.Section != "":
		return "section:" + q.Section
	default:
		return "all"
	}
}

// GeneralAvailability — свободные места входной зоны.
type GeneralAvailability struct {
	Section   string `json:"section"`
	Available int32  `json:"available"`
}

// WithSummaryTTL — сколько отдавать сводку по секторам из памяти (ADR 026).
func WithSummaryTTL(d time.Duration) Option { return func(s *Service) { s.summaryTTL = d } }

// summaryCache — последняя сводка по секторам каждого события. На старте
// продаж стадиона тысячи покупателей открывают план одновременно, а сводка —
// GROUP BY по десяткам тысяч строк мест. Секунда устаревания плана безвредна:
// место всё равно проверяет заказ (ADR 026).
type summaryCache struct {
	mu      sync.Mutex
	entries map[string]summaryEntry
}

type summaryEntry struct {
	body []byte
	at   time.Time
}

func (c *summaryCache) get(eventID string, now time.Time, ttl time.Duration) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[eventID]
	if !ok || now.Sub(e.at) >= ttl {
		return nil, false
	}
	return e.body, true
}

func (c *summaryCache) put(eventID string, body []byte, now time.Time, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]summaryEntry{}
	}
	// Устаревшие записи других событий убираются при записи: кэш не растёт
	// дальше числа событий, которые открывали за последние ttl.
	for id, e := range c.entries {
		if now.Sub(e.at) >= ttl {
			delete(c.entries, id)
		}
	}
	c.entries[eventID] = summaryEntry{body: body, at: now}
}

// GetAvailability возвращает занятость мест опубликованного события в JSON.
// Одновременные запросы по одному событию идут в базу одним запросом.
func (s *Service) GetAvailability(ctx context.Context, eventID string, q AvailabilityQuery) ([]byte, error) {
	if uuid.Validate(eventID) != nil {
		return nil, ErrNotFound
	}
	cached := q.Summary && s.summaryTTL > 0
	if cached {
		if b, ok := s.summaries.get(eventID, time.Now(), s.summaryTTL); ok {
			return b, nil
		}
	}
	v, err, _ := s.group.Do("availability:"+eventID+":"+q.key(), func() (any, error) {
		b, err := s.loadAvailability(context.WithoutCancel(ctx), eventID, q, time.Now())
		if err == nil && cached {
			s.summaries.put(eventID, b, time.Now(), s.summaryTTL)
		}
		return b, err
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func (s *Service) loadAvailability(ctx context.Context, eventID string, aq AvailabilityQuery, now time.Time) ([]byte, error) {
	ev, err := s.rq.GetPublishedEventStatus(ctx, eventID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && ev.Status != "published") {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load event: %w", err)
	}
	var taken []bookingdb.ListTakenSeatsRow
	switch {
	case aq.Summary:
	case aq.Section != "":
		rows, err := s.rq.ListTakenSeatsInSection(ctx, bookingdb.ListTakenSeatsInSectionParams{EventID: eventID, Section: aq.Section, Now: now})
		if err != nil {
			return nil, fmt.Errorf("list taken seats in section: %w", err)
		}
		for _, r := range rows {
			taken = append(taken, bookingdb.ListTakenSeatsRow(r))
		}
	default:
		taken, err = s.rq.ListTakenSeats(ctx, bookingdb.ListTakenSeatsParams{EventID: eventID, Now: now})
		if err != nil {
			return nil, fmt.Errorf("list taken seats: %w", err)
		}
	}
	var sections []bookingdb.CountSeatAvailabilityRow
	if aq.Section != "" {
		// Открытому сектору — только его счётчик: сводка по всему стадиону
		// стоит GROUP BY по десяткам тысяч строк на каждый запрос (ADR 026).
		rows, err := s.rq.CountSectionAvailability(ctx, bookingdb.CountSectionAvailabilityParams{EventID: eventID, Section: aq.Section, Now: now})
		if err != nil {
			return nil, fmt.Errorf("count section seats: %w", err)
		}
		for _, r := range rows {
			sections = append(sections, bookingdb.CountSeatAvailabilityRow(r))
		}
	} else {
		sections, err = s.rq.CountSeatAvailability(ctx, bookingdb.CountSeatAvailabilityParams{EventID: eventID, Now: now})
		if err != nil {
			return nil, fmt.Errorf("count seats: %w", err)
		}
	}
	var general []bookingdb.CountGeneralAvailableRow
	if aq.Section != "" {
		rows, err := s.rq.CountGeneralAvailableInSection(ctx, bookingdb.CountGeneralAvailableInSectionParams{EventID: eventID, Section: aq.Section})
		if err != nil {
			return nil, fmt.Errorf("count general seats in section: %w", err)
		}
		for _, r := range rows {
			general = append(general, bookingdb.CountGeneralAvailableRow(r))
		}
	} else {
		general, err = s.rq.CountGeneralAvailable(ctx, eventID)
		if err != nil {
			return nil, fmt.Errorf("count general seats: %w", err)
		}
	}
	a := Availability{Taken: make([]SeatRef, len(taken)), Sections: make([]SectionAvailability, len(sections)),
		General: make([]GeneralAvailability, len(general)), ServiceFeeBps: s.feeBps}
	for i, c := range sections {
		a.Sections[i] = SectionAvailability{Section: c.Section, Available: c.Available, Total: c.Total}
	}
	if s.queue != nil && ev.WaitingRoom && ev.SalesStartAt != nil {
		a.Queue = &QueueWindow{OpensAt: ev.SalesStartAt.Add(-s.queue.cfg.OpensBefore), ClosesAt: ev.SalesStartAt.Add(s.queue.cfg.Window)}
	}
	for i, t := range taken {
		a.Taken[i] = SeatRef{Section: t.Section, Row: deref(t.RowLabel), Seat: t.SeatLabel}
	}
	for i, g := range general {
		a.General[i] = GeneralAvailability{Section: g.Section, Available: g.Available}
	}
	return json.Marshal(a)
}
