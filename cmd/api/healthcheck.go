package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/funster-a/dd/internal/platform/config"
)

const healthcheckTimeout = 3 * time.Second

func runHealthcheck() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	return healthcheck(cfg.HTTPAddr)
}

// healthcheck запрашивает /healthz у api, запущенного в этом же контейнере.
// Нужен для HEALTHCHECK в образе distroless, где нет shell и curl.
func healthcheck(addr string) error {
	url, err := healthzURL(addr)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return nil
}

// healthzURL превращает адрес прослушивания (":8080", "0.0.0.0:8080")
// в адрес, по которому к серверу можно обратиться изнутри контейнера.
func healthzURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("parse HTTP_ADDR %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}
