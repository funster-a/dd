package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestUnavailable(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"nil":            {nil, false},
		"read only":      {&pgconn.PgError{Code: "25006"}, true},
		"admin shutdown": {fmt.Errorf("wrap: %w", &pgconn.PgError{Code: "57P01"}), true},
		"connection":     {&pgconn.PgError{Code: "08006"}, true},
		"unique":         {&pgconn.PgError{Code: "23505"}, false},
		"eof":            {fmt.Errorf("read: %w", io.ErrUnexpectedEOF), true},
		"refused":        {fmt.Errorf("dial: %w", syscall.ECONNREFUSED), true},
		"conn closed":    {errors.New("failed to deallocate cached statement(s): conn closed"), true},
		"canceled":       {context.Canceled, false},
		"plain":          {errors.New("boom"), false},
	}
	for name, tc := range cases {
		if got := Unavailable(tc.err); got != tc.want {
			t.Errorf("%s: Unavailable() = %v, want %v", name, got, tc.want)
		}
	}
	// Ошибка подключения ко всем узлам — недоступна.
	if _, err := pgconn.Connect(context.Background(), "postgres://u@127.0.0.1:1/db?connect_timeout=1"); !Unavailable(err) {
		t.Errorf("connect error %v is not unavailable", err)
	}
}
