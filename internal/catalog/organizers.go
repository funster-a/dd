package catalog

import (
	"context"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/funster-a/dd/internal/catalog/catalogdb"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Service — операции каталога.
type Service struct {
	pool  *pgxpool.Pool
	q     *catalogdb.Queries
	store ObjectStore
}

// NewService создаёт сервис каталога. store — хранилище обложек; без него
// загрузка медиа недоступна, остальное работает.
func NewService(pool *pgxpool.Pool, store ObjectStore) *Service {
	return &Service{pool: pool, q: catalogdb.New(pool), store: store}
}

// NewOrganizer — данные для заведения организатора.
type NewOrganizer struct {
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	OwnerEmail string `json:"owner_email"`
}

// Organizer — организатор с email владельца.
type Organizer struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Slug       string    `json:"slug"`
	OwnerEmail string    `json:"owner_email"`
	CreatedAt  time.Time `json:"created_at"`
}

// CreateOrganizer заводит организатора и его владельца одной транзакцией.
// Сейчас её вызывает админка, позже — форма самостоятельной регистрации (ADR 006).
func (s *Service) CreateOrganizer(ctx context.Context, in NewOrganizer) (Organizer, error) {
	in, err := in.normalize()
	if err != nil {
		return Organizer{}, err
	}

	var org Organizer
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		row, err := q.CreateOrganizer(ctx, catalogdb.CreateOrganizerParams{Name: in.Name, Slug: in.Slug})
		if err != nil {
			return err
		}
		if _, err := q.CreateOwner(ctx, catalogdb.CreateOwnerParams{OrganizerID: row.ID, Email: in.OwnerEmail}); err != nil {
			return err
		}
		org = Organizer{ID: row.ID, Name: row.Name, Slug: row.Slug, OwnerEmail: in.OwnerEmail, CreatedAt: row.CreatedAt}
		return nil
	})
	switch uniqueConstraint(err) {
	case "":
	case "organizers_slug_key":
		return Organizer{}, &ConflictError{Code: "slug_taken", Message: "organizer with this slug already exists"}
	case "organizer_members_owner_email_key":
		return Organizer{}, &ConflictError{Code: "owner_email_taken", Message: "this email already owns another organizer"}
	}
	if err != nil {
		return Organizer{}, fmt.Errorf("create organizer: %w", err)
	}
	return org, nil
}

// ListOrganizers возвращает организаторов для админки, новые сверху.
func (s *Service) ListOrganizers(ctx context.Context) ([]Organizer, error) {
	const pageSize = 100
	rows, err := s.q.ListOrganizers(ctx, pageSize)
	if err != nil {
		return nil, fmt.Errorf("list organizers: %w", err)
	}
	out := make([]Organizer, 0, len(rows))
	for _, r := range rows {
		out = append(out, Organizer{ID: r.ID, Name: r.Name, Slug: r.Slug, OwnerEmail: r.OwnerEmail, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

func (in NewOrganizer) normalize() (NewOrganizer, error) {
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n == 0 || n > 200 {
		return in, &ValidationError{Field: "name", Message: "must be 1-200 characters"}
	}
	in.Slug = strings.TrimSpace(in.Slug)
	if len(in.Slug) < 3 || len(in.Slug) > 63 || !slugPattern.MatchString(in.Slug) {
		return in, &ValidationError{Field: "slug", Message: "must be 3-63 characters: lowercase latin letters, digits and single hyphens"}
	}
	email := strings.TrimSpace(in.OwnerEmail)
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return in, &ValidationError{Field: "owner_email", Message: "must be a plain email address"}
	}
	in.OwnerEmail = strings.ToLower(email)
	return in, nil
}
