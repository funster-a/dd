package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/funster-a/dd/internal/payment/paymentdb"
	"github.com/funster-a/dd/internal/platform/events"
	"github.com/funster-a/dd/internal/platform/outbox"
)

// Config — адреса, которые платформа сообщает провайдеру.
type Config struct {
	// ReturnURL — страница, куда провайдер вернёт покупателя после оплаты.
	ReturnURL string
	// CallbackURL — адрес вебхука, куда провайдер пришлёт уведомление.
	CallbackURL string
}

// Service — оплата заказов.
type Service struct {
	pool *pgxpool.Pool
	q    *paymentdb.Queries
	gw   Gateway
	cfg  Config
	log  *slog.Logger
}

// NewService создаёт сервис оплаты.
func NewService(pool *pgxpool.Pool, gw Gateway, cfg Config, log *slog.Logger) *Service {
	return &Service{pool: pool, q: paymentdb.New(pool), gw: gw, cfg: cfg, log: log}
}

// Payment — попытка оплаты заказа.
type Payment struct {
	ID         string `json:"id"`
	OrderID    string `json:"order_id"`
	Status     string `json:"status"`
	AmountTiyn int64  `json:"amount_tiyn"`
	Currency   string `json:"currency"`
	// PaymentURL — страница оплаты провайдера: туда переходит покупатель.
	PaymentURL string `json:"payment_url"`
}

// StartPayment возвращает страницу оплаты заказа покупателя. У заказа одна
// действующая попытка: повторный вызов возвращает её же, новая создаётся,
// только если прежняя не удалась.
func (s *Service) StartPayment(ctx context.Context, buyerID, orderID string, now time.Time) (Payment, error) {
	if uuid.Validate(orderID) != nil {
		return Payment{}, ErrNotFound
	}
	o, err := s.q.GetPayableOrder(ctx, paymentdb.GetPayableOrderParams{ID: orderID, BuyerID: buyerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, ErrNotFound
	}
	if err != nil {
		return Payment{}, fmt.Errorf("load order: %w", err)
	}
	switch {
	case o.Status != "pending":
		return Payment{}, &PreconditionError{Code: "order_not_payable", Message: "order is " + o.Status}
	case !now.Before(o.ExpiresAt):
		return Payment{}, &PreconditionError{Code: "order_expired", Message: "order has expired"}
	case o.TotalTiyn == 0:
		return Payment{}, &PreconditionError{Code: "nothing_to_pay", Message: "free orders are not supported yet"}
	}

	// Оплата уже прошла, а заказ ещё ждёт подтверждения через очередь:
	// новая попытка списала бы деньги второй раз.
	paid, err := s.q.HasSucceededPayment(ctx, o.ID)
	if err != nil {
		return Payment{}, fmt.Errorf("check payments: %w", err)
	}
	if paid {
		return Payment{}, &PreconditionError{Code: "order_already_paid", Message: "order is already paid, tickets are on the way"}
	}

	p, err := s.activeOrNew(ctx, o)
	if err != nil {
		return Payment{}, err
	}
	if p.Status == "pending" && p.PaymentUrl != nil {
		return paymentFrom(p), nil
	}

	// Попытка в статусе created: создаём её у провайдера. Если процесс упал
	// после вызова провайдера, повтор с тем же id вернёт тот же платёж.
	pp, err := s.gw.CreatePayment(ctx, CreatePaymentRequest{
		PaymentID: p.ID, AmountTiyn: p.AmountTiyn, Currency: p.Currency,
		Description: fmt.Sprintf("Заказ %s — %s", shortID(o.ID), o.EventTitle),
		ReturnURL:   withQuery(s.cfg.ReturnURL, "order_id", o.ID), CallbackURL: s.cfg.CallbackURL,
	})
	if err != nil {
		// Явный отказ закрывает попытку. Сбой связи — нет: провайдер мог
		// успеть создать платёж, и следующий вызов его подхватит.
		if errors.Is(err, ErrRejected) {
			if ferr := s.q.SetPaymentFailed(context.WithoutCancel(ctx), p.ID); ferr != nil {
				s.log.ErrorContext(ctx, "mark payment failed", slog.Any("error", ferr))
			}
		}
		s.log.WarnContext(ctx, "create payment at provider", slog.String("payment_id", p.ID), slog.Any("error", err))
		return Payment{}, ErrProviderUnavailable
	}
	p, err = s.q.SetPaymentPending(ctx, paymentdb.SetPaymentPendingParams{ID: p.ID, ProviderPaymentID: &pp.ID, PaymentUrl: &pp.PaymentURL})
	if err != nil {
		return Payment{}, fmt.Errorf("save provider payment: %w", err)
	}
	return paymentFrom(p), nil
}

func (s *Service) activeOrNew(ctx context.Context, o paymentdb.GetPayableOrderRow) (paymentdb.Payment, error) {
	for range 2 {
		p, err := s.q.GetActivePayment(ctx, o.ID)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return paymentdb.Payment{}, fmt.Errorf("load active payment: %w", err)
		}
		p, err = s.q.InsertPayment(ctx, paymentdb.InsertPaymentParams{
			OrganizerID: o.OrganizerID, OrderID: o.ID, AmountTiyn: o.TotalTiyn, Currency: o.Currency, Provider: s.gw.Name(),
		})
		if err == nil {
			return p, nil
		}
		// Параллельный запрос успел создать попытку — берём её.
		if uniqueConstraint(err) != "payments_one_active_per_order_key" {
			return paymentdb.Payment{}, fmt.Errorf("insert payment: %w", err)
		}
	}
	return paymentdb.Payment{}, errors.New("payment attempt keeps changing, retry")
}

// HandleWebhook обрабатывает уведомление провайдера. Повторная доставка
// того же уведомления ничего не меняет (CLAUDE.md, правило 3). Успешная
// оплата записывает событие payment.succeeded в той же транзакции.
func (s *Service) HandleWebhook(ctx context.Context, provider string, header http.Header, body []byte, now time.Time) error {
	if provider != s.gw.Name() {
		return ErrNotFound
	}
	ev, err := s.gw.ParseWebhook(header, body)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.InsertWebhookEvent(ctx, paymentdb.InsertWebhookEventParams{
			Provider: provider, ProviderEventID: ev.EventID, Payload: body,
		}); errors.Is(err, pgx.ErrNoRows) {
			return nil // уже обработано
		} else if err != nil {
			return fmt.Errorf("save webhook: %w", err)
		}
		if err := s.apply(ctx, tx, q, provider, ev, now); err != nil {
			return err
		}
		return q.MarkWebhookProcessed(ctx, paymentdb.MarkWebhookProcessedParams{Provider: provider, ProviderEventID: ev.EventID})
	})
}

func (s *Service) apply(ctx context.Context, tx pgx.Tx, q *paymentdb.Queries, provider string, ev WebhookEvent, now time.Time) error {
	if !validID(ev.PaymentID) {
		s.log.WarnContext(ctx, "webhook for unknown payment", slog.String("payment_id", ev.PaymentID))
		return nil
	}
	p, err := q.LockPayment(ctx, paymentdb.LockPaymentParams{ID: ev.PaymentID, Provider: provider})
	if errors.Is(err, pgx.ErrNoRows) {
		s.log.WarnContext(ctx, "webhook for unknown payment", slog.String("payment_id", ev.PaymentID))
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock payment: %w", err)
	}
	if p.Status != "created" && p.Status != "pending" {
		if p.Status == "failed" && ev.Type == EventSucceeded {
			// Провайдер отказал при создании, а потом провёл оплату: так быть
			// не должно, деньги нужно разбирать вручную.
			s.log.ErrorContext(ctx, "succeeded webhook for a failed payment", slog.String("payment_id", p.ID))
		}
		return nil // итог уже записан
	}
	status := "failed"
	if ev.Type == EventSucceeded {
		status = "succeeded"
	}
	if _, err := q.SetPaymentResult(ctx, paymentdb.SetPaymentResultParams{
		ID: p.ID, Status: status, ProviderPaymentID: ev.ProviderPaymentID,
	}); err != nil {
		return fmt.Errorf("set payment result: %w", err)
	}
	if status != "succeeded" {
		return nil
	}
	if ev.AmountTiyn != p.AmountTiyn {
		s.log.ErrorContext(ctx, "provider amount differs from payment",
			slog.String("payment_id", p.ID), slog.Int64("provider", ev.AmountTiyn), slog.Int64("expected", p.AmountTiyn))
	}
	return outbox.Add(ctx, tx, events.PaymentSucceeded, events.PaymentSucceededEvent{
		PaymentID: p.ID, OrderID: p.OrderID, AmountTiyn: ev.AmountTiyn, PaidAt: now,
	})
}

// Refund возвращает всю сумму платежа (обработчик события refund.requested).
// Повторная доставка события не создаёт второй возврат и не возвращает
// деньги дважды: провайдер получает тот же ключ идемпотентности.
func (s *Service) Refund(ctx context.Context, ev events.RefundRequestedEvent) error {
	p, err := s.q.GetPayment(ctx, ev.PaymentID)
	if err != nil {
		return fmt.Errorf("load payment %s: %w", ev.PaymentID, err)
	}
	if p.Status != "succeeded" || p.ProviderPaymentID == nil {
		return fmt.Errorf("refund payment %s: payment is %s", p.ID, p.Status)
	}
	r, err := s.q.InsertRefund(ctx, paymentdb.InsertRefundParams{
		OrganizerID: p.OrganizerID, PaymentID: p.ID, OrderID: p.OrderID, AmountTiyn: p.AmountTiyn, Reason: ev.Reason,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		r, err = s.q.GetRefund(ctx, paymentdb.GetRefundParams{PaymentID: p.ID, Reason: ev.Reason})
	}
	if err != nil {
		return fmt.Errorf("save refund: %w", err)
	}
	if r.Status == "succeeded" {
		return nil
	}
	if err := s.q.SetRefundPending(ctx, r.ID); err != nil {
		return fmt.Errorf("mark refund pending: %w", err)
	}
	pr, err := s.gw.Refund(ctx, RefundRequest{RefundID: r.ID, ProviderPaymentID: *p.ProviderPaymentID, AmountTiyn: r.AmountTiyn})
	if errors.Is(err, ErrRejected) {
		// Провайдер отказал: повтор не поможет, возврат разбирается вручную.
		if ferr := s.q.SetRefundFailed(ctx, r.ID); ferr != nil {
			s.log.ErrorContext(ctx, "mark refund failed", slog.Any("error", ferr))
		}
	}
	if err != nil {
		return fmt.Errorf("refund at provider: %w", err)
	}
	if err := s.q.SetRefundSucceeded(ctx, paymentdb.SetRefundSucceededParams{ID: r.ID, ProviderRefundID: &pr.ID}); err != nil {
		return fmt.Errorf("mark refund succeeded: %w", err)
	}
	s.log.InfoContext(ctx, "payment refunded", slog.String("payment_id", p.ID), slog.String("reason", ev.Reason),
		slog.Int64("amount_tiyn", r.AmountTiyn))
	return nil
}

func paymentFrom(p paymentdb.Payment) Payment {
	out := Payment{ID: p.ID, OrderID: p.OrderID, Status: p.Status, AmountTiyn: p.AmountTiyn, Currency: p.Currency}
	if p.PaymentUrl != nil {
		out.PaymentURL = *p.PaymentUrl
	}
	return out
}

func validID(id string) bool { return uuid.Validate(id) == nil }

func shortID(id string) string {
	if len(id) < 8 {
		return id
	}
	return id[len(id)-8:]
}

func withQuery(raw, key, value string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String()
}
