package booking

import (
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Метрики захвата мест (ADR 017): по ним считаются доля отказов, повторов и
// задержка каждой стратегии в эксперименте и видно поведение в проде.
type holdMetrics struct {
	attempts *prometheus.CounterVec
	retries  *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newHoldMetrics(reg prometheus.Registerer) *holdMetrics {
	m := &holdMetrics{
		attempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dd_booking_attempts_total",
			Help: "Попытки оформить заказ на места: результат и кто отказал (redis, db).",
		}, []string{"strategy", "result", "reject_by"}),
		retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dd_booking_retries_total",
			Help: "Повторы после конфликта версий (оптимистичная стратегия).",
		}, []string{"strategy"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "dd_booking_attempt_seconds",
			Help:    "Время попытки оформить заказ на места.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"strategy", "result"}),
	}
	reg.MustRegister(m.attempts, m.retries, m.duration)
	return m
}

// defaultMetrics регистрируется в общем реестре Prometheus один раз на процесс.
var defaultMetrics = newHoldMetrics(prometheus.DefaultRegisterer)

// observe записывает итог попытки.
func (m *holdMetrics) observe(st Strategy, a *attempt, err error, d time.Duration) {
	result := "ok"
	if err != nil {
		result = "error"
		if c, ok := errors.AsType[*ConflictError](err); ok {
			result = c.Code
		} else if p, ok := errors.AsType[*PreconditionError](err); ok {
			result = p.Code
		} else if _, ok := errors.AsType[*ValidationError](err); ok || errors.Is(err, ErrNotFound) {
			result = "invalid"
		}
	}
	m.attempts.WithLabelValues(string(st), result, a.rejectBy).Inc()
	if a.retries > 0 {
		m.retries.WithLabelValues(string(st)).Add(float64(a.retries))
	}
	m.duration.WithLabelValues(string(st), result).Observe(d.Seconds())
}
