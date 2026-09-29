// Command fakepsp запускает мок внешнего платёжного провайдера (ADR 012).
// Только для разработки и демонстрации: карточных данных он не принимает.
//
// Использование:
//
//	fakepsp              — запустить сервер
//	fakepsp healthcheck  — проверить /healthz запущенного сервера (для Docker)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/funster-a/dd/internal/fakepsp"
	"github.com/funster-a/dd/internal/platform/observability"
)

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := getenv("FAKEPSP_ADDR", ":8090")
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(addr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	log := observability.NewLogger(os.Stdout, slog.LevelInfo, "fakepsp")
	if err := run(addr, log); err != nil {
		log.Error("fakepsp stopped with error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(addr string, log *slog.Logger) error {
	psp := fakepsp.New(fakepsp.Config{
		PublicURL:     getenv("FAKEPSP_PUBLIC_URL", "http://localhost:8090"),
		APIKey:        getenv("PSP_API_KEY", "dev-psp-api-key"),
		WebhookSecret: getenv("PSP_WEBHOOK_SECRET", "dev-psp-webhook-secret"),
		Log:           log,
	})
	srv := &http.Server{Addr: addr, Handler: psp.Handler(), ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		log.Info("fake payment provider started", slog.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func healthcheck(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse FAKEPSP_ADDR: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}
