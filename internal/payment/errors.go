package payment

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// PreconditionError — оплатить нельзя в текущем состоянии заказа. HTTP 422.
type PreconditionError struct {
	Code    string
	Message string
}

func (e *PreconditionError) Error() string { return e.Message }

// ErrNotFound — заказа нет или он чужой.
var ErrNotFound = errors.New("not found")

// ErrProviderUnavailable — провайдер не ответил или ответил ошибкой. HTTP 502.
var ErrProviderUnavailable = errors.New("payment provider is unavailable")

func uniqueConstraint(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}
	return ""
}
