package redis

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// ErrGateOpen — Redis недавно отказал, вызов не выполнялся.
var ErrGateOpen = errors.New("redis is disabled after a recent failure")

// defaultProbeInterval — как часто открытый Gate пропускает пробный вызов.
const defaultProbeInterval = 100 * time.Millisecond

// Gate — предохранитель для необязательных обращений к Redis (ADR 017, 018,
// 023). У каждого вызова короткий бюджет; после ошибки Gate «открывается» на
// Cooldown, и вызовы сразу получают ErrGateOpen, не ожидая таймаутов.
// Вызывающий код в это время работает без Redis.
//
// Открытый Gate раз в ProbeInterval пропускает один пробный вызов
// (полуоткрытое состояние). Удачная проба закрывает Gate сразу. Это важно
// при горизонтальном масштабировании: перегруженный экземпляр api не
// успевает уложиться в бюджет, хотя Redis жив, и без проб он отказывал бы
// всем ещё Cooldown после того, как нагрузка спала (ADR 023).
type Gate struct {
	// Timeout — бюджет одного вызова.
	Timeout time.Duration
	// Cooldown — сколько Gate открыт после ошибки, если пробы не проходят.
	Cooldown time.Duration
	// ProbeInterval — как часто открытый Gate пропускает пробный вызов.
	// 0 — 100 мс.
	ProbeInterval time.Duration
	// OnOpen вызывается, когда Gate открывается (для метрик и логов).
	OnOpen func(err error)

	openUntil atomic.Int64 // UnixNano
	nextProbe atomic.Int64 // UnixNano: не раньше этого — следующая проба
}

// Open сообщает, открыт ли Gate сейчас.
func (g *Gate) Open() bool { return time.Now().UnixNano() < g.openUntil.Load() }

func (g *Gate) probeInterval() time.Duration {
	if g.ProbeInterval > 0 {
		return g.ProbeInterval
	}
	return defaultProbeInterval
}

// Do выполняет f с коротким таймаутом. Ошибка открывает Gate, успех —
// закрывает. Пока Gate открыт, f выполняется только как проба: одна на
// ProbeInterval, остальные вызовы сразу получают ErrGateOpen. Отмена самого
// запроса Gate не открывает: Redis тут ни при чём.
func (g *Gate) Do(ctx context.Context, f func(context.Context) error) error {
	now := time.Now().UnixNano()
	probe := false
	if now < g.openUntil.Load() {
		next := g.nextProbe.Load()
		if now < next || !g.nextProbe.CompareAndSwap(next, now+int64(g.probeInterval())) {
			return ErrGateOpen
		}
		probe = true
	}
	cctx, cancel := context.WithTimeout(ctx, g.Timeout)
	defer cancel()
	err := f(cctx)
	if err == nil {
		// Redis ответил вовремя — значит, он жив, и Gate больше не нужен.
		if g.openUntil.Load() != 0 {
			g.openUntil.Store(0)
		}
		return nil
	}
	if ctx.Err() != nil {
		return err
	}
	t := time.Now()
	g.nextProbe.Store(t.Add(g.probeInterval()).UnixNano())
	prev := g.openUntil.Swap(t.Add(g.Cooldown).UnixNano())
	// Неудачная проба лишь продлевает открытый Gate, это не новое открытие.
	if !probe && prev < t.UnixNano() && g.OnOpen != nil {
		g.OnOpen(err)
	}
	return err
}
