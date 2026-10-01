package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"github.com/funster-a/dd/internal/identity/identitydb"
	"github.com/funster-a/dd/internal/platform/auth"
)

// ErrInvalidAddress — телефон или email в неверном формате.
var ErrInvalidAddress = errors.New("invalid address")

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// Service — вход по одноразовому коду и сессии.
type Service struct {
	q        *identitydb.Queries
	codes    codes
	sessions sessions
	sender   Sender
	admins   map[string]struct{}
	log      *slog.Logger
}

// NewService создаёт сервис. adminEmails — администраторы платформы (ADR 006).
func NewService(pool *pgxpool.Pool, rdb goredis.Cmdable, sender Sender, adminEmails []string, log *slog.Logger) *Service {
	admins := make(map[string]struct{}, len(adminEmails))
	for _, e := range adminEmails {
		admins[strings.ToLower(strings.TrimSpace(e))] = struct{}{}
	}
	return &Service{
		q:        identitydb.New(pool),
		codes:    codes{rdb: rdb},
		sessions: sessions{rdb: rdb},
		sender:   sender,
		admins:   admins,
		log:      log,
	}
}

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
func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	return s.sessions.get(ctx, token)
}

// Logout отзывает сессию.
func (s *Service) Logout(ctx context.Context, token string) error {
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
