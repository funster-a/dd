// Package auth описывает, кто выполняет запрос, и проверяет права на уровне
// HTTP. Сессии создаёт модуль identity; остальные модули только читают
// Principal из контекста, не завися от identity (ADR 006).
package auth

import (
	"context"
	"net/http"
	"slices"

	"github.com/funster-a/dd/internal/platform/httpx"
)

// Kind — вид участника.
type Kind string

// Виды участников (ADR 006).
const (
	KindBuyer     Kind = "buyer"
	KindOrganizer Kind = "organizer"
	KindAdmin     Kind = "admin"
)

// Principal — аутентифицированный участник запроса.
type Principal struct {
	Kind Kind `json:"kind"`
	// SubjectID — id покупателя, id участника организатора или email администратора.
	SubjectID string `json:"subject_id"`
	// OrganizerID заполнен только у организатора. Все запросы к данным
	// арендатора фильтруются по нему, а не по данным из запроса.
	OrganizerID string `json:"organizer_id,omitempty"`
}

type ctxKey struct{}

// WithPrincipal кладёт участника в контекст.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// PrincipalFrom возвращает участника из контекста.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Require пропускает запрос, только если участник аутентифицирован и его
// вид входит в kinds: иначе 401 без сессии и 403 при чужой роли.
func Require(kinds ...Kind) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
				return
			}
			if !slices.Contains(kinds, p.Kind) {
				httpx.WriteError(w, http.StatusForbidden, "forbidden", "not allowed for this account type")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
