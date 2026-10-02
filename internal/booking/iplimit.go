package booking

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/funster-a/dd/internal/booking/bookingdb"
)

// Лимит билетов на событие с одного IP-адреса (ADR 020) — защита от
// перекупщика с множеством номеров телефонов. Лимит на покупателя такого не
// ловит: каждый номер — новый покупатель.
//
// Лимит мягкий: два одновременных заказа с одного адреса могут вместе его
// чуть превысить. Это защита от массовой скупки, а не инвариант, и
// блокировка на адрес не стоит задержки в самый нагруженный момент продаж.
// Поэтому же он щедрый: за NAT мобильного оператора одним адресом выходят
// сотни абонентов.

// WithIPTicketLimit задаёт лимит билетов на событие с одного IP-адреса.
// 0 — без лимита.
func WithIPTicketLimit(n int) Option { return func(s *Service) { s.ipLimit = n } }

func clientAddr(ip string) *netip.Addr {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return nil
	}
	a = a.Unmap()
	return &a
}

func (s *Service) checkIPLimit(ctx context.Context, ip *netip.Addr, buyerID, eventID string, count int, now time.Time) error {
	if s.ipLimit <= 0 || ip == nil {
		return nil
	}
	taken, err := s.q.CountIPTickets(ctx, bookingdb.CountIPTicketsParams{EventID: eventID, ClientIp: *ip, Now: now, BuyerID: buyerID})
	if err != nil {
		return fmt.Errorf("count tickets of ip: %w", err)
	}
	if int(taken)+count > s.ipLimit {
		return &PreconditionError{
			Code:    "ip_ticket_limit_exceeded",
			Message: fmt.Sprintf("at most %d tickets for this event from one network", s.ipLimit),
		}
	}
	return nil
}
