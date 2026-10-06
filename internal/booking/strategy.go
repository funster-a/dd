package booking

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/funster-a/dd/internal/booking/bookingdb"
)

// Strategy — как захватывается место с рядом при конкурентном доступе
// (spec.md, «Инженерное ядро»; ADR 017). Остальной путь заказа одинаков.
type Strategy string

const (
	// StrategyRedis — продуктовый вариант (ADR 011): атомарный захват ключа
	// места в Redis отсекает проигравших до базы, затем условный UPDATE под
	// блокировкой строки в PostgreSQL. При отказе Redis — только база.
	StrategyRedis Strategy = "redis"
	// StrategyPessimistic — только база: SELECT … FOR UPDATE по строкам мест,
	// конкуренты ждут блокировку.
	StrategyPessimistic Strategy = "pessimistic"
	// StrategyOptimistic — только база: чтение без блокировки с версией
	// строки, UPDATE при совпадении версии, повтор при конфликте.
	StrategyOptimistic Strategy = "optimistic"
)

// ParseStrategy разбирает значение настройки BOOKING_STRATEGY.
func ParseStrategy(s string) (Strategy, error) {
	switch st := Strategy(s); st {
	case StrategyRedis, StrategyPessimistic, StrategyOptimistic:
		return st, nil
	case "":
		return StrategyRedis, nil
	default:
		return "", fmt.Errorf("unknown booking strategy %q: want redis, pessimistic or optimistic", s)
	}
}

// Option настраивает сервис бронирования.
type Option func(*Service)

// WithReader направляет чтения занятости мест и опрос очереди в r — обычно
// реплики с запасным ведущим узлом (db.Reader, ADR 031).
func WithReader(r bookingdb.DBTX) Option { return func(s *Service) { s.rq = bookingdb.New(r) } }

// WithStrategy выбирает стратегию захвата мест. Стратегии «только база»
// не используют Redis для холдов вовсе — так их честно сравнивать.
func WithStrategy(st Strategy) Option { return func(s *Service) { s.strategy = st } }

// optimisticAttempts — сколько раз оптимистичная стратегия перечитывает
// места после конфликта версий, прежде чем ответить «место занято».
const optimisticAttempts = 4

// errVersionConflict — версия строки изменилась между чтением и записью:
// транзакция откатывается и повторяется целиком.
var errVersionConflict = errors.New("seat version changed")

// holdSeatsOptimistic держит места, если с момента чтения их никто не менял.
// Место, занятое действующим холдом или продажей, — сразу «занято», без
// повтора: перечитывание этого не изменит.
func holdSeatsOptimistic(ctx context.Context, q *bookingdb.Queries, p bookingdb.HoldSeatsParams) ([]bookingdb.HoldSeatsRow, error) {
	rows, err := q.ReadSeatsForHold(ctx, bookingdb.ReadSeatsForHoldParams{
		EventID: p.EventID, Sections: p.Sections, Rows: p.Rows, SeatLabels: p.SeatLabels,
	})
	if err != nil {
		return nil, fmt.Errorf("read seats: %w", err)
	}
	ids := make([]string, 0, len(rows))
	versions := make([]int32, 0, len(rows))
	held := make([]bookingdb.HoldSeatsRow, 0, len(rows))
	for _, r := range rows {
		free := r.Status == "available" || (r.Status == "held" && r.HoldExpiresAt != nil && !r.HoldExpiresAt.After(p.Now))
		if !free {
			continue // missing() назовёт это место
		}
		ids = append(ids, r.ID)
		versions = append(versions, r.Version)
		held = append(held, bookingdb.HoldSeatsRow{ID: r.ID, Section: r.Section, RowLabel: r.RowLabel, SeatLabel: r.SeatLabel, PriceTiyn: r.PriceTiyn})
	}
	if len(held) != len(p.Sections) {
		return held, nil
	}
	got, err := q.HoldSeatsIfVersion(ctx, bookingdb.HoldSeatsIfVersionParams{
		OrderID: p.OrderID, ExpiresAt: p.ExpiresAt, Ids: ids, Versions: versions,
	})
	if err != nil {
		return nil, fmt.Errorf("hold seats by version: %w", err)
	}
	if len(got) != len(ids) {
		return nil, errVersionConflict
	}
	return held, nil
}

// backoff — пауза перед повтором оптимистичной попытки: растёт с номером
// попытки, со случайным разбросом, чтобы конкуренты не сталкивались снова.
func backoff(attempt int) time.Duration {
	base := time.Duration(attempt) * 2 * time.Millisecond
	return base + rand.N(base+time.Millisecond) //nolint:gosec // разброс пауз, не криптография
}
