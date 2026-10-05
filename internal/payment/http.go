package payment

import (
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/funster-a/dd/internal/platform/auth"
	pgdb "github.com/funster-a/dd/internal/platform/db"
	"github.com/funster-a/dd/internal/platform/httpx"
	"github.com/funster-a/dd/internal/platform/idempotency"
)

// Register добавляет маршруты оплаты в роутер /v1:
//
//	POST /orders/{orderID}/payments        — страница оплаты заказа покупателя
//	POST /payments/webhooks/{provider}     — уведомления провайдера (подпись вместо входа)
func (s *Service) Register(r chi.Router) {
	r.Post("/payments/webhooks/{provider}", s.handleWebhook)
	r.With(auth.Require(auth.KindBuyer), idempotency.Middleware(s.pool)).
		Post("/orders/{orderID}/payments", s.handleStartPayment)
}

func (s *Service) handleStartPayment(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	pay, err := s.StartPayment(r.Context(), p.SubjectID, chi.URLParam(r, "orderID"), time.Now())
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			httpx.WriteError(w, http.StatusNotFound, "not_found", "not found")
		case errors.Is(err, ErrProviderUnavailable):
			httpx.WriteError(w, http.StatusBadGateway, "payment_provider_unavailable", err.Error())
		default:
			if pe, ok := errors.AsType[*PreconditionError](err); ok {
				httpx.WriteError(w, http.StatusUnprocessableEntity, pe.Code, pe.Message)
				return
			}
			if pgdb.Unavailable(err) {
				httpx.WriteUnavailable(w, r, err)
				return
			}
			httpx.Logger(r.Context()).Error("start payment failed", slog.Any("error", err))
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		}
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, pay)
}

// Тело уведомления больше этого не бывает.
const maxWebhookBody = 64 << 10

func (s *Service) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "invalid_request", "body too large")
		return
	}
	err = s.HandleWebhook(r.Context(), chi.URLParam(r, "provider"), r.Header, body, time.Now())
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", "unknown provider")
	case errors.Is(err, ErrBadSignature):
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_signature", err.Error())
	default:
		// 5xx: провайдер доставит уведомление повторно.
		if pgdb.Unavailable(err) {
			httpx.WriteUnavailable(w, r, err)
			return
		}
		httpx.Logger(r.Context()).Error("payment webhook failed", slog.Any("error", err))
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// HandleReturn — страница, куда провайдер возвращает покупателя. Статус из
// адреса только для текста: истина об оплате приходит уведомлением.
func HandleReturn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = returnTmpl.Execute(w, struct{ OrderID, Status string }{r.URL.Query().Get("order_id"), r.URL.Query().Get("status")})
}

var returnTmpl = template.Must(template.New("return").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Оплата заказа</title>
<style>body{font-family:system-ui,sans-serif;background:#f4f4f5;margin:0;padding:16px;color:#18181b}
.card{max-width:420px;margin:40px auto;background:#fff;border-radius:12px;padding:24px;box-shadow:0 1px 3px rgba(0,0,0,.1)}
.muted{color:#71717a;font-size:14px}</style></head>
<body><div class="card">
{{if eq .Status "succeeded"}}<h2>Спасибо, оплата принята</h2>
<p>Билеты придут на e-mail в течение минуты.</p>
{{else}}<h2>Оплата не прошла</h2>
<p>Места держатся за заказом ещё несколько минут — можно попробовать снова.</p>{{end}}
<p class="muted">Заказ {{.OrderID}}</p>
</div></body></html>
`))
