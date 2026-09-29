// Package fakepsp — мок внешнего платёжного провайдера для разработки,
// демонстрации и тестов (ADR 012). Работает отдельным сервисом
// (cmd/fakepsp) и говорит с платформой так же, как настоящий провайдер:
// API создания платежа и возврата, своя страница оплаты, подписанные
// уведомления (вебхуки) с повторной доставкой.
//
// Карточных данных здесь нет вообще: на странице оплаты только кнопки
// «Оплатить» и «Отказать» (CLAUDE.md, правило 7). Состояние хранится в
// памяти и теряется при перезапуске — это мок, а не хранилище денег.
package fakepsp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config — настройки мока.
type Config struct {
	// PublicURL — адрес мока, открываемый в браузере покупателя.
	PublicURL string
	// APIKey — ключ, которым платформа подписывает запросы к API.
	APIKey string
	// WebhookSecret — секрет подписи уведомлений (HMAC-SHA256).
	WebhookSecret string
	// RetryDelays — паузы между повторами доставки уведомления.
	RetryDelays []time.Duration
	Client      *http.Client
	Log         *slog.Logger
}

// Server — мок провайдера.
type Server struct {
	cfg Config

	mu       sync.Mutex
	payments map[string]*Payment
	byMerch  map[string]string // merchant_payment_id → id
	refunds  map[string]*Refund
	wg       sync.WaitGroup
}

// Payment — платёж у провайдера.
type Payment struct {
	ID                string    `json:"id"`
	MerchantPaymentID string    `json:"merchant_payment_id"`
	Amount            int64     `json:"amount"`
	Currency          string    `json:"currency"`
	Description       string    `json:"description"`
	Status            string    `json:"status"` // pending, succeeded, failed
	PaymentURL        string    `json:"payment_url"`
	CreatedAt         time.Time `json:"created_at"`

	returnURL   string
	callbackURL string
	refunded    int64
}

// Refund — возврат у провайдера.
type Refund struct {
	ID               string `json:"id"`
	MerchantRefundID string `json:"merchant_refund_id"`
	PaymentID        string `json:"payment_id"`
	Amount           int64  `json:"amount"`
	Status           string `json:"status"`
}

// Event — уведомление о смене статуса платежа.
type Event struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"` // payment.succeeded, payment.failed
	Payment   Payment   `json:"payment"`
	CreatedAt time.Time `json:"created_at"`
}

// New создаёт мок.
func New(cfg Config) *Server {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.RetryDelays == nil {
		cfg.RetryDelays = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}
	}
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	return &Server{cfg: cfg, payments: map[string]*Payment{}, byMerch: map[string]string{}, refunds: map[string]*Refund{}}
}

// Sign — подпись тела уведомления: заголовок X-Signature: sha256=<hex>.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// Handler — HTTP-интерфейс мока.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /v1/payments", s.auth(s.handleCreatePayment))
	mux.HandleFunc("GET /v1/payments/{id}", s.auth(s.handleGetPayment))
	mux.HandleFunc("POST /v1/refunds", s.auth(s.handleCreateRefund))
	mux.HandleFunc("GET /pay/{id}", s.handlePage)
	mux.HandleFunc("POST /pay/{id}", s.handleDecision)
	return mux
}

// Wait дожидается фоновых повторов доставки (для остановки и тестов).
func (s *Server) Wait() { s.wg.Wait() }

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !hmac.Equal([]byte(got), []byte(s.cfg.APIKey)) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid api key"})
			return
		}
		h(w, r)
	}
}

type createPaymentRequest struct {
	MerchantPaymentID string `json:"merchant_payment_id"`
	Amount            int64  `json:"amount"`
	Currency          string `json:"currency"`
	Description       string `json:"description"`
	ReturnURL         string `json:"return_url"`
	CallbackURL       string `json:"callback_url"`
}

func (s *Server) handleCreatePayment(w http.ResponseWriter, r *http.Request) {
	var req createPaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.MerchantPaymentID == "" || req.Amount <= 0 || !validURL(req.ReturnURL) || !validURL(req.CallbackURL) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "merchant_payment_id, positive amount, return_url and callback_url are required"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Повтор создания с тем же merchant_payment_id возвращает тот же платёж.
	if id, ok := s.byMerch[req.MerchantPaymentID]; ok {
		writeJSON(w, http.StatusOK, s.payments[id])
		return
	}
	id := "pay_" + randomID()
	p := &Payment{
		ID: id, MerchantPaymentID: req.MerchantPaymentID, Amount: req.Amount, Currency: req.Currency,
		Description: req.Description, Status: "pending", PaymentURL: s.cfg.PublicURL + "/pay/" + id,
		CreatedAt: time.Now().UTC(), returnURL: req.ReturnURL, callbackURL: req.CallbackURL,
	}
	s.payments[id] = p
	s.byMerch[req.MerchantPaymentID] = id
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleGetPayment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	p, ok := s.payments[r.PathValue("id")]
	var cp Payment
	if ok {
		cp = *p
	}
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "payment not found"})
		return
	}
	writeJSON(w, http.StatusOK, cp)
}

type createRefundRequest struct {
	MerchantRefundID string `json:"merchant_refund_id"`
	PaymentID        string `json:"payment_id"`
	Amount           int64  `json:"amount"`
}

func (s *Server) handleCreateRefund(w http.ResponseWriter, r *http.Request) {
	var req createRefundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MerchantRefundID == "" || req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "merchant_refund_id and positive amount are required"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Повтор возврата с тем же merchant_refund_id не возвращает деньги дважды.
	if rf, ok := s.refunds[req.MerchantRefundID]; ok {
		writeJSON(w, http.StatusOK, rf)
		return
	}
	p, ok := s.payments[req.PaymentID]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "payment not found"})
		return
	}
	if p.Status != "succeeded" || p.refunded+req.Amount > p.Amount {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "refund exceeds captured amount"})
		return
	}
	p.refunded += req.Amount
	rf := &Refund{ID: "rf_" + randomID(), MerchantRefundID: req.MerchantRefundID, PaymentID: p.ID, Amount: req.Amount, Status: "succeeded"}
	s.refunds[req.MerchantRefundID] = rf
	writeJSON(w, http.StatusCreated, rf)
}

// Refunded — сколько возвращено по платежу (для тестов).
func (s *Server) Refunded(paymentID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.payments[paymentID]; ok {
		return p.refunded
	}
	return 0
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	p, ok := s.payments[r.PathValue("id")]
	var cp Payment
	if ok {
		cp = *p
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "платёж не найден", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTmpl.Execute(w, pageData{Payment: cp, AmountText: formatAmount(cp.Amount, cp.Currency)}); err != nil {
		s.cfg.Log.Warn("render payment page", slog.Any("error", err))
	}
}

// Действия на странице оплаты.
const (
	ActionSucceed      = "succeed"
	ActionFail         = "fail"
	ActionSucceedTwice = "succeed_twice" // уведомление приходит дважды — проверка идемпотентности
)

func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	action := r.FormValue("action")
	ev, returnURL, err := s.Decide(r.Context(), id, action)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	u, _ := url.Parse(returnURL)
	q := u.Query()
	q.Set("payment_id", id)
	q.Set("status", ev.Payment.Status)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

// Decide проводит решение покупателя по платежу: первая попытка доставки
// уведомления синхронная, повторы при неудаче — в фоне.
func (s *Server) Decide(ctx context.Context, id, action string) (Event, string, error) {
	s.mu.Lock()
	p, ok := s.payments[id]
	if !ok {
		s.mu.Unlock()
		return Event{}, "", errors.New("платёж не найден")
	}
	if p.Status != "pending" {
		s.mu.Unlock()
		return Event{}, "", fmt.Errorf("платёж уже в статусе %s", p.Status)
	}
	switch action {
	case ActionSucceed, ActionSucceedTwice:
		p.Status = "succeeded"
	case ActionFail:
		p.Status = "failed"
	default:
		s.mu.Unlock()
		return Event{}, "", errors.New("неизвестное действие")
	}
	ev := Event{ID: "evt_" + randomID(), Type: "payment." + p.Status, Payment: *p, CreatedAt: time.Now().UTC()}
	callback, returnURL := p.callbackURL, p.returnURL
	s.mu.Unlock()

	body, _ := json.Marshal(ev)
	times := 1
	if action == ActionSucceedTwice {
		times = 2
	}
	for range times {
		if !s.deliver(ctx, callback, ev.ID, body) {
			s.wg.Add(1)
			// Повторы переживают запрос покупателя, но не его значения.
			go s.retry(context.WithoutCancel(ctx), callback, ev.ID, body)
			break
		}
	}
	return ev, returnURL, nil
}

func (s *Server) retry(ctx context.Context, callback, eventID string, body []byte) {
	defer s.wg.Done()
	for _, d := range s.cfg.RetryDelays {
		time.Sleep(d)
		if s.deliver(ctx, callback, eventID, body) {
			return
		}
	}
	s.cfg.Log.Error("webhook delivery gave up", slog.String("event_id", eventID))
}

// deliver отправляет уведомление и сообщает, принято ли оно (2xx).
func (s *Server) deliver(ctx context.Context, callback, eventID string, body []byte) bool {
	req, err := http.NewRequestWithContext(context.WithoutCancel(ctx), http.MethodPost, callback, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-Id", eventID)
	req.Header.Set("X-Signature", Sign(s.cfg.WebhookSecret, body))
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		s.cfg.Log.Warn("webhook delivery failed", slog.String("event_id", eventID), slog.Any("error", err))
		return false
	}
	_ = resp.Body.Close()
	ok := resp.StatusCode/100 == 2
	if !ok {
		s.cfg.Log.Warn("webhook rejected", slog.String("event_id", eventID), slog.Int("status", resp.StatusCode))
	}
	return ok
}

func validURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func randomID() string { return strings.ToLower(rand.Text()[:16]) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// formatAmount: 500000 тиын → «5 000,00 ₸».
func formatAmount(minor int64, currency string) string {
	major, rest := minor/100, minor%100
	s := fmt.Sprint(major)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(c)
	}
	sign := currency
	if currency == "KZT" {
		sign = "₸"
	}
	return fmt.Sprintf("%s,%02d %s", b.String(), rest, sign)
}
