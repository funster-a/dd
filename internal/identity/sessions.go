package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/funster-a/dd/internal/platform/auth"
)

// ErrNoSession — токен неизвестен, истёк или отозван.
var ErrNoSession = errors.New("session not found")

// Session — сессия участника. В Redis лежит под ключом из хеша токена,
// поэтому утечка содержимого Redis не раскрывает сами токены.
type Session struct {
	auth.Principal
	ExpiresAt time.Time `json:"expires_at"`
}

func sessionTTL(kind auth.Kind) time.Duration {
	if kind == auth.KindBuyer {
		return 30 * 24 * time.Hour
	}
	return 12 * time.Hour
}

type sessions struct {
	rdb goredis.Cmdable
}

func (s sessions) create(ctx context.Context, p auth.Principal) (string, Session, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Session{}, fmt.Errorf("generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	ttl := sessionTTL(p.Kind)
	sess := Session{Principal: p, ExpiresAt: time.Now().UTC().Add(ttl).Truncate(time.Second)}
	data, err := json.Marshal(sess)
	if err != nil {
		return "", Session{}, fmt.Errorf("encode session: %w", err)
	}
	if err := s.rdb.Set(ctx, sessionKey(token), data, ttl).Err(); err != nil {
		return "", Session{}, fmt.Errorf("store session: %w", err)
	}
	return token, sess, nil
}

func (s sessions) get(ctx context.Context, token string) (Session, error) {
	data, err := s.rdb.Get(ctx, sessionKey(token)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("load session: %w", err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return Session{}, fmt.Errorf("decode session: %w", err)
	}
	return sess, nil
}

func (s sessions) delete(ctx context.Context, token string) error {
	if err := s.rdb.Del(ctx, sessionKey(token)).Err(); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "session:" + hex.EncodeToString(sum[:])
}
