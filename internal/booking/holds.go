package booking

import (
	"context"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// holdStore — холды мест в Redis (ADR 011). Ключ места живёт ровно столько,
// сколько холд, и исчезает сам. Redis здесь — быстрый фильтр перед базой:
// за популярное место конкурирует Redis, а не строки PostgreSQL. Истина о
// брони — в базе, поэтому расхождение с Redis не может продать место дважды.
type holdStore struct {
	rdb goredis.Scripter
}

// Ключи одного события — в одном слоте Redis Cluster благодаря {eventID}:
// скрипты работают со многими ключами сразу.
func holdKey(eventID, section string, row *string, seat string) string {
	r := ""
	if row != nil {
		r = *row
	}
	return "booking:hold:{" + eventID + "}:" + section + "\x1f" + r + "\x1f" + seat
}

// claimScript захватывает все места заказа или ни одного. Место, которое
// держит прежняя корзина того же покупателя, считается свободным: новый
// заказ её заменяет. Возвращает 0 или номер (с 1) первого занятого ключа.
var claimScript = goredis.NewScript(`
for i, k in ipairs(KEYS) do
  local v = redis.call('GET', k)
  if v and v ~= ARGV[2] then return i end
end
for _, k in ipairs(KEYS) do
  redis.call('SET', k, ARGV[1], 'PX', ARGV[3])
end
return 0
`)

// setScript записывает холды мест, уже полученных в базе.
var setScript = goredis.NewScript(`
for _, k in ipairs(KEYS) do
  redis.call('SET', k, ARGV[1], 'PX', ARGV[2])
end
return 0
`)

// releaseScript удаляет только ключи, которые держит этот заказ.
var releaseScript = goredis.NewScript(`
for _, k in ipairs(KEYS) do
  if redis.call('GET', k) == ARGV[1] then redis.call('DEL', k) end
end
return 0
`)

func (h *holdStore) claim(ctx context.Context, keys []string, orderID, prevOrderID string, ttl time.Duration) (int, error) {
	n, err := claimScript.Run(ctx, h.rdb, keys, orderID, prevOrderID, ms(ttl)).Int()
	if err != nil {
		return 0, fmt.Errorf("claim holds: %w", err)
	}
	return n, nil
}

func (h *holdStore) set(ctx context.Context, keys []string, orderID string, ttl time.Duration) error {
	if err := setScript.Run(ctx, h.rdb, keys, orderID, ms(ttl)).Err(); err != nil {
		return fmt.Errorf("set holds: %w", err)
	}
	return nil
}

func (h *holdStore) release(ctx context.Context, keys []string, orderID string) error {
	if err := releaseScript.Run(ctx, h.rdb, keys, orderID).Err(); err != nil {
		return fmt.Errorf("release holds: %w", err)
	}
	return nil
}

func ms(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }
