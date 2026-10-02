package booking

import (
	"testing"
	"time"
)

func TestIPTicketLimit(t *testing.T) {
	e := newEnv(t, false, 3, 10, 0)
	e.svc = NewService(e.db.Pool, nil, quietLog(), WithIPTicketLimit(3))
	ctx := t.Context()
	now := time.Now()
	from := func(ip string, refs ...SeatRef) OrderRequest {
		r := seatsReq(refs...)
		r.ClientIP = ip
		return r
	}

	a, b := e.buyer(t), e.buyer(t)
	if _, err := e.svc.CreateOrder(ctx, a, e.eventID, from("203.0.113.7", seat(1, 1), seat(1, 2)), now); err != nil {
		t.Fatal(fmtErr(err))
	}
	// Второй покупатель с того же адреса: 2 + 2 > 3.
	if _, err := e.svc.CreateOrder(ctx, b, e.eventID, from("203.0.113.7", seat(2, 1), seat(2, 2)), now); !isPrecondition(err, "ip_ticket_limit_exceeded") {
		t.Errorf("over the ip limit: %s", fmtErr(err))
	}
	if _, err := e.svc.CreateOrder(ctx, b, e.eventID, from("203.0.113.7", seat(2, 1)), now); err != nil {
		t.Errorf("within the ip limit: %s", fmtErr(err))
	}
	// Своя корзина не считается: новый заказ её заменит (2 своих → 2 новых, +1 у b).
	if _, err := e.svc.CreateOrder(ctx, a, e.eventID, from("203.0.113.7", seat(3, 1), seat(3, 2)), now); err != nil {
		t.Errorf("replacing own cart: %s", fmtErr(err))
	}
	// IPv4 в IPv6-записи — тот же адрес.
	if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, from("::ffff:203.0.113.7", seat(3, 5)), now); !isPrecondition(err, "ip_ticket_limit_exceeded") {
		t.Errorf("same address in ipv6 form: %s", fmtErr(err))
	}
	// Другой адрес и заказ без адреса — без ограничений.
	if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, from("198.51.100.1", seat(3, 7), seat(3, 8)), now); err != nil {
		t.Errorf("another address: %s", fmtErr(err))
	}
	if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, seatsReq(seat(3, 9), seat(3, 10)), now); err != nil {
		t.Errorf("no address: %s", fmtErr(err))
	}
	// Истёкшая корзина места не занимает.
	if _, err := e.svc.CreateOrder(ctx, e.buyer(t), e.eventID, from("203.0.113.7", seat(2, 5)), now.Add(HoldTTL+time.Minute)); err != nil {
		t.Errorf("after carts expired: %s", fmtErr(err))
	}
}
