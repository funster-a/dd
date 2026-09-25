package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const checkTimeout = 2 * time.Second

// Check проверяет доступность одной зависимости.
type Check func(ctx context.Context) error

// Healthz отвечает 200, пока процесс жив. Зависимости не проверяет.
func Healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz параллельно выполняет проверки и отвечает 200, если все прошли,
// иначе 503. Подробности ошибок пишутся в лог, а не в ответ.
func Readyz(checks map[string]Check) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
		defer cancel()

		var (
			mu      sync.Mutex
			wg      sync.WaitGroup
			results = make(map[string]string, len(checks))
			ready   = true
		)
		for name, check := range checks {
			wg.Go(func() {
				err := check(ctx)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					ready = false
					results[name] = "fail"
					Logger(r.Context()).Warn("readiness check failed", slog.String("check", name), slog.Any("error", err))
					return
				}
				results[name] = "ok"
			})
		}
		wg.Wait()

		status, code := "ok", http.StatusOK
		if !ready {
			status, code = "unavailable", http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]any{"status": status, "checks": results})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
