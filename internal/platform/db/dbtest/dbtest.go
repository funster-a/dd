// Package dbtest поднимает для тестов отдельную временную базу PostgreSQL
// с применёнными миграциями и удаляет её после теста.
//
// Нужна переменная DATABASE_TEST_URL — строка подключения к серверу,
// где пользователю разрешено CREATE DATABASE. Без неё тест пропускается.
package dbtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // драйвер database/sql для goose
	"github.com/pressly/goose/v3"

	"github.com/funster-a/dd/internal/platform/db"
	"github.com/funster-a/dd/migrations"
)

// DB — временная база теста.
type DB struct {
	// Pool — пул соединений к временной базе.
	Pool *pgxpool.Pool
	// URL — строка подключения к временной базе.
	URL string
}

// New создаёт пустую базу, применяет к ней все миграции и регистрирует
// её удаление в t.Cleanup.
func New(t testing.TB) *DB {
	t.Helper()
	db := NewEmpty(t)
	provider := Provider(t, db.URL)
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

// NewEmpty создаёт базу без миграций — для тестов самих миграций.
func NewEmpty(t testing.TB) *DB {
	t.Helper()

	serverURL := os.Getenv("DATABASE_TEST_URL")
	if serverURL == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}

	ctx := t.Context()
	admin, err := pgx.Connect(ctx, serverURL)
	if err != nil {
		t.Fatalf("connect to test server: %v", err)
	}
	defer func() { _ = admin.Close(context.Background()) }()

	// Имя из base32-алфавита, в идентификаторе оно безопасно без кавычек.
	name := "dd_test_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	dbURL, err := withDatabase(serverURL, name)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.NewPool(ctx, dbURL)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, serverURL)
		if err != nil {
			t.Errorf("drop test database %s: %v", name, err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database %s: %v", name, err)
		}
	})

	return &DB{Pool: pool, URL: dbURL}
}

// Provider возвращает goose-провайдер миграций для базы по адресу dbURL.
func Provider(t testing.TB, dbURL string) *goose.Provider {
	t.Helper()
	sqlDB, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open database/sql: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	return provider
}

func withDatabase(serverURL, name string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		// Без исходной строки: в ней пароль.
		return "", fmt.Errorf("parse DATABASE_TEST_URL: invalid URL")
	}
	u.Path = "/" + name
	return u.String(), nil
}
