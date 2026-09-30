package ticket

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/funster-a/dd/internal/platform/events"
)

func TestReport(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	// Два оплаченных заказа: 2 билета и 1 билет по 5 000 ₸.
	o1, b1 := e.order(t, "paid", e.seats[0], e.seats[1])
	o2, _ := e.order(t, "paid", e.seats[2])
	for _, o := range []string{o1, o2} {
		if err := e.svc.Issue(ctx, events.OrderPaidEvent{OrderID: o}); err != nil {
			t.Fatal(err)
		}
	}
	ts, _ := e.svc.ForOrder(ctx, b1, o1)
	// Один билет прошёл на вход, один возвращён (деньги вернулись).
	sess := e.scanner(t)
	e.scan(t, sess, ts[0].URL)
	e.exec(t, `UPDATE tickets SET status = 'revoked', revoked_at = now() WHERE id = $1`, ts[1].ID)
	e.exec(t, `UPDATE event_seats SET status = 'available' WHERE id = $1`, e.seats[1])
	var payment string
	if err := e.db.Pool.QueryRow(ctx, `INSERT INTO payments (organizer_id, order_id, status, amount_tiyn, provider, provider_payment_id)
		VALUES ($1, $2, 'succeeded', 1000000, 'fakepsp', 'pay_1') RETURNING id`, e.org, o1).Scan(&payment); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO refunds (organizer_id, payment_id, order_id, status, amount_tiyn, reason) VALUES ($1, $2, $3, 'succeeded', 500000, 'buyer_request')`,
		e.org, payment, o1)
	// Email с формулой: в CSV она не должна выполниться.
	e.exec(t, `UPDATE orders SET email = '=HYPERLINK("x")@evil.kz' WHERE id = $1`, o2)

	r, err := e.svc.Report(ctx, e.org, e.event, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Seats.Capacity != 3 || r.Seats.Sold != 2 || r.Seats.Available != 1 || r.Seats.OccupancyPermille != 666 {
		t.Errorf("seats = %+v", r.Seats)
	}
	if r.Tickets.Active != 2 || r.Tickets.Used != 1 || r.Tickets.Refunded != 1 {
		t.Errorf("tickets = %+v", r.Tickets)
	}
	if r.Money.PaidOrders != 2 || r.Money.GrossTiyn != 1500000 || r.Money.RefundedTiyn != 500000 || r.Money.NetTiyn != 1000000 {
		t.Errorf("money = %+v", r.Money)
	}
	if len(r.Categories) != 1 || r.Categories[0].Sold != 2 || r.Categories[0].RevenueTiyn != 1000000 || r.Categories[0].Capacity != 3 {
		t.Errorf("categories = %+v", r.Categories)
	}

	var buf bytes.Buffer
	if err := e.svc.ExportTickets(ctx, e.org, e.event, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("\xef\xbb\xbf")) {
		t.Error("CSV must start with a UTF-8 BOM for Excel")
	}
	cr := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(buf.Bytes(), []byte("\xef\xbb\xbf"))))
	cr.Comma = ';'
	records, err := cr.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 || records[0][0] != "Билет" {
		t.Fatalf("csv = %v", records)
	}
	if records[1][5] != "5000,00" {
		t.Errorf("price cell = %q, want 5000,00", records[1][5])
	}
	var statuses, emails []string
	for _, rec := range records[1:] {
		statuses = append(statuses, rec[6])
		emails = append(emails, rec[8])
	}
	if got := strings.Join(statuses, ","); !strings.Contains(got, "прошёл") || !strings.Contains(got, "возвращён") {
		t.Errorf("statuses = %s", got)
	}
	for _, em := range emails {
		if strings.HasPrefix(em, "=") {
			t.Errorf("formula reached the CSV unescaped: %q", em)
		}
	}

	stranger := uuid.NewString()
	if _, err := e.svc.Report(ctx, stranger, e.event, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("another organizer's report: %v", err)
	}
	if err := e.svc.ExportTickets(ctx, stranger, e.event, &buf); !errors.Is(err, ErrNotFound) {
		t.Errorf("another organizer's export: %v", err)
	}
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.db.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}
