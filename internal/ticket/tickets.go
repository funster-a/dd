package ticket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/funster-a/dd/internal/platform/events"
	"github.com/funster-a/dd/internal/ticket/ticketdb"
)

// ErrNotFound — билета или заказа нет, либо он чужой.
var ErrNotFound = errors.New("not found")

// Mailer доставляет билеты покупателю.
type Mailer interface {
	SendTickets(ctx context.Context, m Mail) error
}

// Mail — письмо с билетами.
type Mail struct {
	To         string
	EventTitle string
	StartsAt   time.Time
	Links      []string
}

// LogMailer пишет письмо в лог вместо отправки — для разработки, как код
// входа в identity.LogSender.
type LogMailer struct{ Log *slog.Logger }

// SendTickets пишет письмо в лог.
func (m LogMailer) SendTickets(ctx context.Context, mail Mail) error {
	m.Log.InfoContext(ctx, "tickets email (not sent in development)",
		slog.String("to", mail.To), slog.String("event", mail.EventTitle), slog.Any("links", mail.Links))
	return nil
}

// Config — настройки выпуска билетов.
type Config struct {
	// PublicBaseURL — адрес платформы для ссылок на билеты.
	PublicBaseURL string
	// SigningKey — секрет подписи токенов билетов.
	SigningKey string
}

// Service — выпуск и показ билетов.
type Service struct {
	pool   *pgxpool.Pool
	q      *ticketdb.Queries
	mailer Mailer
	signer Signer
	base   string
	log    *slog.Logger
}

// NewService создаёт сервис билетов.
func NewService(pool *pgxpool.Pool, mailer Mailer, cfg Config, log *slog.Logger) *Service {
	return &Service{
		pool: pool, q: ticketdb.New(pool), mailer: mailer, signer: NewSigner(cfg.SigningKey),
		base: strings.TrimRight(cfg.PublicBaseURL, "/"), log: log,
	}
}

// Ticket — билет в заказе покупателя.
type Ticket struct {
	ID      string  `json:"id"`
	Status  string  `json:"status"`
	Kind    string  `json:"kind"`
	Section string  `json:"section"`
	Row     *string `json:"row"`
	Seat    string  `json:"seat"`
	// URL — страница билета с QR-кодом; её же кодирует QR.
	URL string `json:"url"`
}

// Issue выпускает билеты оплаченного заказа и отправляет их на почту
// (обработчик события order.paid). Повторная доставка события не выпускает
// билеты второй раз и не шлёт второе письмо.
func (s *Service) Issue(ctx context.Context, ev events.OrderPaidEvent) error {
	var issued []string
	var d ticketdb.GetOrderDeliveryRow
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		if d, err = q.GetOrderDelivery(ctx, ev.OrderID); err != nil {
			return fmt.Errorf("load order %s: %w", ev.OrderID, err)
		}
		if d.Status != "paid" {
			return fmt.Errorf("issue tickets: order %s is %s", ev.OrderID, d.Status)
		}
		issued, err = q.IssueTickets(ctx, ev.OrderID)
		if err != nil {
			return fmt.Errorf("issue tickets: %w", err)
		}
		return nil
	})
	if err != nil || len(issued) == 0 {
		return err
	}
	links := make([]string, len(issued))
	for i, id := range issued {
		links[i] = s.link(id)
	}
	s.log.InfoContext(ctx, "tickets issued", slog.String("order_id", ev.OrderID), slog.Int("count", len(issued)))
	// Письмо после коммита: билеты уже есть и видны в заказе, даже если
	// отправка не удалась.
	if err := s.mailer.SendTickets(ctx, Mail{To: d.Email, EventTitle: d.Title, StartsAt: d.StartsAt, Links: links}); err != nil {
		s.log.ErrorContext(ctx, "send tickets email", slog.String("order_id", ev.OrderID), slog.Any("error", err))
	}
	return nil
}

// ForOrder возвращает билеты заказа покупателя.
func (s *Service) ForOrder(ctx context.Context, buyerID, orderID string) ([]Ticket, error) {
	if uuid.Validate(orderID) != nil {
		return nil, ErrNotFound
	}
	d, err := s.q.GetOrderDelivery(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && d.BuyerID != buyerID) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load order: %w", err)
	}
	rows, err := s.q.ListOrderTickets(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("list tickets: %w", err)
	}
	out := make([]Ticket, len(rows))
	for i, r := range rows {
		out[i] = Ticket{ID: r.ID, Status: r.Status, Kind: r.Kind, Section: r.Section, Row: r.RowLabel, Seat: r.SeatLabel, URL: s.link(r.ID)}
	}
	return out, nil
}

// View — публичный вид билета по ссылке.
type View struct {
	Status    string    `json:"status"`
	Event     string    `json:"event"`
	AgeRating string    `json:"age_rating"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Venue     string    `json:"venue"`
	Address   string    `json:"address"`
	Timezone  string    `json:"timezone"`
	Kind      string    `json:"kind"`
	Section   string    `json:"section"`
	Row       *string   `json:"row"`
	Seat      string    `json:"seat"`
	URL       string    `json:"url"`
}

// ByToken возвращает билет по токену из ссылки.
func (s *Service) ByToken(ctx context.Context, token string) (View, error) {
	id, ok := s.signer.Parse(token)
	if !ok {
		return View{}, ErrNotFound
	}
	t, err := s.q.GetTicket(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, ErrNotFound
	}
	if err != nil {
		return View{}, fmt.Errorf("load ticket: %w", err)
	}
	return View{
		Status: t.Status, Event: t.Title, AgeRating: t.AgeRating, StartsAt: t.StartsAt, EndsAt: t.EndsAt,
		Venue: t.VenueName, Address: t.VenueAddress, Timezone: t.VenueTimezone,
		Kind: t.Kind, Section: t.Section, Row: t.RowLabel, Seat: t.SeatLabel, URL: s.link(t.ID),
	}, nil
}

func (s *Service) link(ticketID string) string { return s.base + "/t/" + s.signer.Token(ticketID) }
