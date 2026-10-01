// Команда loadseed готовит и проверяет нагрузочный эксперимент со
// стратегиями захвата мест (spec.md, «Инженерное ядро»; ADR 017).
//
//	loadseed buyers -n 5000 -out buyers.json   покупатели с сессиями
//	loadseed event -out event.json             новое событие с залом
//	loadseed check -event <id>                 инвариант после прогона
//	loadseed report -dir <results>             таблица и графики
//
// Покупатели и события создаются кодом модулей — тем же, что в продукте.
// Инструмент работает рядом с базой и Redis и не публикуется.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/funster-a/dd/internal/platform/config"
	"github.com/funster-a/dd/internal/platform/db"
	"github.com/funster-a/dd/internal/platform/redis"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: loadseed buyers|event|check|report [flags]")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	err := run(ctx, os.Args[1], os.Args[2:])
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadseed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd string, args []string) error {
	if cmd == "report" {
		return report(args)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	log := slog.New(slog.DiscardHandler)
	switch cmd {
	case "buyers":
		rdb := redis.NewClient(cfg.RedisAddr, log)
		defer func() { _ = rdb.Close() }()
		return seedBuyers(ctx, pool, rdb, args)
	case "event":
		return seedEvent(ctx, pool, args)
	case "check":
		return check(ctx, pool, args)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	if path == "" || path == "-" {
		_, err = os.Stdout.Write(append(data, '\n'))
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
