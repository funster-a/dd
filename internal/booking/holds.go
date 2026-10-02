package booking

import (
	"context"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/funster-a/dd/internal/platform/redis"
)

// holdStore — холды мест в Redis (ADR 011). Ключ места живёт ровно столько,
// сколько холд, и исчезает сам. Redis здесь — быстрый фильтр перед базой:
// за популярное место конкурирует Redis, а не строки PostgreSQL. Истина о
// брони — в базе, поэтому расхождение с Redis не может продать место дважды.
//
// Redis здесь необязателен, поэтому его отказ не должен тормозить продажу
// (ADR 017): у каждого вызова короткий таймаут, а после ошибки фильтр
// выключается на breakerCooldown — запросы сразу идут в базу и не ждут
// таймаутов. Без этого эксперимент с отказом Redis показал очередь из
// запросов, ждущих по 3–4 секунды каждый, и обрывы по таймауту сервера.
type holdStore struct {
	rdb  goredis.Scripter
	gate *redis.Gate
}

const (
	// redisCallTimeout — бюджет одного вызова Redis: обычный ответ — доли
	// миллисекунды, ждать дольше незачем.
	redisCallTimeout = 500 * time.Millisecond
	// breakerCooldown — сколько фильтр выключен после ошибки Redis.
	breakerCooldown = 5 * time.Second
)

func newHoldStore(rdb goredis.Scripter, onOpen func(error)) *holdStore {
	return &holdStore{rdb: rdb, gate: &redis.Gate{Timeout: redisCallTimeout, Cooldown: breakerCooldown, OnOpen: onOpen}}
}

func (h *holdStore) call(ctx context.Context, f func(context.Context) error) error {
	return h.gate.Do(ctx, f)
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
	var n int
	err := h.call(ctx, func(ctx context.Context) error {
		var err error
		n, err = claimScript.Run(ctx, h.rdb, keys, orderID, prevOrderID, ms(ttl)).Int()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("claim holds: %w", err)
	}
	return n, nil
}

func (h *holdStore) set(ctx context.Context, keys []string, orderID string, ttl time.Duration) error {
	err := h.call(ctx, func(ctx context.Context) error { return setScript.Run(ctx, h.rdb, keys, orderID, ms(ttl)).Err() })
	if err != nil {
		return fmt.Errorf("set holds: %w", err)
	}
	return nil
}

func (h *holdStore) release(ctx context.Context, keys []string, orderID string) error {
	err := h.call(ctx, func(ctx context.Context) error { return releaseScript.Run(ctx, h.rdb, keys, orderID).Err() })
	if err != nil {
		return fmt.Errorf("release holds: %w", err)
	}
	return nil
}

func ms(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }
