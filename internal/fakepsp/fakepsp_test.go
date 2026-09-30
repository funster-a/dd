package fakepsp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Мок помнит платежи после перезапуска, как настоящий провайдер: возврат по
// оплате, принятой до перезапуска, проходит.
func TestStatePersistsAcrossRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer hook.Close()
	cfg := Config{APIKey: "k", WebhookSecret: "s", StateFile: file, RetryDelays: []time.Duration{}}

	first := New(cfg)
	srv := httptest.NewServer(first.Handler())
	body := `{"merchant_payment_id":"m1","amount":1000,"currency":"KZT","return_url":"http://x/r","callback_url":"` + hook.URL + `"}`
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/v1/payments", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	var id string
	for pid := range first.payments {
		id = pid
	}
	if _, _, err := first.Decide(context.Background(), id, ActionSucceed); err != nil {
		t.Fatal(err)
	}
	srv.Close()

	second := New(cfg)
	srv2 := httptest.NewServer(second.Handler())
	defer srv2.Close()
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, srv2.URL+"/v1/refunds",
		strings.NewReader(`{"merchant_refund_id":"r1","payment_id":"`+id+`","amount":1000}`))
	req.Header.Set("Authorization", "Bearer k")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || second.Refunded(id) != 1000 {
		t.Errorf("refund after restart = %d, refunded %d", resp.StatusCode, second.Refunded(id))
	}
	// Повтор создания с тем же merchant_payment_id после перезапуска — тот же платёж.
	if len(second.byMerch) != 1 {
		t.Errorf("merchant index not restored: %v", second.byMerch)
	}
}
