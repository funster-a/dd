// Package db создаёт пул соединений с PostgreSQL.
package db

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool создаёт пул. Соединения открываются лениво, поэтому процесс
// стартует и при недоступной базе; готовность проверяет /readyz.
//
// timestamptz читается в UTC независимо от часового пояса процесса:
// время хранится и передаётся в UTC (CLAUDE.md, правило 6), а pgx по
// умолчанию отдаёт его в time.Local.
func NewPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	return newPool(ctx, url, false)
}

// NewReplicaPool — пул к репликам (ADR 031). pgx подключается к первому
// подходящему хосту из списка, и все соединения шли бы на одну реплику, пока
// остальные простаивают. Поэтому каждое новое соединение начинает перебор со
// следующего хоста: соединения пула делятся между репликами поровну.
func NewReplicaPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	return newPool(ctx, url, true)
}

func newPool(ctx context.Context, url string, rotate bool) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if rotate {
		groups := hostGroups(cfg.ConnConfig)
		var next atomic.Uint64
		cfg.BeforeConnect = func(_ context.Context, c *pgx.ConnConfig) error {
			setHosts(c, rotated(groups, int(next.Add(1)%uint64(len(groups))))) //nolint:gosec // остаток меньше числа хостов
			return nil
		}
	}
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{
			Name: "timestamptz", OID: pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
		})
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	return pool, nil
}

// hostGroups — хосты из настроек по порядку; записи одного хоста идут
// группой: при sslmode=prefer pgx пробует каждый хост сначала с TLS, потом без.
func hostGroups(c *pgx.ConnConfig) [][]*pgconn.FallbackConfig {
	all := append([]*pgconn.FallbackConfig{{Host: c.Host, Port: c.Port, TLSConfig: c.TLSConfig}}, c.Fallbacks...)
	var groups [][]*pgconn.FallbackConfig
	for _, h := range all {
		if n := len(groups); n > 0 && groups[n-1][0].Host == h.Host && groups[n-1][0].Port == h.Port {
			groups[n-1] = append(groups[n-1], h)
			continue
		}
		groups = append(groups, []*pgconn.FallbackConfig{h})
	}
	return groups
}

// rotated — хосты, начиная с группы start: перебор идёт с неё, остальные —
// запасные по кругу.
func rotated(groups [][]*pgconn.FallbackConfig, start int) []*pgconn.FallbackConfig {
	return slices.Concat(slices.Concat(groups[start:], groups[:start])...)
}

func setHosts(c *pgx.ConnConfig, hosts []*pgconn.FallbackConfig) {
	c.Host, c.Port, c.TLSConfig = hosts[0].Host, hosts[0].Port, hosts[0].TLSConfig
	c.Fallbacks = hosts[1:]
}
