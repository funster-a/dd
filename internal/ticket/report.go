package ticket

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/ticket/ticketdb"
)

// Report — продажи и посещаемость события для организатора (ADR 014).
// Деньги — целые тиыны (CLAUDE.md, правило 5).
type Report struct {
	Event       ReportEvent      `json:"event"`
	Seats       SeatStats        `json:"seats"`
	Tickets     IssueStats       `json:"tickets"`
	Money       MoneyStats       `json:"money"`
	Categories  []CategoryReport `json:"categories"`
	GeneratedAt time.Time        `json:"generated_at"`
}

// ReportEvent — событие отчёта.
type ReportEvent struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Admission string    `json:"admission"`
	StartsAt  time.Time `json:"starts_at"`
}

// SeatStats — заполняемость зала.
type SeatStats struct {
	Capacity  int32 `json:"capacity"`
	Sold      int32 `json:"sold"`
	Held      int32 `json:"held"` // в корзинах покупателей прямо сейчас
	Available int32 `json:"available"`
	// OccupancyPermille — продано от вместимости, в десятых долях процента
	// (734 = 73,4 %), чтобы не передавать дробные числа.
	OccupancyPermille int32 `json:"occupancy_permille"`
}

// IssueStats — билеты и проходы.
type IssueStats struct {
	Active   int32 `json:"active"`   // действующие: выпущены и использованы
	Used     int32 `json:"used"`     // прошли на вход
	Refunded int32 `json:"refunded"` // аннулированы при возврате
}

// MoneyStats — выручка с учётом возвратов.
type MoneyStats struct {
	PaidOrders    int32  `json:"paid_orders"`
	GrossTiyn     int64  `json:"gross_tiyn"`
	RefundedTiyn  int64  `json:"refunded_tiyn"`
	NetTiyn       int64  `json:"net_tiyn"`
	AverageTiyn   int64  `json:"average_order_tiyn"`
	CurrencyLabel string `json:"currency"`
}

// CategoryReport — продажи ценовой категории.
type CategoryReport struct {
	Name        string `json:"name"`
	PriceTiyn   int64  `json:"price_tiyn"`
	Capacity    int32  `json:"capacity"`
	Sold        int32  `json:"sold"`
	RevenueTiyn int64  `json:"revenue_tiyn"`
}

// Report собирает отчёт по событию организатора.
func (s *Service) Report(ctx context.Context, organizerID, eventID string, now time.Time) (Report, error) {
	ev, err := s.reportEvent(ctx, organizerID, eventID)
	if err != nil {
		return Report{}, err
	}
	seats, err := s.q.ReportSeats(ctx, ticketdb.ReportSeatsParams{EventID: eventID, Now: now})
	if err != nil {
		return Report{}, fmt.Errorf("report seats: %w", err)
	}
	tickets, err := s.q.ReportTickets(ctx, eventID)
	if err != nil {
		return Report{}, fmt.Errorf("report tickets: %w", err)
	}
	money, err := s.q.ReportMoney(ctx, eventID)
	if err != nil {
		return Report{}, fmt.Errorf("report money: %w", err)
	}
	cats, err := s.q.ReportCategories(ctx, eventID)
	if err != nil {
		return Report{}, fmt.Errorf("report categories: %w", err)
	}
	r := Report{
		Event: ReportEvent{ID: ev.ID, Title: ev.Title, Status: ev.Status, Admission: ev.Admission, StartsAt: ev.StartsAt},
		Seats: SeatStats{
			Capacity: seats.Capacity, Sold: seats.Sold, Held: seats.Held,
			Available: seats.Capacity - seats.Sold - seats.Held,
		},
		Tickets: IssueStats{Active: tickets.Active, Used: tickets.Used, Refunded: tickets.Revoked},
		Money: MoneyStats{
			PaidOrders: money.PaidOrders, GrossTiyn: money.Gross, RefundedTiyn: money.Refunded,
			NetTiyn: money.Gross - money.Refunded, CurrencyLabel: "KZT",
		},
		Categories:  make([]CategoryReport, len(cats)),
		GeneratedAt: now,
	}
	if seats.Capacity > 0 {
		r.Seats.OccupancyPermille = int32(int64(seats.Sold) * 1000 / int64(seats.Capacity)) //nolint:gosec // не больше 1000
	}
	if money.PaidOrders > 0 {
		r.Money.AverageTiyn = money.Gross / int64(money.PaidOrders)
	}
	for i, c := range cats {
		r.Categories[i] = CategoryReport{Name: c.Name, PriceTiyn: c.PriceTiyn, Capacity: c.Capacity, Sold: c.Sold, RevenueTiyn: c.Revenue}
	}
	return r, nil
}

func (s *Service) reportEvent(ctx context.Context, organizerID, eventID string) (ticketdb.ReportEventRow, error) {
	if uuid.Validate(eventID) != nil {
		return ticketdb.ReportEventRow{}, ErrNotFound
	}
	ev, err := s.q.ReportEvent(ctx, ticketdb.ReportEventParams{OrganizerID: organizerID, ID: eventID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ticketdb.ReportEventRow{}, ErrNotFound
	}
	if err != nil {
		return ticketdb.ReportEventRow{}, fmt.Errorf("load event: %w", err)
	}
	return ev, nil
}

// ExportTickets пишет список билетов события в CSV для Excel: UTF-8 с BOM
// и разделитель «;», как ждёт Excel в русской локали. Время — в часовом
// поясе UTC, суммы — тенге с копейками, посчитанные из тиынов без float.
func (s *Service) ExportTickets(ctx context.Context, organizerID, eventID string, w io.Writer) error {
	if _, err := s.reportEvent(ctx, organizerID, eventID); err != nil {
		return err
	}
	rows, err := s.q.ExportTickets(ctx, eventID)
	if err != nil {
		return fmt.Errorf("export tickets: %w", err)
	}
	if _, err := io.WriteString(w, "\ufeff"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write([]string{"Билет", "Сектор", "Ряд", "Место", "Категория", "Цена, ₸", "Статус", "Заказ", "Email покупателя", "Выпущен (UTC)", "Проход (UTC)"})
	for _, r := range rows {
		used := ""
		if r.UsedAt != nil {
			used = r.UsedAt.UTC().Format("2006-01-02 15:04:05")
		}
		row := []string{
			r.ID, r.Section, deref(r.RowLabel), r.SeatLabel, r.Category, tenge(r.PriceTiyn), ticketStatus[r.Status],
			r.OrderID, r.Email, r.IssuedAt.UTC().Format("2006-01-02 15:04:05"), used,
		}
		for i := range row {
			row[i] = safeCell(row[i])
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

var ticketStatus = map[string]string{"issued": "действует", "used": "прошёл", "revoked": "возвращён"}

// tenge: 500000 тиын → «5000,00».
func tenge(tiyn int64) string { return fmt.Sprintf("%d,%02d", tiyn/100, tiyn%100) }

// safeCell не даёт Excel выполнить формулу из данных покупателя
// (CSV injection): ячейка не начинается с = + - @.
func safeCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
