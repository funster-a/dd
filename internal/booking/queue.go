package booking

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	goredis "github.com/redis/go-redis/v9"

	"github.com/funster-a/dd/internal/booking/bookingdb"
	"github.com/funster-a/dd/internal/platform/redis"
)

// Очередь ожидания при старте продаж (ADR 020). В момент старта популярного
// события тысячи покупателей приходят за одну секунду. Очередь пускает их к
// покупке с постоянной скоростью, и база с Redis работают в режиме, который
// эксперимент ADR 017 показал быстрым, а не на пике.
//
// Как устроено:
//   - очередь события — отсортированное множество в Redis, участник —
//     покупатель (вход по телефону уже отсекает ботов без номеров);
//   - кто встал до старта, получает случайный номер среди пришедших до
//     старта: открывшие вкладку за 15 минут и за 15 секунд равны;
//   - кто пришёл после старта — в конец, по времени прихода;
//   - через t секунд после старта пропущены первые ⌊t × скорость⌋ мест:
//     пропуск вычисляется по часам, фонового процесса нет;
//   - очередь работает от QueueConfig.OpensBefore до старта и
//     QueueConfig.Window после него. Когда пропуск догнал хвост, новые
//     покупатели проходят сразу.
//
// Очередь только сглаживает нагрузку: корректность продажи от неё не
// зависит. Поэтому при отказе Redis она пропускает всех (как фильтр холдов,
// ADR 017), а не останавливает продажу.

// QueueConfig — параметры очереди ожидания.
type QueueConfig struct {
	// AdmitPerSecond — сколько покупателей в секунду пропускается к покупке.
	AdmitPerSecond int
	// OpensBefore — за сколько до старта продаж можно встать в очередь.
	OpensBefore time.Duration
	// Window — сколько очередь действует после старта продаж.
	Window time.Duration
}

// DefaultQueueConfig — очередь открывается за 15 минут до старта и
// действует 2 часа после него.
func DefaultQueueConfig(admitPerSecond int) QueueConfig {
	return QueueConfig{AdmitPerSecond: admitPerSecond, OpensBefore: 15 * time.Minute, Window: 2 * time.Hour}
}

// WithQueue включает очередь ожидания для событий с объявленным стартом
// продаж. Нужен Redis; без него очереди нет.
func WithQueue(cfg QueueConfig) Option { return func(s *Service) { s.queueCfg = cfg } }

// QueueStatus — место покупателя в очереди.
type QueueStatus struct {
	// State: not_required — очереди нет, можно покупать; not_open — очередь
	// ещё не открыта; waiting — ждёт; admitted — можно покупать.
	State string `json:"state"`
	// Position — номер в очереди среди ещё не пропущенных, с 1.
	Position int64 `json:"position,omitempty"`
	// EstimatedWaitSeconds — сколько примерно ждать пропуска.
	EstimatedWaitSeconds int64 `json:"estimated_wait_seconds,omitempty"`
	// PollAfterSeconds — когда спросить снова. Чем дальше покупатель, тем
	// реже: тысячи ждущих не должны сами создавать пик, от которого
	// очередь защищает.
	PollAfterSeconds int64 `json:"poll_after_seconds,omitempty"`
	// OpensAt — когда можно встать в очередь (для not_open).
	OpensAt *time.Time `json:"opens_at,omitempty"`
	// SalesStartAt — старт продаж.
	SalesStartAt *time.Time `json:"sales_start_at,omitempty"`
}

type waitingRoom struct {
	rdb  goredis.Scripter
	cfg  QueueConfig
	gate *redis.Gate
}

func newWaitingRoom(rdb goredis.Scripter, cfg QueueConfig, onOpen func(error)) *waitingRoom {
	return &waitingRoom{rdb: rdb, cfg: cfg, gate: &redis.Gate{Timeout: redisCallTimeout, Cooldown: breakerCooldown, OnOpen: onOpen}}
}

func queueKey(eventID string) string { return "booking:queue:{" + eventID + "}" }

// phase — где мы относительно окна очереди события.
type phase int

const (
	phaseNone phase = iota // очереди нет: старт не объявлен или окно прошло
	phaseNotOpen
	phaseOpen
)

func (w *waitingRoom) phase(start *time.Time, now time.Time) phase {
	switch {
	case w == nil || start == nil || !now.Before(start.Add(w.cfg.Window)):
		return phaseNone
	case now.Before(start.Add(-w.cfg.OpensBefore)):
		return phaseNotOpen
	default:
		return phaseOpen
	}
}

// admittedCount — сколько первых мест очереди пропущено к моменту now.
func (w *waitingRoom) admittedCount(start, now time.Time) int64 {
	if now.Before(start) {
		return 0
	}
	return int64(math.Floor(now.Sub(start).Seconds() * float64(w.cfg.AdmitPerSecond)))
}

// score — номер для нового участника: до старта случайный в [0, 1), после
// старта — 1 + секунды от старта, то есть позади всех пришедших до старта.
func score(start, now time.Time) float64 {
	if now.Before(start) {
		return rand.Float64() //nolint:gosec // жребий среди пришедших до старта, не секрет
	}
	return 1 + now.Sub(start).Seconds()
}

// queueScript ставит покупателя в очередь (если join и его там нет) и
// возвращает его номер с 0 или -1, если его нет в очереди.
var queueScript = goredis.NewScript(`
if ARGV[3] == '1' then
  redis.call('ZADD', KEYS[1], 'NX', ARGV[2], ARGV[1])
  redis.call('PEXPIRE', KEYS[1], ARGV[4])
end
local r = redis.call('ZRANK', KEYS[1], ARGV[1])
if not r then return -1 end
return r
`)

func (w *waitingRoom) rank(ctx context.Context, eventID, buyerID string, start, now time.Time, join bool) (int64, error) {
	j := "0"
	if join {
		j = "1"
	}
	// Ключ живёт до конца окна очереди и ещё час: потом он не нужен.
	ttl := start.Add(w.cfg.Window + time.Hour).Sub(now).Milliseconds()
	var r int64
	err := w.gate.Do(ctx, func(ctx context.Context) error {
		var err error
		r, err = queueScript.Run(ctx, w.rdb, []string{queueKey(eventID)},
			buyerID, strconv.FormatFloat(score(start, now), 'f', -1, 64), j, ttl).Int64()
		return err
	})
	return r, err
}

func (w *waitingRoom) status(start time.Time, rank int64, now time.Time) QueueStatus {
	st := start
	admitted := w.admittedCount(start, now)
	if rank < admitted {
		return QueueStatus{State: "admitted", SalesStartAt: &st}
	}
	pos := rank - admitted + 1
	// Пропуск места rank наступает через (rank+1)/скорость секунд после старта.
	at := start.Add(time.Duration(float64(rank+1) / float64(w.cfg.AdmitPerSecond) * float64(time.Second)))
	wait := int64(math.Ceil(at.Sub(now).Seconds()))
	wait = max(wait, 1)
	return QueueStatus{State: "waiting", Position: pos, EstimatedWaitSeconds: wait, PollAfterSeconds: pollAfter(wait), SalesStartAt: &st}
}

// pollAfter — половина оставшегося ожидания, от 2 до 20 секунд.
func pollAfter(wait int64) int64 { return min(max(wait/2, 2), 20) }

// JoinQueue ставит покупателя в очередь события и возвращает его место.
// Повторный вызов место не меняет — им же сайт опрашивает очередь.
func (s *Service) JoinQueue(ctx context.Context, buyerID, eventID string, now time.Time) (QueueStatus, error) {
	if uuid.Validate(eventID) != nil {
		return QueueStatus{}, ErrNotFound
	}
	ev, err := s.q.GetBookableEvent(ctx, eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return QueueStatus{}, ErrNotFound
	}
	if err != nil {
		return QueueStatus{}, fmt.Errorf("load event: %w", err)
	}
	if err := checkSalesOpenable(ev, now); err != nil {
		return QueueStatus{}, err
	}
	switch s.queue.phase(ev.SalesStartAt, now) {
	case phaseNone:
		return QueueStatus{State: "not_required"}, nil
	case phaseNotOpen:
		opens := ev.SalesStartAt.Add(-s.queue.cfg.OpensBefore)
		return QueueStatus{State: "not_open", OpensAt: &opens, SalesStartAt: ev.SalesStartAt, PollAfterSeconds: 20}, nil
	}
	rank, err := s.queue.rank(ctx, eventID, buyerID, *ev.SalesStartAt, now, true)
	if err != nil {
		// Без Redis очереди нет — покупка идёт напрямую.
		s.metrics.queueBypassed.Inc()
		return QueueStatus{State: "not_required"}, nil //nolint:nilerr // отказ Redis не останавливает продажу (ADR 020)
	}
	return s.queue.status(*ev.SalesStartAt, rank, now), nil
}

// checkQueue пропускает заказ, только если покупатель дождался своей
// очереди. Вызывается после checkSales: продажи уже идут.
func (s *Service) checkQueue(ctx context.Context, ev bookingdb.GetBookableEventRow, buyerID string, now time.Time) error {
	if s.queue.phase(ev.SalesStartAt, now) != phaseOpen {
		return nil
	}
	rank, err := s.queue.rank(ctx, ev.ID, buyerID, *ev.SalesStartAt, now, false)
	if err != nil {
		s.metrics.queueBypassed.Inc()
		return nil //nolint:nilerr // отказ Redis не останавливает продажу (ADR 020)
	}
	if rank < 0 || rank >= s.queue.admittedCount(*ev.SalesStartAt, now) {
		return &PreconditionError{Code: "queue_required", Message: "wait for your turn in the queue"}
	}
	return nil
}

// checkSalesOpenable — можно ли вообще ждать покупки: событие опубликовано,
// продаётся по билетам и продажи не закончились. Старт продаж здесь не
// проверяется: в очередь встают и до него.
func checkSalesOpenable(ev bookingdb.GetBookableEventRow, now time.Time) error {
	if err := checkSales(ev, now, 0); err != nil {
		if p, ok := errors.AsType[*PreconditionError](err); ok && p.Code == "sales_not_started" {
			return nil
		}
		return err
	}
	return nil
}
