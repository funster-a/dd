package mq

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Метрики обработчиков очередей (ADR 022): сколько сообщений обработано,
// сколько ушло в очередь недоставленных и как долго длилась обработка.
var (
	handled = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dd_mq_messages_total",
		Help: "Обработанные сообщения по очереди и результату (ok, error — сообщение ушло в очередь недоставленных).",
	}, []string{"queue", "result"})
	handleTime = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dd_mq_handle_seconds",
		Help:    "Время обработки сообщения.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"queue"})
)

func result(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}
