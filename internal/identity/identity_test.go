package identity

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/funster-a/dd/internal/platform/auth"
	"github.com/funster-a/dd/internal/platform/db/dbtest"
)

// Интеграционные тесты: нужны DATABASE_TEST_URL и REDIS_TEST_ADDR
// (make test-integration задаёт обе). Ключи в Redis содержат случайные
// телефоны, email и IP, поэтому тесты не мешают друг другу.

// fakeSender запоминает последний код для каждого адреса.
type fakeSender struct {
	mu    sync.Mutex
	codes map[string]string
	sent  int
}

func (f *fakeSender) Send(_ context.Context, _ auth.Kind, address, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.codes == nil {
		f.codes = map[string]string{}
	}
	f.codes[address] = code
	f.sent++
	return nil
}

func (f *fakeSender) code(t *testing.T, address string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	code, ok := f.codes[address]
	if !ok {
		t.Fatalf("no code was sent to %s", address)
	}
	return code
}

type env struct {
	svc    *Service
	sender *fakeSender
	db     *dbtest.DB
}

func newEnv(t *testing.T, admins ...string) *env {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR is not set")
	}
	db := dbtest.New(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })

	sender := &fakeSender{}
	return &env{
		svc:    NewService(db.Pool, rdb, sender, admins, slog.New(slog.DiscardHandler)),
		sender: sender,
		db:     db,
	}
}

func randomPhone() string {
	var b strings.Builder
	b.WriteString("+77")
	digits := make([]byte, 10)
	_, _ = rand.Read(digits)
	for _, d := range digits {
		fmt.Fprintf(&b, "%d", d%10)
	}
	return b.String()
}

func randomIP() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("10.%d.%d.%d", b[0], b[1], b[2])
}

func randomEmail() string {
	return "owner-" + strings.ToLower(rand.Text()[:10]) + "@example.com"
}

func (e *env) seedOwner(t *testing.T, email string) (organizerID, memberID string) {
	t.Helper()
	ctx := t.Context()
	slug := "org-" + strings.ToLower(rand.Text()[:10])
	if err := e.db.Pool.QueryRow(ctx,
		`INSERT INTO organizers (name, slug) VALUES ('Org', $1) RETURNING id`, slug).Scan(&organizerID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx,
		`INSERT INTO organizer_members (organizer_id, email, role) VALUES ($1, $2, 'owner') RETURNING id`,
		organizerID, email).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	return organizerID, memberID
}

func TestBuyerLogin(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	phone := randomPhone()

	if err := e.svc.RequestCode(ctx, auth.KindBuyer, phone, randomIP()); err != nil {
		t.Fatalf("RequestCode() error = %v", err)
	}
	token, sess, err := e.svc.Login(ctx, auth.KindBuyer, phone, e.sender.code(t, phone))
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if sess.Kind != auth.KindBuyer || sess.SubjectID == "" || sess.OrganizerID != "" {
		t.Fatalf("session = %+v, want buyer with id", sess)
	}

	got, err := e.svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if got.Principal != sess.Principal {
		t.Errorf("Authenticate() = %+v, want %+v", got.Principal, sess.Principal)
	}

	// Повторный вход с тем же телефоном — тот же покупатель.
	if err := e.svc.RequestCode(ctx, auth.KindBuyer, phone, randomIP()); !errors.Is(err, ErrTooManyRequests) {
		t.Fatalf("immediate resend: err = %v, want ErrTooManyRequests", err)
	}
	var buyers int
	if err := e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM buyers WHERE phone = $1`, phone).Scan(&buyers); err != nil {
		t.Fatal(err)
	}
	if buyers != 1 {
		t.Errorf("buyers with phone = %d, want 1", buyers)
	}
}

func TestCodeIsSingleUse(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	phone := randomPhone()
	if err := e.svc.RequestCode(ctx, auth.KindBuyer, phone, randomIP()); err != nil {
		t.Fatal(err)
	}
	code := e.sender.code(t, phone)

	if _, _, err := e.svc.Login(ctx, auth.KindBuyer, phone, code); err != nil {
		t.Fatalf("first login: %v", err)
	}
	if _, _, err := e.svc.Login(ctx, auth.KindBuyer, phone, code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("second login with same code: err = %v, want ErrInvalidCode", err)
	}
}

// TestCodeIsSingleUseUnderConcurrency: один верный код, много одновременных
// входов — сессию получает ровно один.
func TestCodeIsSingleUseUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	phone := randomPhone()
	if err := e.svc.RequestCode(ctx, auth.KindBuyer, phone, randomIP()); err != nil {
		t.Fatal(err)
	}
	code := e.sender.code(t, phone)

	const attempts = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ok      int
		unknown []error
	)
	start := make(chan struct{})
	for range attempts {
		wg.Go(func() {
			<-start
			_, _, err := e.svc.Login(context.Background(), auth.KindBuyer, phone, code)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrInvalidCode):
			default:
				unknown = append(unknown, err)
			}
		})
	}
	close(start)
	wg.Wait()

	if len(unknown) > 0 {
		t.Fatalf("unexpected errors: %v", unknown)
	}
	if ok != 1 {
		t.Fatalf("%d sessions from one code, want exactly 1", ok)
	}
}

func TestWrongCodeAttemptsAreLimited(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	phone := randomPhone()
	if err := e.svc.RequestCode(ctx, auth.KindBuyer, phone, randomIP()); err != nil {
		t.Fatal(err)
	}
	code := e.sender.code(t, phone)
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}

	for i := range maxCodeAttempts {
		if _, _, err := e.svc.Login(ctx, auth.KindBuyer, phone, wrong); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("wrong attempt %d: err = %v, want ErrInvalidCode", i+1, err)
		}
	}
	// После исчерпания попыток не принимается даже верный код.
	if _, _, err := e.svc.Login(ctx, auth.KindBuyer, phone, code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("correct code after lockout: err = %v, want ErrInvalidCode", err)
	}
}

func TestCodeRequestsPerIPAreLimited(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	ip := randomIP()

	for i := range maxCodesPerIPHour {
		if err := e.svc.RequestCode(ctx, auth.KindBuyer, randomPhone(), ip); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	if err := e.svc.RequestCode(ctx, auth.KindBuyer, randomPhone(), ip); !errors.Is(err, ErrTooManyRequests) {
		t.Fatalf("request over IP limit: err = %v, want ErrTooManyRequests", err)
	}
}

func TestOrganizerLogin(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	email := randomEmail()
	organizerID, memberID := e.seedOwner(t, email)

	// Адрес нормализуется: регистр и пробелы не мешают войти.
	if err := e.svc.RequestCode(ctx, auth.KindOrganizer, "  "+strings.ToUpper(email[:1])+email[1:], randomIP()); err != nil {
		t.Fatalf("RequestCode() error = %v", err)
	}
	_, sess, err := e.svc.Login(ctx, auth.KindOrganizer, email, e.sender.code(t, email))
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	want := auth.Principal{Kind: auth.KindOrganizer, SubjectID: memberID, OrganizerID: organizerID}
	if sess.Principal != want {
		t.Errorf("principal = %+v, want %+v", sess.Principal, want)
	}
}

func TestUnknownOrganizerGetsNoCode(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	email := randomEmail()

	// Ответ такой же, как для известного адреса, но код не отправлен.
	if err := e.svc.RequestCode(ctx, auth.KindOrganizer, email, randomIP()); err != nil {
		t.Fatalf("RequestCode() error = %v", err)
	}
	if e.sender.sent != 0 {
		t.Fatalf("codes sent = %d, want 0", e.sender.sent)
	}
	if _, _, err := e.svc.Login(ctx, auth.KindOrganizer, email, "123456"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("login of unknown organizer: err = %v, want ErrInvalidCode", err)
	}
}

func TestAdminLogin(t *testing.T) {
	admin := randomEmail()
	e := newEnv(t, admin)
	ctx := t.Context()

	if err := e.svc.RequestCode(ctx, auth.KindAdmin, admin, randomIP()); err != nil {
		t.Fatal(err)
	}
	_, sess, err := e.svc.Login(ctx, auth.KindAdmin, admin, e.sender.code(t, admin))
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if sess.Kind != auth.KindAdmin || sess.SubjectID != admin {
		t.Errorf("session = %+v, want admin %s", sess, admin)
	}

	// Не из списка администраторов — код не отправляется.
	stranger := randomEmail()
	if err := e.svc.RequestCode(ctx, auth.KindAdmin, stranger, randomIP()); err != nil {
		t.Fatal(err)
	}
	if e.sender.sent != 1 {
		t.Errorf("codes sent = %d, want 1 (only to the admin)", e.sender.sent)
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	phone := randomPhone()
	if err := e.svc.RequestCode(ctx, auth.KindBuyer, phone, randomIP()); err != nil {
		t.Fatal(err)
	}
	token, _, err := e.svc.Login(ctx, auth.KindBuyer, phone, e.sender.code(t, phone))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Logout(ctx, token); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Authenticate() after logout: err = %v, want ErrNoSession", err)
	}
}

func TestNormalizeAddress(t *testing.T) {
	tests := []struct {
		kind    auth.Kind
		in      string
		want    string
		wantErr bool
	}{
		{auth.KindBuyer, "+77001234567", "+77001234567", false},
		{auth.KindBuyer, " +77001234567 ", "+77001234567", false},
		{auth.KindBuyer, "87001234567", "", true},
		{auth.KindBuyer, "+7 700 123 45 67", "", true},
		{auth.KindOrganizer, "Owner@Example.com", "owner@example.com", false},
		{auth.KindOrganizer, "Owner <owner@example.com>", "", true},
		{auth.KindAdmin, "not-an-email", "", true},
		{auth.Kind("root"), "a@b.c", "", true},
	}
	for _, tt := range tests {
		got, err := NormalizeAddress(tt.kind, tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("NormalizeAddress(%s, %q) = %q, %v; want %q, err %v", tt.kind, tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

// Сидер нагрузочного эксперимента выдаёт сессии без кода: покупатель тот же,
// что при обычном входе, сессия проходит проверку.
func TestIssueBuyerSession(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	phone := randomPhone()
	token, err := e.svc.IssueBuyerSession(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := e.svc.Authenticate(ctx, token)
	if err != nil || sess.Kind != auth.KindBuyer || sess.SubjectID == "" {
		t.Fatalf("Authenticate() = %+v, %v", sess, err)
	}
	again, err := e.svc.IssueBuyerSession(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	if s2, _ := e.svc.Authenticate(ctx, again); s2.SubjectID != sess.SubjectID {
		t.Errorf("second session for the same phone: buyer %s, want %s", s2.SubjectID, sess.SubjectID)
	}
	if _, err := e.svc.IssueBuyerSession(ctx, "not a phone"); !errors.Is(err, ErrInvalidAddress) {
		t.Errorf("bad phone: err = %v", err)
	}
}
