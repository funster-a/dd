package redis

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// ErrGateOpen — Redis недавно отказал, вызов не выполнялся.
var ErrGateOpen = errors.New("redis is disabled after a recent failure")

// Gate — предохранитель для необязательных обращений к Redis (ADR 017, 018).
// У каждого вызова короткий бюджет; после ошибки Gate «открывается» на
// Cooldown, и следующие вызовы сразу получают ErrGateOpen, не ожидая
// таймаутов. Вызывающий код в это время работает без Redis.
type Gate struct {
	// Timeout — бюджет одного вызова.
	Timeout time.Duration
	// Cooldown — сколько Gate открыт после ошибки.
	Cooldown time.Duration
	// OnOpen вызывается, когда Gate открывается (для метрик и логов).
	OnOpen func(err error)

	openUntil atomic.Int64 // UnixNano
}

// Open сообщает, открыт ли Gate сейчас.
func (g *Gate) Open() bool { return time.Now().UnixNano() < g.openUntil.Load() }

// Do выполняет f с коротким таймаутом и открывает Gate при ошибке. Отмена
// самого запроса Gate не открывает: Redis тут ни при чём.
func (g *Gate) Do(ctx context.Context, f func(context.Context) error) error {
	if g.Open() {
		return ErrGateOpen
	}
	cctx, cancel := context.WithTimeout(ctx, g.Timeout)
	defer cancel()
	err := f(cctx)
	if err != nil && ctx.Err() == nil {
		now := time.Now()
		if prev := g.openUntil.Swap(now.Add(g.Cooldown).UnixNano()); prev < now.UnixNano() && g.OnOpen != nil {
			g.OnOpen(err)
		}
	}
	return err
}
