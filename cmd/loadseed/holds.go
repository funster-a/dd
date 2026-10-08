package main

import (
	"context"
	"flag"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
)

// holdsCheck сверяет холды мест в Redis с базой после прогона (ADR 029).
// Redis — только фильтр перед базой (ADR 011), поэтому расхождения не
// ломают инвариант, но бывают двух видов:
//   - «призрачный» холд: ключ в Redis есть, а в базе место свободно или
//     держится другим заказом. Покупатели получают «место занято», пока ключ
//     не истечёт, хотя место можно продать;
//   - пропавший холд: место держится в базе, а ключа нет. Фильтр
//     пропускает конкурентов до базы, и отказ им даёт она.
func holdsCheck(ctx context.Context, pool *pgxpool.Pool, rdb goredis.UniversalClient, args []string) error {
	fs := flag.NewFlagSet("holds-check", flag.ContinueOnError)
	event := fs.String("event", "", "id события")
	out := fs.String("out", "-", "куда записать итог")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := compareHolds(ctx, pool, rdb, *event, time.Now())
	if err != nil {
		return err
	}
	return writeJSON(*out, r)
}

// holds — итог сверки холдов одного события.
type holds struct {
	RedisHolds int `json:"redis_holds"`
	DBHeld     int `json:"db_held"`
	GhostFree  int `json:"ghost_free"`  // в базе место свободно
	GhostOther int `json:"ghost_other"` // в базе держит другой заказ
	Missing    int `json:"missing"`     // в базе держится, в Redis ключа нет
}

// compareHolds сверяет ключи холдов события в Redis с местами в базе на
// момент at. Холд в базе, чей срок вышел, не считается: его ключ в Redis
// законно истёк по TTL, а место уже свободно для следующего заказа.
func compareHolds(ctx context.Context, pool *pgxpool.Pool, rdb goredis.UniversalClient, eventID string, at time.Time) (holds, error) {
	type seat struct{ status, holder string }
	seats := map[string]seat{}
	rows, err := pool.Query(ctx, `SELECT section, coalesce(row_label, ''), seat_label,
		  CASE WHEN status = 'held' AND hold_expires_at <= $2 THEN 'available' ELSE status END,
		  coalesce(hold_order_id::text, '')
		FROM event_seats WHERE event_id = $1`, eventID, at)
	if err != nil {
		return holds{}, err
	}
	for rows.Next() {
		var section, row, label string
		var s seat
		if err := rows.Scan(&section, &row, &label, &s.status, &s.holder); err != nil {
			return holds{}, err
		}
		seats[holdKey(eventID, section, row, label)] = s
	}
	if err := rows.Err(); err != nil {
		return holds{}, err
	}

	var r holds
	inRedis := map[string]bool{}
	iter := rdb.Scan(ctx, 0, "booking:hold:{"+eventID+"}:*", 1000).Iterator()
	for iter.Next(ctx) {
		k := iter.Val()
		v, err := rdb.Get(ctx, k).Result()
		if err == goredis.Nil { //nolint:errorlint // goredis.Nil не оборачивается
			continue // истёк между SCAN и GET
		}
		if err != nil {
			return holds{}, err
		}
		inRedis[k] = true
		r.RedisHolds++
		switch s := seats[k]; {
		case s.status == "available":
			r.GhostFree++
		case s.status == "held" && s.holder != v:
			r.GhostOther++
		}
	}
	if err := iter.Err(); err != nil {
		return holds{}, err
	}
	for k, s := range seats {
		if s.status == "held" {
			r.DBHeld++
			if !inRedis[k] {
				r.Missing++
			}
		}
	}
	return r, nil
}

// holdKey — тот же ключ, что booking.holdKey.
func holdKey(eventID, section, row, seat string) string {
	return "booking:hold:{" + eventID + "}:" + section + "\x1f" + row + "\x1f" + seat
}
