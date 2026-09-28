package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/funster-a/dd/internal/platform/auth"
)

// Параметры одноразовых кодов (ADR 006).
const (
	codeTTL           = 5 * time.Minute
	maxCodeAttempts   = 5
	resendInterval    = time.Minute
	maxCodesPerHour   = 5  // на один адрес
	maxCodesPerIPHour = 20 // с одного IP
)

var (
	// ErrTooManyRequests — превышен лимит запросов кода.
	ErrTooManyRequests = errors.New("too many code requests")
	// ErrInvalidCode — код неверный, истёк, уже использован или исчерпаны попытки.
	ErrInvalidCode = errors.New("invalid or expired code")
)

// verifyScript атомарно проверяет код: считает попытку, сжигает код после
// maxCodeAttempts неудач и удаляет его при успехе, поэтому один код даёт
// ровно одну сессию даже при одновременных запросах.
//
// Возвращает 1 — код верный, 0 — неверный или отсутствует, -1 — попытки исчерпаны.
var verifyScript = goredis.NewScript(`
local h = redis.call('HGET', KEYS[1], 'hash')
if not h then return 0 end
local n = redis.call('HINCRBY', KEYS[1], 'attempts', 1)
if h == ARGV[1] then
  redis.call('DEL', KEYS[1])
  return 1
end
if n >= tonumber(ARGV[2]) then
  redis.call('DEL', KEYS[1])
  return -1
end
return 0
`)

type codes struct {
	rdb goredis.Cmdable
}

// limit проверяет лимиты запросов кода: не чаще раза в минуту на адрес,
// не больше maxCodesPerHour в час на адрес и maxCodesPerIPHour с одного IP.
func (c codes) limit(ctx context.Context, kind auth.Kind, address, ip string) error {
	checks := []struct {
		key    string
		max    int64
		window time.Duration
	}{
		{"rl:code:resend:" + string(kind) + ":" + address, 1, resendInterval},
		{"rl:code:hour:" + string(kind) + ":" + address, maxCodesPerHour, time.Hour},
		{"rl:code:ip:" + ip, maxCodesPerIPHour, time.Hour},
	}
	for _, chk := range checks {
		over, err := c.hit(ctx, chk.key, chk.max, chk.window)
		if err != nil {
			return err
		}
		if over {
			return ErrTooManyRequests
		}
	}
	return nil
}

// hit увеличивает счётчик фиксированного окна и сообщает, превышен ли лимит.
func (c codes) hit(ctx context.Context, key string, maxHits int64, window time.Duration) (bool, error) {
	pipe := c.rdb.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.ExpireNX(ctx, key, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, fmt.Errorf("rate limit: %w", err)
	}
	return incr.Val() > maxHits, nil
}

// store создаёт новый код для адреса, заменяя прежний, и возвращает его.
func (c codes) store(ctx context.Context, kind auth.Kind, address string) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	code := fmt.Sprintf("%06d", n.Int64())

	key := codeKey(kind, address)
	pipe := c.rdb.TxPipeline()
	pipe.Del(ctx, key)
	pipe.HSet(ctx, key, "hash", codeHash(address, code), "attempts", 0)
	pipe.Expire(ctx, key, codeTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("store code: %w", err)
	}
	return code, nil
}

// verify сверяет код; при успехе код больше не действует.
func (c codes) verify(ctx context.Context, kind auth.Kind, address, code string) error {
	res, err := verifyScript.Run(ctx, c.rdb, []string{codeKey(kind, address)},
		codeHash(address, code), maxCodeAttempts).Int()
	if err != nil {
		return fmt.Errorf("verify code: %w", err)
	}
	if res != 1 {
		return ErrInvalidCode
	}
	return nil
}

func codeKey(kind auth.Kind, address string) string {
	return "code:" + string(kind) + ":" + address
}

// codeHash хранит в Redis не сам код, а хеш с привязкой к адресу.
func codeHash(address, code string) string {
	sum := sha256.Sum256([]byte(address + ":" + code))
	return hex.EncodeToString(sum[:])
}
