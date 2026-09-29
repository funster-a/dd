package booking

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// ValidationError — неверное значение поля во входных данных. HTTP 400.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Message) }

// ConflictError — места уже заняты или заказ меняется параллельно. HTTP 409.
type ConflictError struct {
	Code    string
	Message string
}

func (e *ConflictError) Error() string { return e.Message }

// PreconditionError — действие невозможно в текущем состоянии события или
// заказа (продажи закрыты, превышен лимит). HTTP 422.
type PreconditionError struct {
	Code    string
	Message string
}

func (e *PreconditionError) Error() string { return e.Message }

// ErrNotFound — записи нет или она чужая. Эти случаи не различаются.
var ErrNotFound = errors.New("not found")

func pgCode(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code
	}
	return ""
}

func uniqueConstraint(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}
	return ""
}
