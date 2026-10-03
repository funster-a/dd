package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestHTTPMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTPMetrics(reg)
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/v1/orders/{orderID}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	for _, p := range []string{"/v1/orders/1", "/v1/orders/2", "/ok", "/nowhere"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, p, nil))
	}

	want := `
# HELP dd_http_requests_total HTTP-запросы по маршруту, методу и коду ответа.
# TYPE dd_http_requests_total counter
dd_http_requests_total{code="200",method="GET",route="/ok"} 1
dd_http_requests_total{code="404",method="GET",route="/v1/orders/{orderID}"} 2
dd_http_requests_total{code="404",method="GET",route="unmatched"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "dd_http_requests_total"); err != nil {
		t.Error(err)
	}
	if n := testutil.CollectAndCount(m.duration); n != 3 {
		t.Errorf("duration series = %d, want 3 (one per route)", n)
	}
}
