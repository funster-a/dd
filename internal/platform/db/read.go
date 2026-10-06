package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reader — источник чтений, которым не нужна свежесть до миллисекунды:
// занятость мест, список событий (ADR 031). Читает с реплики, а если реплика
// недоступна — с ведущего узла: отказ реплик не останавливает продажу, только
// возвращает нагрузку на ведущий. Писать через Reader нельзя: на реплике
// запись упадёт с «read-only transaction».
//
// Реплика отстаёт от ведущего на миллисекунды. Поэтому через Reader не
// читают то, что пользователь только что записал сам (свой заказ), и то, по
// чему принимается решение (проверки заказа идут в транзакции на ведущем).
type Reader struct {
	replica, primary *pgxpool.Pool
}

// NewReader читает с replica, при её отказе — с primary. replica == nil —
// всё с primary.
func NewReader(replica, primary *pgxpool.Pool) *Reader {
	return &Reader{replica: replica, primary: primary}
}

// Exec выполняет запрос на ведущем узле: запись на реплике невозможна.
func (r *Reader) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return r.primary.Exec(ctx, sql, args...)
}

// CopyFrom копирует строки на ведущий узел.
func (r *Reader) CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error) {
	return r.primary.CopyFrom(ctx, table, columns, src)
}

// Query читает с реплики, при её недоступности — с ведущего узла.
func (r *Reader) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if r.replica != nil {
		rows, err := r.replica.Query(ctx, sql, args...)
		if !Unavailable(err) {
			return rows, err
		}
	}
	return r.primary.Query(ctx, sql, args...)
}

// QueryRow читает строку с реплики, при её недоступности — с ведущего узла.
func (r *Reader) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if r.replica == nil {
		return r.primary.QueryRow(ctx, sql, args...)
	}
	return &fallbackRow{ctx: ctx, r: r, sql: sql, args: args}
}

// fallbackRow откладывает запрос до Scan, как pgx.Row, и повторяет его на
// ведущем, если реплика недоступна.
type fallbackRow struct {
	ctx  context.Context //nolint:containedctx // pgx.Row выполняет запрос в Scan
	r    *Reader
	sql  string
	args []any
}

func (f *fallbackRow) Scan(dest ...any) error {
	err := f.r.replica.QueryRow(f.ctx, f.sql, f.args...).Scan(dest...)
	if Unavailable(err) {
		return f.r.primary.QueryRow(f.ctx, f.sql, f.args...).Scan(dest...)
	}
	return err
}
