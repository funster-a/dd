// Package db создаёт пул соединений с PostgreSQL.
package db

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
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

// NewReplicaPool — пул к репликам (ADR 031). Порядок хостов из url
// перемешивается: pgx подключается к первому подходящему хосту, и без этого
// все экземпляры api читали бы с одной реплики, пока остальные простаивают.
func NewReplicaPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	return newPool(ctx, url, true)
}

func newPool(ctx context.Context, url string, shuffle bool) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if shuffle {
		shuffleHosts(cfg.ConnConfig)
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

// shuffleHosts перемешивает хосты, сохраняя порядок записей одного хоста:
// при sslmode=prefer pgx пробует каждый хост сначала с TLS, потом без.
func shuffleHosts(c *pgx.ConnConfig) {
	all := append([]*pgconn.FallbackConfig{{Host: c.Host, Port: c.Port, TLSConfig: c.TLSConfig}}, c.Fallbacks...)
	var groups [][]*pgconn.FallbackConfig
	for _, h := range all {
		if n := len(groups); n > 0 && groups[n-1][0].Host == h.Host && groups[n-1][0].Port == h.Port {
			groups[n-1] = append(groups[n-1], h)
			continue
		}
		groups = append(groups, []*pgconn.FallbackConfig{h})
	}
	rand.Shuffle(len(groups), func(i, j int) { groups[i], groups[j] = groups[j], groups[i] })
	all = slices.Concat(groups...)
	c.Host, c.Port, c.TLSConfig = all[0].Host, all[0].Port, all[0].TLSConfig
	c.Fallbacks = all[1:]
}
