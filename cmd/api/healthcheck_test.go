package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzURL(t *testing.T) {
	tests := map[string]string{
		":8080":          "http://127.0.0.1:8080/healthz",
		"0.0.0.0:8080":   "http://127.0.0.1:8080/healthz",
		"[::]:8080":      "http://127.0.0.1:8080/healthz",
		"localhost:9000": "http://localhost:9000/healthz",
		"10.0.0.5:8080":  "http://10.0.0.5:8080/healthz",
		"[::1]:8080":     "http://[::1]:8080/healthz",
	}
	for addr, want := range tests {
		got, err := healthzURL(addr)
		if err != nil {
			t.Errorf("healthzURL(%q) error = %v", addr, err)
			continue
		}
		if got != want {
			t.Errorf("healthzURL(%q) = %q, want %q", addr, got, want)
		}
	}

	if _, err := healthzURL("8080"); err == nil {
		t.Error(`healthzURL("8080") error = nil, want error`)
	}
}

func TestHealthcheck(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"healthy", http.StatusOK, false},
		{"unhealthy", http.StatusServiceUnavailable, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
			err := healthcheck(":" + port)
			if (err != nil) != tt.wantErr {
				t.Errorf("healthcheck() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}

	t.Run("nothing listening", func(t *testing.T) {
		l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close()
		if err := healthcheck(addr); err == nil {
			t.Error("healthcheck() error = nil, want connection error")
		}
	})
}
