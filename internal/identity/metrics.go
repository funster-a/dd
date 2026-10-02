package identity

import "github.com/prometheus/client_golang/prometheus"

// Метрики проверки сессий при отказе Redis (ADR 018).
var (
	sessionGateOpened = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "dd_identity_session_gate_opened_total",
		Help: "Сколько раз проверка сессий переходила на кэш после ошибки Redis.",
	})
	sessionStaleHits = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "dd_identity_session_stale_hits_total",
		Help: "Запросы, пропущенные по недавно проверенной сессии без Redis.",
	})
)

func init() { prometheus.MustRegister(sessionGateOpened, sessionStaleHits) }
