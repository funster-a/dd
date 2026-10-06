package booking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"github.com/funster-a/dd/internal/booking/bookingdb"
	"github.com/funster-a/dd/internal/platform/idempotency"
)

// HoldTTL — сколько места держатся за неоплаченным заказом (CLAUDE.md).
const HoldTTL = 10 * time.Minute

// Service — бронирование мест и статусы заказа.
type Service struct {
	pool *pgxpool.Pool
	// summaries — сводка занятости по секторам в памяти на summaryTTL.
	summaries  summaryCache
	summaryTTL time.Duration
	q          *bookingdb.Queries
	// rq — чтения, которым не нужна свежесть до миллисекунды: занятость мест,
	// опрос очереди (ADR 031). По умолчанию — тот же ведущий узел.
	rq    *bookingdb.Queries
	holds *holdStore // nil — без Redis, только база
	log   *slog.Logger
	group singleflight.Group

	strategy Strategy
	metrics  *holdMetrics
	feeBps   int32        // сервисный сбор с покупателя, ADR 019
	ipLimit  int          // билетов на событие с одного IP, 0 — без лимита (ADR 020)
	queue    *waitingRoom // nil — очереди нет
	queueCfg QueueConfig
}

// NewService создаёт сервис бронирования. rdb может быть nil: тогда холды
// держит только база, а корректность от этого не меняется.
func NewService(pool *pgxpool.Pool, rdb goredis.Scripter, log *slog.Logger, opts ...Option) *Service {
	s := &Service{pool: pool, q: bookingdb.New(pool), log: log, strategy: StrategyRedis, metrics: defaultMetrics}
	s.rq = s.q
	for _, o := range opts {
		o(s)
	}
	if rdb != nil && s.strategy == StrategyRedis {
		s.holds = newHoldStore(rdb, func(err error) {
			s.metrics.gateOpened.Inc()
			s.log.Warn("redis holds disabled, booking through database only", slog.Duration("for", breakerCooldown), slog.Any("error", err))
		})
	}
	if rdb != nil && s.queueCfg.AdmitPerSecond > 0 {
		s.queue = newWaitingRoom(rdb, s.queueCfg, func(err error) {
			s.metrics.gateOpened.Inc()
			s.log.Warn("redis queue disabled, buyers go straight to booking", slog.Duration("for", breakerCooldown), slog.Any("error", err))
		})
	}
	return s
}

// Strategy — стратегия захвата мест этого сервиса.
func (s *Service) Strategy() Strategy { return s.strategy }

// SeatRef — место с рядом, указанное позицией на схеме зала.
type SeatRef struct {
	Section string `json:"section"`
	Row     string `json:"row"`
	Seat    string `json:"seat"`
}

func (r SeatRef) String() string {
	return fmt.Sprintf("%s, ряд %s, место %s", r.Section, r.Row, r.Seat)
}

// GeneralRef — сколько мест взять во входной зоне. Конкретные виртуальные
// места выбирает сервер.
type GeneralRef struct {
	Section  string `json:"section"`
	Quantity int    `json:"quantity"`
}

// OrderRequest — выбор покупателя.
type OrderRequest struct {
	Seats   []SeatRef    `json:"seats"`
	General []GeneralRef `json:"general"`
	// Email — куда отправить билеты.
	Email string `json:"email"`
	// ClientIP — адрес покупателя для лимита билетов на IP (ADR 020).
	// Заполняет обработчик HTTP, не клиент.
	ClientIP string `json:"-"`
}

// Order — заказ покупателя.
type Order struct {
	ID        string      `json:"id"`
	EventID   string      `json:"event_id"`
	Status    string      `json:"status"`
	Email     string      `json:"email"`
	TotalTiyn int64       `json:"total_tiyn"` // к оплате: билеты и сервисный сбор
	FeeTiyn   int64       `json:"fee_tiyn"`   // сервисный сбор в сумме заказа
	Currency  string      `json:"currency"`
	ExpiresAt time.Time   `json:"expires_at"`
	PaidAt    *time.Time  `json:"paid_at"`
	CreatedAt time.Time   `json:"created_at"`
	Items     []OrderItem `json:"items"`
}

// OrderItem — место в заказе с ценой на момент заказа.
type OrderItem struct {
	Kind      string  `json:"kind"`
	Section   string  `json:"section"`
	Row       *string `json:"row"`
	Seat      string  `json:"seat"`
	PriceTiyn int64   `json:"price_tiyn"`
	FeeTiyn   int64   `json:"fee_tiyn"`
}

func (r *OrderRequest) normalize() (int, error) {
	r.Email = strings.TrimSpace(r.Email)
	if a, err := mail.ParseAddress(r.Email); err != nil || a.Address != r.Email || len(r.Email) > 254 {
		return 0, &ValidationError{Field: "email", Message: "must be a valid email address"}
	}
	seen := map[SeatRef]bool{}
	for i := range r.Seats {
		s := &r.Seats[i]
		s.Section, s.Row, s.Seat = strings.TrimSpace(s.Section), strings.TrimSpace(s.Row), strings.TrimSpace(s.Seat)
		if s.Section == "" || s.Row == "" || s.Seat == "" {
			return 0, &ValidationError{Field: "seats", Message: "section, row and seat are required"}
		}
		if seen[*s] {
			return 0, &ValidationError{Field: "seats", Message: "seat " + s.String() + " is listed twice"}
		}
		seen[*s] = true
	}
	total := len(r.Seats)
	sections := map[string]bool{}
	for i := range r.General {
		g := &r.General[i]
		g.Section = strings.TrimSpace(g.Section)
		if g.Section == "" || g.Quantity <= 0 {
			return 0, &ValidationError{Field: "general", Message: "section and positive quantity are required"}
		}
		if sections[g.Section] {
			return 0, &ValidationError{Field: "general", Message: "section " + g.Section + " is listed twice"}
		}
		sections[g.Section] = true
		total += g.Quantity
	}
	if total == 0 {
		return 0, &ValidationError{Field: "seats", Message: "choose at least one seat"}
	}
	return total, nil
}

// CreateOrder держит выбранные места за покупателем HoldTTL и создаёт
// заказ в статусе pending. Прежняя корзина покупателя на это событие
// отменяется в той же транзакции.
//
// Место достаётся ровно одному заказу при любой конкурентности: захват —
// условный UPDATE строки места под блокировкой. Перед базой стоит холд в
// Redis: проигравшие конкуренты за популярное место отсекаются там и не
// нагружают PostgreSQL. Отказ Redis не останавливает продажу.
func (s *Service) CreateOrder(ctx context.Context, buyerID, eventID string, req OrderRequest, now time.Time) (Order, error) {
	var st attempt
	start := time.Now()
	o, err := s.createOrder(ctx, buyerID, eventID, req, now, &st)
	s.metrics.observe(s.strategy, &st, err, time.Since(start))
	return o, err
}

// attempt — что случилось с одной попыткой захвата: для метрик эксперимента.
type attempt struct {
	retries  int    // повторы оптимистичной стратегии
	rejectBy string // кто отказал: redis или db
}

func (s *Service) createOrder(ctx context.Context, buyerID, eventID string, req OrderRequest, now time.Time, st *attempt) (Order, error) {
	if uuid.Validate(eventID) != nil {
		return Order{}, ErrNotFound
	}
	// Повтор запроса, чей заказ уже зафиксирован, хотя ответ до клиента не
	// дошёл: база упала между фиксацией и ответом, и middleware
	// идемпотентности снял свой ключ (ADR 028). Возвращается тот же заказ.
	key := idempotency.KeyFrom(ctx)
	if key != "" {
		prev, err := s.q.GetOrderByRequestKey(ctx, bookingdb.GetOrderByRequestKeyParams{BuyerID: buyerID, RequestKey: &key})
		switch {
		case err == nil && prev.EventID == eventID:
			return s.GetOrder(ctx, buyerID, prev.ID, now)
		case err == nil:
			return Order{}, &ConflictError{Code: "idempotency_key_reused", Message: "this idempotency key was used for another event"}
		case !errors.Is(err, pgx.ErrNoRows):
			return Order{}, fmt.Errorf("load order by request key: %w", err)
		}
	}
	count, err := req.normalize()
	if err != nil {
		return Order{}, err
	}
	ev, err := s.q.GetBookableEvent(ctx, eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("load event: %w", err)
	}
	if err := checkSales(ev, now, count); err != nil {
		return Order{}, err
	}
	if err := s.checkQueue(ctx, ev, buyerID, now); err != nil {
		return Order{}, err
	}
	// Лимит — на покупателя за всё событие, с учётом уже купленных билетов:
	// перекупщик не наберёт билеты несколькими заказами.
	bought, err := s.q.CountBuyerTickets(ctx, bookingdb.CountBuyerTicketsParams{BuyerID: buyerID, EventID: eventID})
	if err != nil {
		return Order{}, fmt.Errorf("count bought tickets: %w", err)
	}
	if left := int(ev.MaxTicketsPerBuyer) - int(bought); count > left {
		return Order{}, &PreconditionError{
			Code: "ticket_limit_exceeded",
			Message: fmt.Sprintf("at most %d tickets per buyer for this event, %d already bought",
				ev.MaxTicketsPerBuyer, bought),
		}
	}

	ip := clientAddr(req.ClientIP)
	if err := s.checkIPLimit(ctx, ip, buyerID, eventID, count, now); err != nil {
		return Order{}, err
	}

	orderID := uuid.Must(uuid.NewV7()).String()
	prevID := ""
	if prev, err := s.q.LockPendingOrder(ctx, bookingdb.LockPendingOrderParams{BuyerID: buyerID, EventID: eventID}); err == nil {
		prevID = prev.ID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Order{}, fmt.Errorf("load pending order: %w", err)
	}

	keys := make([]string, len(req.Seats))
	for i, r := range req.Seats {
		keys[i] = holdKey(eventID, r.Section, &r.Row, r.Seat)
	}
	claimed := false
	if s.holds != nil && len(keys) > 0 {
		n, err := s.holds.claim(ctx, keys, orderID, prevID, HoldTTL)
		switch {
		case err != nil:
			// База держит инвариант сама; без Redis просто нет фильтра.
			// Выключение фильтра логирует holdStore — один раз, а не на каждый запрос.
		case n > 0:
			st.rejectBy = "redis"
			return Order{}, &ConflictError{Code: "seat_taken", Message: "seat " + req.Seats[n-1].String() + " is taken"}
		default:
			claimed = true
		}
	}

	res, err := s.createTx(ctx, ev, buyerID, orderID, req, ip, now)
	// Оптимистичная стратегия: конфликт версий — перечитать и попробовать снова.
	for errors.Is(err, errVersionConflict) && st.retries < optimisticAttempts-1 {
		st.retries++
		select {
		case <-ctx.Done():
			return Order{}, ctx.Err()
		case <-time.After(backoff(st.retries)):
		}
		res, err = s.createTx(ctx, ev, buyerID, orderID, req, ip, now)
	}
	if errors.Is(err, errVersionConflict) {
		err = &ConflictError{Code: "seat_taken", Message: "seats are being booked concurrently, retry"}
	}
	if err != nil {
		if c, ok := errors.AsType[*ConflictError](err); ok && c.Code == "seat_taken" {
			st.rejectBy = "db"
		}
		if claimed {
			s.releaseKeys(ctx, keys, orderID)
		}
		return Order{}, err
	}

	if res.free {
		// Места уже проданы: холды в Redis больше не нужны.
		s.releaseKeys(ctx, keys, orderID)
		res.generalKeys = nil
	}
	if s.holds != nil {
		// Виртуальные места выбирает база; их холды попадают в Redis после.
		if len(res.generalKeys) > 0 {
			if err := s.holds.set(ctx, res.generalKeys, orderID, HoldTTL); err != nil {
				s.log.WarnContext(ctx, "mirror general holds", slog.Any("error", err))
			}
		}
		if res.prevID != "" {
			s.releaseKeys(ctx, res.prevKeys, res.prevID)
		}
	}
	return res.order, nil
}

func checkSales(ev bookingdb.GetBookableEventRow, now time.Time, count int) error {
	switch ev.Status {
	case "published":
	case "cancelled":
		return &PreconditionError{Code: "event_cancelled", Message: "event is cancelled"}
	default:
		return ErrNotFound
	}
	if ev.Admission == "free_entry" {
		return &PreconditionError{Code: "free_entry", Message: "event has free entry, no tickets are needed"}
	}
	if ev.SalesStartAt != nil && now.Before(*ev.SalesStartAt) {
		return &PreconditionError{Code: "sales_not_started", Message: "sales have not started yet"}
	}
	end := ev.StartsAt
	if ev.SalesEndAt != nil {
		end = *ev.SalesEndAt
	}
	if !now.Before(end) {
		return &PreconditionError{Code: "sales_closed", Message: "sales are closed"}
	}
	if count > int(ev.MaxTicketsPerBuyer) {
		return &PreconditionError{
			Code:    "ticket_limit_exceeded",
			Message: fmt.Sprintf("at most %d tickets per buyer for this event", ev.MaxTicketsPerBuyer),
		}
	}
	return nil
}

type createResult struct {
	order       Order
	free        bool // бесплатный заказ оформлен сразу
	generalKeys []string
	prevID      string
	prevKeys    []string
}

func (s *Service) createTx(ctx context.Context, ev bookingdb.GetBookableEventRow, buyerID, orderID string, req OrderRequest, ip *netip.Addr, now time.Time) (createResult, error) {
	var res createResult
	expires := now.Add(HoldTTL)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)

		// Прежняя корзина: отменяется, её места снова свободны — в том числе
		// для этого же заказа.
		prev, err := q.LockPendingOrder(ctx, bookingdb.LockPendingOrderParams{BuyerID: buyerID, EventID: ev.ID})
		switch {
		case err == nil:
			res.prevID = prev.ID
			released, err := s.closeOrder(ctx, q, prev.ID, prev.ExpiresAt, now, "cancelled")
			if err != nil {
				return err
			}
			res.prevKeys = keysOf(ev.ID, released)
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("lock pending order: %w", err)
		}

		// Заказ вставляется до мест: холд ссылается на него внешним ключом.
		// Уникальный индекс «одна корзина» отсекает параллельный второй заказ
		// покупателя раньше, чем тот тронет места.
		if _, err := q.InsertOrder(ctx, bookingdb.InsertOrderParams{
			ID: orderID, OrganizerID: ev.OrganizerID, EventID: ev.ID, BuyerID: buyerID,
			Email: req.Email, ExpiresAt: expires, ClientIp: ip, RequestKey: nonEmpty(idempotency.KeyFrom(ctx)),
		}); err != nil {
			if c := uniqueConstraint(err); c == "orders_one_pending_per_buyer_event_key" || c == "orders_buyer_request_key" {
				return &ConflictError{Code: "order_in_progress", Message: "another order for this event is being created, retry"}
			}
			return fmt.Errorf("insert order: %w", err)
		}

		var seatIDs []string
		var prices, fees []int64
		var total int64
		add := func(id string, price int64) {
			fee := ServiceFee(price, s.feeBps)
			seatIDs = append(seatIDs, id)
			prices = append(prices, price)
			fees = append(fees, fee)
			total += price + fee
		}

		if len(req.Seats) > 0 {
			p := bookingdb.HoldSeatsParams{OrderID: orderID, ExpiresAt: expires, Now: now, EventID: ev.ID}
			for _, r := range req.Seats {
				p.Sections = append(p.Sections, r.Section)
				p.Rows = append(p.Rows, r.Row)
				p.SeatLabels = append(p.SeatLabels, r.Seat)
			}
			var held []bookingdb.HoldSeatsRow
			var err error
			if s.strategy == StrategyOptimistic {
				held, err = holdSeatsOptimistic(ctx, q, p)
			} else {
				// redis и pessimistic: SELECT … FOR UPDATE в порядке id.
				held, err = q.HoldSeats(ctx, p)
			}
			if errors.Is(err, errVersionConflict) {
				return err
			}
			if err != nil {
				return fmt.Errorf("hold seats: %w", err)
			}
			if len(held) != len(req.Seats) {
				return &ConflictError{Code: "seat_taken", Message: "seat " + missing(req.Seats, held).String() + " is taken or does not exist"}
			}
			for _, h := range held {
				add(h.ID, h.PriceTiyn)
			}
		}

		for _, g := range req.General {
			held, err := q.HoldGeneral(ctx, bookingdb.HoldGeneralParams{
				OrderID: orderID, ExpiresAt: expires, EventID: ev.ID, Section: g.Section, Quantity: int32(g.Quantity), //nolint:gosec // не больше лимита билетов
			})
			if err != nil {
				return fmt.Errorf("hold general: %w", err)
			}
			if len(held) < g.Quantity {
				return &ConflictError{Code: "not_enough_seats", Message: fmt.Sprintf("only %d seats left in %s", len(held), g.Section)}
			}
			for _, h := range held {
				add(h.ID, h.PriceTiyn)
				res.generalKeys = append(res.generalKeys, holdKey(ev.ID, h.Section, h.RowLabel, h.SeatLabel))
			}
		}

		o, err := q.SetOrderTotal(ctx, bookingdb.SetOrderTotalParams{ID: orderID, TotalTiyn: total})
		if err != nil {
			return fmt.Errorf("set order total: %w", err)
		}
		if total == 0 {
			// Бесплатные билеты: платить нечего, заказ оформляется сразу.
			if o, err = s.completeFree(ctx, tx, q, orderID, now); err != nil {
				return err
			}
			res.free = true
		}
		if err := q.InsertOrderItems(ctx, bookingdb.InsertOrderItemsParams{
			OrganizerID: ev.OrganizerID, EventID: ev.ID, OrderID: orderID, SeatIds: seatIDs, Prices: prices, Fees: fees,
		}); err != nil {
			return fmt.Errorf("insert order items: %w", err)
		}
		items, err := q.ListOrderItems(ctx, orderID)
		if err != nil {
			return fmt.Errorf("list order items: %w", err)
		}
		res.order = orderFrom(o, items)
		return nil
	})
	if code := pgCode(err); code == "40P01" || code == "40001" {
		return res, &ConflictError{Code: "seat_taken", Message: "seats are being booked concurrently, retry"}
	}
	return res, err
}

// closeOrder переводит pending-заказ в status (или в expired, если срок уже
// вышел) и освобождает его места. Возвращает освобождённые места.
func (s *Service) closeOrder(ctx context.Context, q *bookingdb.Queries, orderID string, expiresAt, now time.Time, status string) ([]bookingdb.ReleaseOrderSeatsRow, error) {
	if !now.Before(expiresAt) {
		status = "expired"
	}
	if _, err := q.SetOrderStatus(ctx, bookingdb.SetOrderStatusParams{ID: orderID, Status: status}); err != nil {
		return nil, fmt.Errorf("set order status: %w", err)
	}
	released, err := q.ReleaseOrderSeats(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("release seats: %w", err)
	}
	return released, nil
}

func missing(want []SeatRef, got []bookingdb.HoldSeatsRow) SeatRef {
	have := map[SeatRef]bool{}
	for _, g := range got {
		have[SeatRef{Section: g.Section, Row: deref(g.RowLabel), Seat: g.SeatLabel}] = true
	}
	for _, w := range want {
		if !have[w] {
			return w
		}
	}
	return want[0]
}

func keysOf(eventID string, seats []bookingdb.ReleaseOrderSeatsRow) []string {
	keys := make([]string, len(seats))
	for i, r := range seats {
		keys[i] = holdKey(eventID, r.Section, r.RowLabel, r.SeatLabel)
	}
	return keys
}

func (s *Service) releaseKeys(ctx context.Context, keys []string, orderID string) {
	if s.holds == nil || len(keys) == 0 {
		return
	}
	if err := s.holds.release(context.WithoutCancel(ctx), keys, orderID); err != nil {
		// Ключ всё равно истечёт вместе с холдом.
		s.log.WarnContext(ctx, "release redis holds", slog.Any("error", err))
	}
}

// GetOrder возвращает заказ покупателя. Просроченный pending-заказ
// закрывается прямо здесь, не дожидаясь фоновой очистки.
func (s *Service) GetOrder(ctx context.Context, buyerID, orderID string, now time.Time) (Order, error) {
	if uuid.Validate(orderID) != nil {
		return Order{}, ErrNotFound
	}
	o, err := s.q.GetBuyerOrder(ctx, bookingdb.GetBuyerOrderParams{ID: orderID, BuyerID: buyerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("load order: %w", err)
	}
	if o.Status == "pending" && !now.Before(o.ExpiresAt) {
		o, _, err = s.transition(ctx, buyerID, orderID, now, "expired")
		if err != nil {
			return Order{}, err
		}
	}
	return s.withItems(ctx, o)
}

// CancelOrder отменяет неоплаченный заказ покупателя и освобождает места.
// Повторная отмена возвращает тот же заказ.
func (s *Service) CancelOrder(ctx context.Context, buyerID, orderID string, now time.Time) (Order, error) {
	if uuid.Validate(orderID) != nil {
		return Order{}, ErrNotFound
	}
	o, released, err := s.transition(ctx, buyerID, orderID, now, "cancelled")
	if err != nil {
		return Order{}, err
	}
	s.releaseKeys(ctx, keysOf(o.EventID, released), o.ID)
	switch o.Status {
	case "cancelled":
		return s.withItems(ctx, o)
	case "expired":
		return Order{}, &PreconditionError{Code: "order_expired", Message: "order has already expired"}
	default:
		return Order{}, &PreconditionError{Code: "order_not_cancellable", Message: "only an unpaid order can be cancelled"}
	}
}

// transition закрывает pending-заказ покупателя статусом status под
// блокировкой строки заказа. Заказ в другом статусе возвращается как есть.
func (s *Service) transition(ctx context.Context, buyerID, orderID string, now time.Time, status string) (bookingdb.Order, []bookingdb.ReleaseOrderSeatsRow, error) {
	var o bookingdb.Order
	var released []bookingdb.ReleaseOrderSeatsRow
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		o, err = q.LockBuyerOrder(ctx, bookingdb.LockBuyerOrderParams{ID: orderID, BuyerID: buyerID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock order: %w", err)
		}
		if o.Status != "pending" {
			return nil
		}
		if released, err = s.closeOrder(ctx, q, o.ID, o.ExpiresAt, now, status); err != nil {
			return err
		}
		o, err = q.GetBuyerOrder(ctx, bookingdb.GetBuyerOrderParams{ID: orderID, BuyerID: buyerID})
		return err
	})
	return o, released, err
}

func (s *Service) withItems(ctx context.Context, o bookingdb.Order) (Order, error) {
	items, err := s.q.ListOrderItems(ctx, o.ID)
	if err != nil {
		return Order{}, fmt.Errorf("list order items: %w", err)
	}
	return orderFrom(o, items), nil
}

// ExpireOrders закрывает до batch просроченных заказов и освобождает их
// места. Возвращает число закрытых заказов. Холды в Redis истекают сами.
func (s *Service) ExpireOrders(ctx context.Context, now time.Time, batch int32) (int, error) {
	var n int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		ids, err := q.ExpireDueOrders(ctx, bookingdb.ExpireDueOrdersParams{Now: now, Batch: batch})
		if err != nil {
			return fmt.Errorf("expire orders: %w", err)
		}
		n = len(ids)
		if n == 0 {
			return nil
		}
		return q.ReleaseSeatsOfOrders(ctx, ids)
	})
	return n, err
}

func orderFrom(o bookingdb.Order, items []bookingdb.ListOrderItemsRow) Order {
	out := Order{
		ID: o.ID, EventID: o.EventID, Status: o.Status, Email: o.Email, TotalTiyn: o.TotalTiyn,
		Currency: o.Currency, ExpiresAt: o.ExpiresAt, PaidAt: o.PaidAt, CreatedAt: o.CreatedAt,
		Items: make([]OrderItem, len(items)),
	}
	for i, it := range items {
		out.Items[i] = OrderItem{Kind: it.Kind, Section: it.Section, Row: it.RowLabel, Seat: it.SeatLabel, PriceTiyn: it.PriceTiyn, FeeTiyn: it.FeeTiyn}
		out.FeeTiyn += it.FeeTiyn
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// OrderSummary — заказ в разделе «Мои билеты».
type OrderSummary struct {
	ID            string    `json:"id"`
	Status        string    `json:"status"`
	TotalTiyn     int64     `json:"total_tiyn"`
	Items         int32     `json:"items"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	EventID       string    `json:"event_id"`
	EventSlug     string    `json:"event_slug"`
	EventTitle    string    `json:"event_title"`
	EventStartsAt time.Time `json:"event_starts_at"`
	OrganizerSlug string    `json:"organizer_slug"`
	Venue         string    `json:"venue"`
	Timezone      string    `json:"timezone"`
}

// ListOrders возвращает заказы покупателя, новые сверху.
func (s *Service) ListOrders(ctx context.Context, buyerID string) ([]OrderSummary, error) {
	rows, err := s.q.ListBuyerOrders(ctx, buyerID)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	out := make([]OrderSummary, len(rows))
	for i, r := range rows {
		out[i] = OrderSummary{
			ID: r.ID, Status: r.Status, TotalTiyn: r.TotalTiyn, Items: r.Items, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
			EventID: r.EventID, EventSlug: r.EventSlug, EventTitle: r.EventTitle, EventStartsAt: r.EventStartsAt,
			OrganizerSlug: r.OrganizerSlug, Venue: r.VenueName, Timezone: r.VenueTimezone,
		}
	}
	return out, nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
