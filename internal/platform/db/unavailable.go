package db

import (
	"errors"
	"io"
	"net"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

// Unavailable — ошибка значит «база временно недоступна», а не «запрос
// неверен»: ведущий узел упал, реплика ещё не повышена и принимает только
// чтение, соединение оборвалось (ADR 028). На такие ошибки api отвечает
// 503 с Retry-After: повтор того же запроса скоро пройдёт.
func Unavailable(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := errors.AsType[*pgconn.ConnectError](err); ok {
		return true
	}
	if pe, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch {
		case pe.Code == "25006": // read_only_sql_transaction: попали на реплику
			return true
		case strings.HasPrefix(pe.Code, "08"): // connection_exception
			return true
		case pe.Code == "57P01", pe.Code == "57P02", pe.Code == "57P03": // остановка сервера, нельзя подключиться
			return true
		}
		return false
	}
	if pgconn.SafeToRetry(err) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, net.ErrClosed) {
		return true
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}
	// pgx сообщает об оборванном соединении строкой «conn closed».
	return strings.Contains(err.Error(), "conn closed")
}
