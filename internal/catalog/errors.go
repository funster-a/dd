package catalog

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// ValidationError — неверное значение поля во входных данных.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// ConflictError — запись с таким значением уже есть.
type ConflictError struct {
	Code    string
	Message string
}

func (e *ConflictError) Error() string { return e.Message }

// uniqueConstraint возвращает имя нарушенного уникального ограничения или "".
func uniqueConstraint(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}
	return ""
}
