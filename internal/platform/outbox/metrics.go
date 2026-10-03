package outbox

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// publishedTotal — сколько событий релей отправил в RabbitMQ.
var publishedTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "dd_outbox_published_total",
	Help: "События, опубликованные релеем outbox.",
})

// BacklogCollector отдаёт отставание outbox при каждом сборе метрик (ADR
// 022): сколько событий ещё не опубликовано и сколько ждёт самое старое.
// Растущее отставание — главный признак того, что выпуск билетов и
// возвраты встали, хотя продажа идёт.
type BacklogCollector struct {
	pool    *pgxpool.Pool
	pending *prometheus.Desc
	oldest  *prometheus.Desc
	up      *prometheus.Desc
}

// NewBacklogCollector создаёт сборщик отставания outbox.
func NewBacklogCollector(pool *pgxpool.Pool) *BacklogCollector {
	return &BacklogCollector{
		pool:    pool,
		pending: prometheus.NewDesc("dd_outbox_pending", "Неопубликованные события outbox.", nil, nil),
		oldest:  prometheus.NewDesc("dd_outbox_oldest_pending_seconds", "Возраст самого старого неопубликованного события.", nil, nil),
		up:      prometheus.NewDesc("dd_outbox_backlog_up", "1 — отставание удалось прочитать из базы.", nil, nil),
	}
}

// Describe реализует prometheus.Collector.
func (c *BacklogCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.pending
	ch <- c.oldest
	ch <- c.up
}

// Collect реализует prometheus.Collector. Запрос идёт по частичному индексу
// неопубликованных событий и ограничен двумя секундами.
func (c *BacklogCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var n int64
	var age float64
	err := c.pool.QueryRow(ctx, `SELECT count(*), coalesce(extract(epoch FROM now() - min(created_at)), 0)::float8
		FROM outbox WHERE published_at IS NULL`).Scan(&n, &age)
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(c.pending, prometheus.GaugeValue, float64(n))
	ch <- prometheus.MustNewConstMetric(c.oldest, prometheus.GaugeValue, age)
}
