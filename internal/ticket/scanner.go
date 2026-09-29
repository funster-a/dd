package ticket

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/funster-a/dd/internal/ticket/ticketdb"
)

// Контроль входа (ADR 013). Организатор создаёт ссылку сканера для события
// и отдаёт её контролёру; учётной записи у контролёра нет (spec.md). Ссылка
// даёт право только сканировать билеты этого события и отзывается.

// ValidationError — неверное значение во входных данных. HTTP 400.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// ErrScannerRevoked — ссылка сканера отозвана или не существует. HTTP 401.
var ErrScannerRevoked = errors.New("scanner link is invalid or revoked")

// Scanner — ссылка сканера в кабинете организатора.
type Scanner struct {
	ID        string     `json:"id"`
	EventID   string     `json:"event_id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
	// URL и Token показываются один раз, при создании: в базе только хэш.
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

// CreateScanner создаёт ссылку сканера для события организатора.
func (s *Service) CreateScanner(ctx context.Context, organizerID, eventID, name string) (Scanner, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > 100 {
		return Scanner{}, &ValidationError{Field: "name", Message: "must be 1-100 characters, e.g. «Вход А»"}
	}
	if uuid.Validate(eventID) != nil {
		return Scanner{}, ErrNotFound
	}
	token := rand.Text() + rand.Text() // 256 бит
	row, err := s.q.CreateScannerLink(ctx, ticketdb.CreateScannerLinkParams{
		OrganizerID: organizerID, EventID: eventID, Name: name, TokenHash: hashToken(token),
	})
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23503" {
		return Scanner{}, ErrNotFound // событие чужое или его нет
	}
	if err != nil {
		return Scanner{}, fmt.Errorf("create scanner link: %w", err)
	}
	return Scanner{
		ID: row.ID, EventID: row.EventID, Name: row.Name, CreatedAt: row.CreatedAt,
		// Токен во фрагменте ссылки: браузер не отправляет его на сервер и
		// он не попадает в логи прокси.
		URL: s.base + "/scan#" + token, Token: token,
	}, nil
}

// ListScanners возвращает ссылки сканера события без токенов.
func (s *Service) ListScanners(ctx context.Context, organizerID, eventID string) ([]Scanner, error) {
	if uuid.Validate(eventID) != nil {
		return nil, ErrNotFound
	}
	rows, err := s.q.ListScannerLinks(ctx, ticketdb.ListScannerLinksParams{OrganizerID: organizerID, EventID: eventID})
	if err != nil {
		return nil, fmt.Errorf("list scanner links: %w", err)
	}
	out := make([]Scanner, len(rows))
	for i, r := range rows {
		out[i] = Scanner{ID: r.ID, EventID: r.EventID, Name: r.Name, CreatedAt: r.CreatedAt, RevokedAt: r.RevokedAt}
	}
	return out, nil
}

// RevokeScanner отзывает ссылку: дальше она не принимается ни для списка
// билетов, ни для сканирований. Повторный отзыв ничего не меняет.
func (s *Service) RevokeScanner(ctx context.Context, organizerID, id string) error {
	if uuid.Validate(id) != nil {
		return ErrNotFound
	}
	n, err := s.q.RevokeScannerLink(ctx, ticketdb.RevokeScannerLinkParams{OrganizerID: organizerID, ID: id})
	if err != nil {
		return fmt.Errorf("revoke scanner link: %w", err)
	}
	if n == 0 {
		exists, err := s.q.ScannerLinkExists(ctx, ticketdb.ScannerLinkExistsParams{OrganizerID: organizerID, ID: id})
		if err != nil {
			return fmt.Errorf("check scanner link: %w", err)
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

// ScannerSession — действующая ссылка сканера из заголовка запроса.
type ScannerSession struct {
	LinkID      string
	OrganizerID string
	EventID     string
	Name        string
	Event       ScannerEvent
}

// ScannerEvent — событие, которое сканирует контролёр.
type ScannerEvent struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	Venue    string    `json:"venue"`
	Timezone string    `json:"timezone"`
}

// ScannerByToken проверяет токен ссылки сканера.
func (s *Service) ScannerByToken(ctx context.Context, token string) (ScannerSession, error) {
	if token == "" {
		return ScannerSession{}, ErrScannerRevoked
	}
	r, err := s.q.GetScannerByTokenHash(ctx, hashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return ScannerSession{}, ErrScannerRevoked
	}
	if err != nil {
		return ScannerSession{}, fmt.Errorf("load scanner link: %w", err)
	}
	if r.RevokedAt != nil {
		return ScannerSession{}, ErrScannerRevoked
	}
	return ScannerSession{
		LinkID: r.ID, OrganizerID: r.OrganizerID, EventID: r.EventID, Name: r.Name,
		Event: ScannerEvent{ID: r.EventID, Title: r.Title, StartsAt: r.StartsAt, EndsAt: r.EndsAt, Venue: r.VenueName, Timezone: r.VenueTimezone},
	}, nil
}

// Manifest — всё, что нужно сканеру для работы без сети: билеты события.
// Токен билета — id и подпись; сканер без сети достаёт id из токена и
// ищет его в списке. Подобрать чужой id нельзя: в UUIDv7 74 случайных бита.
type Manifest struct {
	Event       ScannerEvent     `json:"event"`
	Tickets     []ManifestTicket `json:"tickets"`
	GeneratedAt time.Time        `json:"generated_at"`
}

// ManifestTicket — билет в списке сканера.
type ManifestTicket struct {
	ID      string  `json:"id"`
	Status  string  `json:"status"`
	Section string  `json:"section"`
	Row     *string `json:"row"`
	Seat    string  `json:"seat"`
}

// Manifest возвращает список билетов события сканера.
func (s *Service) Manifest(ctx context.Context, sess ScannerSession, now time.Time) (Manifest, error) {
	rows, err := s.q.ManifestTickets(ctx, sess.EventID)
	if err != nil {
		return Manifest{}, fmt.Errorf("list tickets: %w", err)
	}
	m := Manifest{Event: sess.Event, Tickets: make([]ManifestTicket, len(rows)), GeneratedAt: now}
	for i, r := range rows {
		m.Tickets[i] = ManifestTicket{ID: r.ID, Status: r.Status, Section: r.Section, Row: r.RowLabel, Seat: r.SeatLabel}
	}
	return m, nil
}

// Результаты сканирования.
const (
	ScanAccepted   = "accepted"    // проход засчитан
	ScanDuplicate  = "duplicate"   // билет уже прошёл раньше
	ScanRevoked    = "revoked"     // билет аннулирован (возврат)
	ScanInvalid    = "invalid"     // не билет платформы или подделка
	ScanWrongEvent = "wrong_event" // билет на другое событие
)

// ScanInput — одно сканирование с устройства.
type ScanInput struct {
	// ClientScanID — id сканирования на устройстве: повторная отправка
	// той же пачки после обрыва связи ничего не удваивает.
	ClientScanID string `json:"client_scan_id"`
	// Code — содержимое QR: ссылка на билет или её токен.
	Code string `json:"code"`
	// Offline — сканирование сделано без сети: контролёр уже пропустил
	// человека по списку билетов, сервер только записывает итог.
	Offline bool `json:"offline"`
	// ScannedAt — когда отсканировано на устройстве; учитывается только для
	// офлайн-сканирований. Онлайн время ставит сервер.
	ScannedAt time.Time `json:"scanned_at"`
}

// ScanResult — итог сканирования для экрана контролёра.
type ScanResult struct {
	ClientScanID string `json:"client_scan_id"`
	Result       string `json:"result"`
	// FirstScannedAt — когда билет прошёл впервые (для duplicate и accepted).
	FirstScannedAt *time.Time `json:"first_scanned_at,omitempty"`
	Section        string     `json:"section,omitempty"`
	Row            *string    `json:"row,omitempty"`
	Seat           string     `json:"seat,omitempty"`
}

// maxScanBatch — сколько сканирований принимается за один запрос.
const maxScanBatch = 500

// clockSkew — насколько часы устройства могут спешить.
const clockSkew = time.Minute

// Scan принимает пачку сканирований: онлайн — по одному, после работы без
// сети — всё накопленное.
//
// Онлайн решает сервер: первое обработанное сканирование проходит, остальные
// — повторные попытки. Офлайн люди уже прошли, поэтому при синхронизации
// засчитывается самое раннее по времени устройства сканирование, остальные
// записываются как повторные попытки прохода (spec.md).
func (s *Service) Scan(ctx context.Context, sess ScannerSession, deviceID string, scans []ScanInput, now time.Time) ([]ScanResult, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		deviceID = sess.Name
	}
	if len(scans) == 0 || len(scans) > maxScanBatch {
		return nil, &ValidationError{Field: "scans", Message: fmt.Sprintf("send 1-%d scans", maxScanBatch)}
	}
	for _, sc := range scans {
		if id := strings.TrimSpace(sc.ClientScanID); id == "" || len(id) > 100 {
			return nil, &ValidationError{Field: "client_scan_id", Message: "is required, at most 100 characters"}
		}
	}
	out := make([]ScanResult, len(scans))
	for i, sc := range scans {
		r, err := s.scanOne(ctx, sess, deviceID, sc, now)
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return out, nil
}

func (s *Service) scanOne(ctx context.Context, sess ScannerSession, deviceID string, in ScanInput, now time.Time) (ScanResult, error) {
	res := ScanResult{ClientScanID: strings.TrimSpace(in.ClientScanID)}
	if stored, ok, err := s.storedScan(ctx, sess, res.ClientScanID); err != nil || ok {
		return stored, err
	}
	code := strings.TrimSpace(in.Code)
	ticketID, ok := s.signer.Parse(code[strings.LastIndex(code, "/")+1:])
	if !ok {
		res.Result = ScanInvalid
		return res, nil
	}
	// База хранит микросекунды: время в ответе должно совпадать с записанным.
	at := now.Truncate(time.Microsecond)
	if in.Offline && !in.ScannedAt.IsZero() && !in.ScannedAt.After(now.Add(clockSkew)) {
		at = in.ScannedAt.UTC().Truncate(time.Microsecond)
	}

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		t, err := q.LockTicketForScan(ctx, ticketID)
		if errors.Is(err, pgx.ErrNoRows) {
			res.Result = ScanInvalid
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock ticket: %w", err)
		}
		if t.EventID != sess.EventID {
			res.Result = ScanWrongEvent
			return nil
		}
		res.Section, res.Row, res.Seat = t.Section, t.RowLabel, t.SeatLabel
		switch {
		case t.Status == "revoked":
			res.Result = ScanRevoked
		case t.Status == "issued":
			if err := q.MarkTicketUsed(ctx, ticketdb.MarkTicketUsedParams{ID: t.ID, UsedAt: at}); err != nil {
				return fmt.Errorf("mark used: %w", err)
			}
			res.Result, res.FirstScannedAt = ScanAccepted, &at
		case in.Offline && t.UsedAt != nil && at.Before(*t.UsedAt):
			// Офлайн-сканирование пришло позже, но случилось раньше
			// засчитанного: первым считается оно.
			if err := q.DemoteAcceptedScan(ctx, t.ID); err != nil {
				return fmt.Errorf("demote scan: %w", err)
			}
			if err := q.MoveTicketUsedAt(ctx, ticketdb.MoveTicketUsedAtParams{ID: t.ID, UsedAt: at}); err != nil {
				return fmt.Errorf("move used_at: %w", err)
			}
			res.Result, res.FirstScannedAt = ScanAccepted, &at
		default:
			res.Result, res.FirstScannedAt = ScanDuplicate, t.UsedAt
		}
		return q.InsertScan(ctx, ticketdb.InsertScanParams{
			OrganizerID: t.OrganizerID, TicketID: t.ID, DeviceID: deviceID, ScannedAt: at, Result: res.Result,
			ScannerLinkID: sess.LinkID, ClientScanID: res.ClientScanID,
		})
	})
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.ConstraintName == "ticket_scans_client_key" {
		// То же сканирование пришло параллельно — отвечаем записанным итогом.
		stored, _, err := s.storedScan(ctx, sess, res.ClientScanID)
		return stored, err
	}
	if err != nil {
		return ScanResult{}, err
	}
	return res, nil
}

// storedScan — итог уже принятого сканирования с тем же id устройства.
func (s *Service) storedScan(ctx context.Context, sess ScannerSession, clientScanID string) (ScanResult, bool, error) {
	r, err := s.q.GetScanByClient(ctx, ticketdb.GetScanByClientParams{ScannerLinkID: sess.LinkID, ClientScanID: clientScanID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ScanResult{}, false, nil
	}
	if err != nil {
		return ScanResult{}, false, fmt.Errorf("load scan: %w", err)
	}
	res := ScanResult{ClientScanID: clientScanID, Result: r.Result, Section: r.Section, Row: r.RowLabel, Seat: r.SeatLabel}
	if r.Result != ScanRevoked {
		res.FirstScannedAt = r.UsedAt
	}
	return res, true, nil
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
