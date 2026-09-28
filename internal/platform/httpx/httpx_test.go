package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIDGenerated(t *testing.T) {
	var fromCtx string
	h := RequestID(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		fromCtx = RequestIDFrom(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	got := rec.Header().Get(RequestIDHeader)
	if got == "" {
		t.Fatal("response has no request id")
	}
	if fromCtx != got {
		t.Errorf("context id = %q, header id = %q", fromCtx, got)
	}
}

func TestRequestIDPropagated(t *testing.T) {
	h := RequestID(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "abc-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get(RequestIDHeader); got != "abc-123" {
		t.Errorf("request id = %q, want abc-123", got)
	}
}

func TestRequestIDRejectsUnsafe(t *testing.T) {
	h := RequestID(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for _, bad := range []string{"has space", "line\nbreak", `quote"`, strings.Repeat("a", maxRequestIDLen+1)} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		req.Header.Set(RequestIDHeader, bad)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := rec.Header().Get(RequestIDHeader); got == bad || got == "" {
			t.Errorf("unsafe id %q: got %q, want a generated one", bad, got)
		}
	}
}

func TestRequestIDInLogs(t *testing.T) {
	var buf strings.Builder
	base := slog.New(slog.NewJSONHandler(&buf, nil))
	h := RequestID(base)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		Logger(r.Context()).Info("hello")
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "log-me")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !strings.Contains(buf.String(), `"request_id":"log-me"`) {
		t.Errorf("log line has no request_id: %s", buf.String())
	}
}

func TestReadyz(t *testing.T) {
	ok := func(context.Context) error { return nil }
	fail := func(context.Context) error { return errors.New("down") }

	tests := []struct {
		name     string
		checks   map[string]Check
		wantCode int
		wantFail string
	}{
		{"all ok", map[string]Check{"a": ok, "b": ok}, http.StatusOK, ""},
		{"one failed", map[string]Check{"a": ok, "b": fail}, http.StatusServiceUnavailable, "b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Readyz(tt.checks)(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

			if rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d", rec.Code, tt.wantCode)
			}
			var body struct {
				Checks map[string]string `json:"checks"`
			}
			raw, _ := io.ReadAll(rec.Body)
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("decode body %q: %v", raw, err)
			}
			for name := range tt.checks {
				want := "ok"
				if name == tt.wantFail {
					want = "fail"
				}
				if body.Checks[name] != want {
					t.Errorf("check %q = %q, want %q", name, body.Checks[name], want)
				}
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	Healthz(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
}
