package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// HTTPMetrics — метрики «частота, ошибки, длительность» по маршрутам
// (ADR 022): сколько запросов, с каким статусом и как долго. Маршрут — шаблон
// chi (/v1/orders/{orderID}), а не путь: иначе каждый id стал бы отдельным
// рядом в Prometheus.
type HTTPMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inflight prometheus.Gauge
}

// NewHTTPMetrics регистрирует метрики в reg.
func NewHTTPMetrics(reg prometheus.Registerer) *HTTPMetrics {
	m := &HTTPMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dd_http_requests_total",
			Help: "HTTP-запросы по маршруту, методу и коду ответа.",
		}, []string{"route", "method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "dd_http_request_duration_seconds",
			Help:    "Длительность HTTP-запроса по маршруту и методу.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}, []string{"route", "method"}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dd_http_requests_in_flight",
			Help: "Запросы, которые обрабатываются прямо сейчас.",
		}),
	}
	reg.MustRegister(m.requests, m.duration, m.inflight)
	return m
}

// Middleware записывает метрики каждого запроса. Ставится на корневой
// роутер chi: шаблон маршрута известен после обработки запроса.
func (m *HTTPMetrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		m.inflight.Inc()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		defer func() {
			m.inflight.Dec()
			route := "unmatched"
			if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
				route = rc.RoutePattern()
			}
			m.requests.WithLabelValues(route, r.Method, strconv.Itoa(sw.code)).Inc()
			m.duration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
		}()
		next.ServeHTTP(sw, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code    int
	written bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.written {
		w.code, w.written = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(b)
}

// Unwrap даёт http.ResponseController доступ к исходному writer (Flush и др.).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
