package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/funster-a/dd/internal/booking"
)

// expiredTotal — сколько неоплаченных заказов закрыто по истечении холда.
var expiredTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "dd_booking_orders_expired_total",
	Help: "Неоплаченные заказы, закрытые воркером по истечении холда.",
})

// Как часто закрывать просроченные заказы. Место с истёкшим холдом можно
// купить и до очистки (захват считает такой холд свободным); очистка
// нужна, чтобы заказ получил статус expired, а виртуальные места вернулись
// в выдачу входной зоны.
const (
	expireInterval = 15 * time.Second
	expireBatch    = 500
)

// expireOrders закрывает просроченные заказы, пока ctx не отменён.
func expireOrders(ctx context.Context, book *booking.Service, log *slog.Logger) {
	t := time.NewTicker(expireInterval)
	defer t.Stop()
	for {
		for {
			n, err := book.ExpireOrders(ctx, time.Now(), expireBatch)
			if err != nil {
				if ctx.Err() == nil {
					log.Error("expire orders", slog.Any("error", err))
				}
				break
			}
			if n > 0 {
				expiredTotal.Add(float64(n))
				log.Info("orders expired", slog.Int("count", n))
			}
			if n < expireBatch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
