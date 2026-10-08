// Команда loadseed готовит и проверяет нагрузочный эксперимент со
// стратегиями захвата мест (spec.md, «Инженерное ядро»; ADR 017).
//
//	loadseed buyers -n 5000 -out buyers.json   покупатели с сессиями
//	loadseed event -out event.json             новое событие с залом
//	loadseed check -event <id>                 инвариант после прогона
//	loadseed report -dir <results>             таблица и графики
//	loadseed queue-report -dir <results>       то же для серии с очередью (ADR 020)
//	loadseed scale-report -dir <results>       ёмкость при 1, 2, 4 экземплярах api (ADR 023)
//	loadseed stadium -out stadium.json         событие на Центральном стадионе Алматы (ADR 024)
//	loadseed storm-report -dir <results>       отчёт по штурму стадиона (ADR 026)
//	loadseed chaos-report -series до=<dir>,…  отказ экземпляра api посреди продажи (ADR 027)
//	loadseed pgchaos-report -in <dir>         переключение PostgreSQL посреди продажи (ADR 028)
//	loadseed redischaos-report -in <dir>      отказ Redis посреди продажи (ADR 029)
//	loadseed holds-check -event <id>          холды мест в Redis против базы (ADR 029)
//	loadseed paystream -event <file>          поток оплат через outbox и RabbitMQ (ADR 030)
//	loadseed tickets-check -stream <file>     билеты по оплаченным заказам потока (ADR 030)
//	loadseed mqchaos-report -in <dir>         отказ RabbitMQ посреди продажи (ADR 030)
//	loadseed readpath-report -in <dir>        нагрузка на ведущий узел при старте продаж (ADR 031)
//	loadseed k8s-report -in <data> -out <dir> графики автомасштабирования при штурме (ADR 032)
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
		fmt.Fprintln(os.Stderr, "usage: loadseed buyers|event|stadium|check|report|queue-report|scale-report|storm-report|chaos-report|pgchaos-report|redischaos-report|holds-check|paystream|tickets-check|mqchaos-report|readpath-report|k8s-report [flags]")
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
	switch cmd {
	case "report":
		return report(args)
	case "queue-report":
		return queueReport(args)
	case "scale-report":
		return scaleReport(args)
	case "storm-report":
		return stormReport(args)
	case "chaos-report":
		return chaosReport(args)
	case "pgchaos-report":
		return pgChaosReport(args)
	case "redischaos-report":
		return redisChaosReport(args)
	case "mqchaos-report":
		return mqChaosReport(args)
	case "readpath-report":
		return readPathReport(args)
	case "k8s-report":
		return k8sReport(args)
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
		rdb := redis.Open(cfg.RedisAddr, cfg.RedisMaster, cfg.RedisSentinels, log)
		defer func() { _ = rdb.Close() }()
		return seedBuyers(ctx, pool, rdb, args)
	case "holds-check":
		rdb := redis.Open(cfg.RedisAddr, cfg.RedisMaster, cfg.RedisSentinels, log)
		defer func() { _ = rdb.Close() }()
		return holdsCheck(ctx, pool, rdb, args)
	case "paystream":
		return payStream(ctx, pool, args)
	case "tickets-check":
		return ticketsCheck(ctx, pool, args)
	case "event":
		return seedEvent(ctx, pool, args)
	case "stadium":
		return seedStadium(ctx, pool, args)
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
