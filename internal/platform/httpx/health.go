package httpx

import (
	"context"
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
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz параллельно выполняет проверки. Без обязательной зависимости
// процесс работать не может: её сбой — 503, и балансировщик или оркестратор
// выводит экземпляр из ротации. Без необязательной процесс работает хуже, но
// работает (например, api без Redis, ADR 018): её сбой — 200 со статусом
// degraded. Иначе отказ общей зависимости вывел бы из ротации все экземпляры
// сразу (ADR 030). Подробности ошибок пишутся в лог, а не в ответ.
func Readyz(required, optional map[string]Check) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
		defer cancel()

		var (
			mu       sync.Mutex
			wg       sync.WaitGroup
			results  = make(map[string]string, len(required)+len(optional))
			ready    = true
			degraded = false
		)
		run := func(name string, check Check, mandatory bool) {
			err := check(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if mandatory {
					ready = false
				} else {
					degraded = true
				}
				results[name] = "fail"
				Logger(ctx).Warn("readiness check failed", slog.String("check", name),
					slog.Bool("required", mandatory), slog.Any("error", err))
				return
			}
			results[name] = "ok"
		}
		for name, check := range required {
			wg.Go(func() { run(name, check, true) })
		}
		for name, check := range optional {
			wg.Go(func() { run(name, check, false) })
		}
		wg.Wait()

		status, code := "ok", http.StatusOK
		switch {
		case !ready:
			status, code = "unavailable", http.StatusServiceUnavailable
		case degraded:
			status = "degraded"
		}
		WriteJSON(w, code, map[string]any{"status": status, "checks": results})
	}
}
