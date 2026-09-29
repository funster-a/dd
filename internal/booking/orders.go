package booking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"github.com/funster-a/dd/internal/booking/bookingdb"
)

// HoldTTL — сколько места держатся за неоплаченным заказом (CLAUDE.md).
const HoldTTL = 10 * time.Minute

// Service — бронирование мест и статусы заказа.
type Service struct {
	pool  *pgxpool.Pool
	q     *bookingdb.Queries
	holds *holdStore // nil — без Redis, только база
	log   *slog.Logger
	group singleflight.Group
}

// NewService создаёт сервис бронирования. rdb может быть nil: тогда холды
// держит только база, а корректность от этого не меняется.
func NewService(pool *pgxpool.Pool, rdb goredis.Scripter, log *slog.Logger) *Service {
	s := &Service{pool: pool, q: bookingdb.New(pool), log: log}
	if rdb != nil {
		s.holds = &holdStore{rdb: rdb}
	}
	return s
}

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
}

// Order — заказ покупателя.
type Order struct {
	ID        string      `json:"id"`
	EventID   string      `json:"event_id"`
	Status    string      `json:"status"`
	Email     string      `json:"email"`
	TotalTiyn int64       `json:"total_tiyn"`
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
	if uuid.Validate(eventID) != nil {
		return Order{}, ErrNotFound
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
			s.log.WarnContext(ctx, "redis holds unavailable, booking through database only", slog.Any("error", err))
		case n > 0:
			return Order{}, &ConflictError{Code: "seat_taken", Message: "seat " + req.Seats[n-1].String() + " is taken"}
		default:
			claimed = true
		}
	}

	res, err := s.createTx(ctx, ev, buyerID, orderID, req, now)
	if err != nil {
		if claimed {
			s.releaseKeys(ctx, keys, orderID)
		}
		return Order{}, err
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
			Message: fmt.Sprintf("at most %d tickets per order", ev.MaxTicketsPerBuyer),
		}
	}
	return nil
}

type createResult struct {
	order       Order
	generalKeys []string
	prevID      string
	prevKeys    []string
}

func (s *Service) createTx(ctx context.Context, ev bookingdb.GetBookableEventRow, buyerID, orderID string, req OrderRequest, now time.Time) (createResult, error) {
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
			Email: req.Email, ExpiresAt: expires,
		}); err != nil {
			if uniqueConstraint(err) == "orders_one_pending_per_buyer_event_key" {
				return &ConflictError{Code: "order_in_progress", Message: "another order for this event is being created, retry"}
			}
			return fmt.Errorf("insert order: %w", err)
		}

		var seatIDs []string
		var prices []int64
		var total int64
		add := func(id string, price int64) {
			seatIDs = append(seatIDs, id)
			prices = append(prices, price)
			total += price
		}

		if len(req.Seats) > 0 {
			p := bookingdb.HoldSeatsParams{OrderID: orderID, ExpiresAt: expires, Now: now, EventID: ev.ID}
			for _, r := range req.Seats {
				p.Sections = append(p.Sections, r.Section)
				p.Rows = append(p.Rows, r.Row)
				p.SeatLabels = append(p.SeatLabels, r.Seat)
			}
			held, err := q.HoldSeats(ctx, p)
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
		if err := q.InsertOrderItems(ctx, bookingdb.InsertOrderItemsParams{
			OrganizerID: ev.OrganizerID, EventID: ev.ID, OrderID: orderID, SeatIds: seatIDs, Prices: prices,
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
		out.Items[i] = OrderItem{Kind: it.Kind, Section: it.Section, Row: it.RowLabel, Seat: it.SeatLabel, PriceTiyn: it.PriceTiyn}
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
