package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"github.com/funster-a/dd/internal/identity/identitydb"
	"github.com/funster-a/dd/internal/platform/auth"
	"github.com/funster-a/dd/internal/platform/redis"
)

// ErrInvalidAddress — телефон или email в неверном формате.
var ErrInvalidAddress = errors.New("invalid address")

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// Service — вход по одноразовому коду и сессии.
type Service struct {
	q        *identitydb.Queries
	codes    codes
	sessions sessions
	// cache и gate — работа проверки сессий при отказе Redis (ADR 018).
	cache  *sessionCache
	gate   *redis.Gate
	sender Sender
	admins map[string]struct{}
	log    *slog.Logger
}

// NewService создаёт сервис. adminEmails — администраторы платформы (ADR 006).
func NewService(pool *pgxpool.Pool, rdb goredis.Cmdable, sender Sender, adminEmails []string, log *slog.Logger) *Service {
	admins := make(map[string]struct{}, len(adminEmails))
	for _, e := range adminEmails {
		admins[strings.ToLower(strings.TrimSpace(e))] = struct{}{}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		q:        identitydb.New(pool),
		codes:    codes{rdb: rdb},
		sessions: sessions{rdb: rdb},
		sender:   sender,
		admins:   admins,
		log:      log,
		cache:    newSessionCache(sessionStaleMaxAge, sessionCacheLimit),
	}
	s.gate = &redis.Gate{Timeout: sessionCallTimeout, Cooldown: sessionGateCooldown, OnOpen: func(err error) {
		sessionGateOpened.Inc()
		s.log.Warn("session store unavailable, using recently verified sessions", slog.Duration("for", sessionGateCooldown), slog.Any("error", err))
	}}
	return s
}

const (
	// sessionCallTimeout — бюджет проверки сессии в Redis.
	sessionCallTimeout = 300 * time.Millisecond
	// sessionGateCooldown — сколько после ошибки Redis сессии проверяются
	// только по кэшу.
	sessionGateCooldown = 5 * time.Second
	// sessionStaleMaxAge — по проверке какой давности можно пустить запрос
	// без Redis (бизнес-решение, ADR 018).
	sessionStaleMaxAge = 10 * time.Minute
	// sessionCacheLimit — сколько сессий помнит процесс: около 300 байт на
	// сессию, до ~60 МБ.
	sessionCacheLimit = 200_000
)

// ErrUnavailable — хранилище сессий недоступно, а сессии нет в кэше
// недавно проверенных. HTTP 503: можно повторить позже.
var ErrUnavailable = errors.New("session store is unavailable")

// NormalizeAddress проверяет и приводит адрес к каноническому виду:
// телефон в E.164 для покупателя, email в нижнем регистре для остальных.
func NormalizeAddress(kind auth.Kind, address string) (string, error) {
	address = strings.TrimSpace(address)
	switch kind {
	case auth.KindBuyer:
		if !e164.MatchString(address) {
			return "", ErrInvalidAddress
		}
		return address, nil
	case auth.KindOrganizer, auth.KindAdmin:
		addr, err := mail.ParseAddress(address)
		if err != nil || addr.Address != address {
			return "", ErrInvalidAddress
		}
		return strings.ToLower(address), nil
	default:
		return "", ErrInvalidAddress
	}
}

// RequestCode отправляет код входа. Лимиты считаются для любого адреса,
// а организатору и администратору код уходит, только если адрес известен;
// ответ в обоих случаях одинаков, чтобы по нему нельзя было перебирать email.
func (s *Service) RequestCode(ctx context.Context, kind auth.Kind, address, ip string) error {
	address, err := NormalizeAddress(kind, address)
	if err != nil {
		return err
	}
	if err := s.codes.limit(ctx, kind, address, ip); err != nil {
		return err
	}

	known, err := s.known(ctx, kind, address)
	if err != nil {
		return err
	}
	if !known {
		return nil
	}

	code, err := s.codes.store(ctx, kind, address)
	if err != nil {
		return err
	}
	if err := s.sender.Send(ctx, kind, address, code); err != nil {
		return fmt.Errorf("send code: %w", err)
	}
	return nil
}

// Login сверяет код и открывает сессию. Возвращает токен и сессию.
func (s *Service) Login(ctx context.Context, kind auth.Kind, address, code string) (string, Session, error) {
	address, err := NormalizeAddress(kind, address)
	if err != nil {
		return "", Session{}, err
	}
	if err := s.codes.verify(ctx, kind, address, code); err != nil {
		return "", Session{}, err
	}

	var p auth.Principal
	switch kind {
	case auth.KindBuyer:
		id, err := s.q.UpsertBuyerByPhone(ctx, address)
		if err != nil {
			return "", Session{}, fmt.Errorf("upsert buyer: %w", err)
		}
		p = auth.Principal{Kind: kind, SubjectID: id}
	case auth.KindOrganizer:
		owner, err := s.q.GetOwnerByEmail(ctx, address)
		if errors.Is(err, pgx.ErrNoRows) {
			// Владельца удалили, пока код был в пути.
			return "", Session{}, ErrInvalidCode
		}
		if err != nil {
			return "", Session{}, fmt.Errorf("load organizer owner: %w", err)
		}
		p = auth.Principal{Kind: kind, SubjectID: owner.ID, OrganizerID: owner.OrganizerID}
	case auth.KindAdmin:
		if _, ok := s.admins[address]; !ok {
			return "", Session{}, ErrInvalidCode
		}
		p = auth.Principal{Kind: kind, SubjectID: address}
	}
	return s.sessions.create(ctx, p)
}

// Authenticate возвращает сессию по токену.
//
// Пока Redis отвечает, сессия всегда проверяется в нём: отзыв действует
// сразу. При отказе Redis запрос пускается по последней успешной проверке
// этой сессии в этом процессе, если ей не больше sessionStaleMaxAge
// (ADR 018). Новые входы без Redis невозможны: коды тоже живут в нём.
func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	key := sessionKey(token)
	var sess Session
	err := s.gate.Do(ctx, func(ctx context.Context) error {
		var err error
		sess, err = s.sessions.get(ctx, token)
		if errors.Is(err, ErrNoSession) {
			return nil // ответ Redis, а не его отказ
		}
		return err
	})
	now := time.Now()
	switch {
	case err == nil && sess.ExpiresAt.IsZero():
		s.cache.delete(key)
		return Session{}, ErrNoSession
	case err == nil:
		s.cache.put(key, sess, now)
		return sess, nil
	case ctx.Err() != nil:
		return Session{}, err
	}
	if cached, ok := s.cache.get(key, now); ok {
		sessionStaleHits.Inc()
		return cached, nil
	}
	return Session{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// Logout отзывает сессию. Кэш этого процесса забывает её сразу.
func (s *Service) Logout(ctx context.Context, token string) error {
	s.cache.delete(sessionKey(token))
	return s.sessions.delete(ctx, token)
}

func (s *Service) known(ctx context.Context, kind auth.Kind, address string) (bool, error) {
	switch kind {
	case auth.KindBuyer:
		return true, nil
	case auth.KindOrganizer:
		_, err := s.q.GetOwnerByEmail(ctx, address)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("load organizer owner: %w", err)
		}
		return true, nil
	case auth.KindAdmin:
		_, ok := s.admins[address]
		return ok, nil
	default:
		return false, ErrInvalidAddress
	}
}

// IssueBuyerSession выдаёт сессию покупателю с номером phone без кода
// подтверждения. Только для доверенных инструментов, запускаемых рядом с
// базой: сидер нагрузочного эксперимента создаёт тысячи покупателей
// (ADR 017). Через HTTP этот путь недоступен.
func (s *Service) IssueBuyerSession(ctx context.Context, phone string) (string, error) {
	phone, err := NormalizeAddress(auth.KindBuyer, phone)
	if err != nil {
		return "", err
	}
	id, err := s.q.UpsertBuyerByPhone(ctx, phone)
	if err != nil {
		return "", fmt.Errorf("upsert buyer: %w", err)
	}
	token, _, err := s.sessions.create(ctx, auth.Principal{Kind: auth.KindBuyer, SubjectID: id})
	return token, err
}
