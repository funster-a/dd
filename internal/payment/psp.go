package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PSPClient — клиент протокола провайдера fakepsp (ADR 012): REST API с
// ключом в Authorization и уведомления с подписью HMAC-SHA256.
type PSPClient struct {
	BaseURL       string
	APIKey        string
	WebhookSecret string
	HTTP          *http.Client
}

// NewPSPClient создаёт клиент с таймаутом запросов.
func NewPSPClient(baseURL, apiKey, webhookSecret string) *PSPClient {
	return &PSPClient{
		BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, WebhookSecret: webhookSecret,
		HTTP: &http.Client{Timeout: 10 * time.Second},
	}
}

// Name — имя провайдера.
func (c *PSPClient) Name() string { return "fakepsp" }

type pspPayment struct {
	ID                string `json:"id"`
	MerchantPaymentID string `json:"merchant_payment_id"`
	Amount            int64  `json:"amount"`
	Status            string `json:"status"`
	PaymentURL        string `json:"payment_url"`
}

// CreatePayment создаёт платёж у провайдера.
func (c *PSPClient) CreatePayment(ctx context.Context, req CreatePaymentRequest) (ProviderPayment, error) {
	var p pspPayment
	err := c.post(ctx, "/v1/payments", map[string]any{
		"merchant_payment_id": req.PaymentID, "amount": req.AmountTiyn, "currency": req.Currency,
		"description": req.Description, "return_url": req.ReturnURL, "callback_url": req.CallbackURL,
	}, &p)
	if err != nil {
		return ProviderPayment{}, err
	}
	return ProviderPayment{ID: p.ID, PaymentURL: p.PaymentURL}, nil
}

// Refund возвращает деньги по платежу.
func (c *PSPClient) Refund(ctx context.Context, req RefundRequest) (ProviderRefund, error) {
	var r struct {
		ID string `json:"id"`
	}
	err := c.post(ctx, "/v1/refunds", map[string]any{
		"merchant_refund_id": req.RefundID, "payment_id": req.ProviderPaymentID, "amount": req.AmountTiyn,
	}, &r)
	if err != nil {
		return ProviderRefund{}, err
	}
	return ProviderRefund{ID: r.ID}, nil
}

func (c *PSPClient) post(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	// Адрес провайдера — из конфигурации, не из запроса пользователя.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b)) //nolint:gosec // G704: см. выше
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req) //nolint:gosec // G704: адрес из конфигурации
	if err != nil {
		return fmt.Errorf("payment provider %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode / 100 {
	case 2:
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("payment provider %s: decode response: %w", path, err)
		}
		return nil
	case 4:
		return fmt.Errorf("%w: %s %s", ErrRejected, resp.Status, bytes.TrimSpace(data))
	default:
		return fmt.Errorf("payment provider %s: %s", path, resp.Status)
	}
}

// ParseWebhook проверяет подпись X-Signature и разбирает уведомление.
func (c *PSPClient) ParseWebhook(header http.Header, body []byte) (WebhookEvent, error) {
	m := hmac.New(sha256.New, []byte(c.WebhookSecret))
	m.Write(body)
	want := "sha256=" + hex.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(header.Get("X-Signature")), []byte(want)) {
		return WebhookEvent{}, ErrBadSignature
	}
	var ev struct {
		ID      string     `json:"id"`
		Type    string     `json:"type"`
		Payment pspPayment `json:"payment"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return WebhookEvent{}, fmt.Errorf("decode webhook: %w", err)
	}
	if ev.ID == "" || ev.Payment.MerchantPaymentID == "" || (ev.Type != EventSucceeded && ev.Type != EventFailed) {
		return WebhookEvent{}, fmt.Errorf("decode webhook: unexpected event %q", ev.Type)
	}
	return WebhookEvent{
		EventID: ev.ID, Type: ev.Type, PaymentID: ev.Payment.MerchantPaymentID,
		ProviderPaymentID: ev.Payment.ID, AmountTiyn: ev.Payment.Amount,
	}, nil
}
