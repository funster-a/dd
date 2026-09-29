package ticket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/funster-a/dd/internal/platform/auth"
	"github.com/funster-a/dd/internal/platform/events"
)

// issued выпускает билеты оплаченного заказа на места seats и возвращает
// их ссылки (содержимое QR).
func (e *env) issued(t *testing.T, seats ...string) []string {
	t.Helper()
	order, buyer := e.order(t, "paid", seats...)
	if err := e.svc.Issue(t.Context(), events.OrderPaidEvent{OrderID: order}); err != nil {
		t.Fatal(err)
	}
	ts, err := e.svc.ForOrder(t.Context(), buyer, order)
	if err != nil {
		t.Fatal(err)
	}
	urls := make([]string, len(ts))
	for i, tk := range ts {
		urls[i] = tk.URL
	}
	return urls
}

func (e *env) scanner(t *testing.T) ScannerSession {
	t.Helper()
	sc, err := e.svc.CreateScanner(t.Context(), e.org, e.event, "Вход А")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := e.svc.ScannerByToken(t.Context(), sc.Token)
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// scan — онлайн-сканирование; время ставит сервер.
func (e *env) scan(t *testing.T, sess ScannerSession, code string) ScanResult {
	t.Helper()
	res, err := e.svc.Scan(t.Context(), sess, "phone-1", []ScanInput{{ClientScanID: uuid.NewString(), Code: code}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return res[0]
}

func (e *env) acceptedScans(t *testing.T, code string) int {
	t.Helper()
	id, _ := e.svc.signer.Parse(tokenOf(code))
	var n int
	if err := e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_scans WHERE ticket_id = $1 AND result = 'accepted'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestScannerLinks(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	if _, err := e.svc.CreateScanner(ctx, e.org, e.event, "  "); err == nil {
		t.Error("empty name accepted")
	}
	if _, err := e.svc.CreateScanner(ctx, uuid.NewString(), e.event, "Чужой"); !errors.Is(err, ErrNotFound) {
		t.Errorf("someone else's event: err = %v, want ErrNotFound", err)
	}
	sc, err := e.svc.CreateScanner(ctx, e.org, e.event, "Вход А")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sc.URL, "https://tickets.test/scan#") || sc.Token == "" || !strings.HasSuffix(sc.URL, sc.Token) {
		t.Errorf("scanner = %+v", sc)
	}
	list, err := e.svc.ListScanners(ctx, e.org, e.event)
	if err != nil || len(list) != 1 || list[0].Token != "" || list[0].URL != "" {
		t.Errorf("list = %+v, %v; want one link without its token", list, err)
	}
	var stored []byte
	if err := e.db.Pool.QueryRow(ctx, `SELECT token_hash FROM scanner_links WHERE id = $1`, sc.ID).Scan(&stored); err != nil ||
		bytes.Contains(stored, []byte(sc.Token)) || len(stored) != 32 {
		t.Errorf("token must be stored as a hash: %x, %v", stored, err)
	}

	if _, err := e.svc.ScannerByToken(ctx, sc.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ScannerByToken(ctx, sc.Token+"x"); !errors.Is(err, ErrScannerRevoked) {
		t.Errorf("wrong token: err = %v", err)
	}
	if err := e.svc.RevokeScanner(ctx, e.org, sc.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RevokeScanner(ctx, e.org, sc.ID); err != nil {
		t.Errorf("repeated revoke: %v", err)
	}
	if _, err := e.svc.ScannerByToken(ctx, sc.Token); !errors.Is(err, ErrScannerRevoked) {
		t.Errorf("revoked link still works: %v", err)
	}
	if err := e.svc.RevokeScanner(ctx, uuid.NewString(), sc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke by another organizer: err = %v", err)
	}
}

func TestScanResults(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	codes := e.issued(t, e.seats[0], e.seats[1])
	sess := e.scanner(t)
	first := e.scan(t, sess, codes[0])
	if first.Result != ScanAccepted || first.Seat != "1" || *first.Row != "1" {
		t.Fatalf("first scan = %+v, want accepted for row 1 seat 1", first)
	}
	again := e.scan(t, sess, codes[0])
	if again.Result != ScanDuplicate || again.FirstScannedAt == nil || !again.FirstScannedAt.Equal(*first.FirstScannedAt) {
		t.Errorf("second scan = %+v, want duplicate with the first time", again)
	}
	// Токен без ссылки тоже годится: так его передаёт сканер без сети.
	if r := e.scan(t, sess, tokenOf(codes[1])); r.Result != ScanAccepted {
		t.Errorf("bare token = %+v", r)
	}
	b := []byte(tokenOf(codes[0]))
	b[3] ^= 1
	for name, code := range map[string]string{"forged": string(b), "garbage": "hello", "empty": ""} {
		if r := e.scan(t, sess, code); r.Result != ScanInvalid {
			t.Errorf("%s code = %+v, want invalid", name, r)
		}
	}

	// Повторная отправка того же сканирования (обрыв связи) — тот же итог.
	in := []ScanInput{{ClientScanID: "resend-1", Code: codes[0]}}
	r1, err := e.svc.Scan(ctx, sess, "phone-1", in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := e.svc.Scan(ctx, sess, "phone-1", in, time.Now())
	if err != nil || r2[0].Result != r1[0].Result {
		t.Errorf("resend = %+v, %v; want %+v", r2, err, r1)
	}
	var rows int
	if err := e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM ticket_scans WHERE client_scan_id = 'resend-1'`).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("stored resend rows = %d, %v; want 1", rows, err)
	}

	// Аннулированный билет (возврат) не проходит.
	revokedCode := e.issued(t, e.seats[2])[0]
	id, _ := e.svc.signer.Parse(tokenOf(revokedCode))
	if _, err := e.db.Pool.Exec(ctx, `UPDATE tickets SET status = 'revoked', revoked_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if r := e.scan(t, sess, revokedCode); r.Result != ScanRevoked {
		t.Errorf("revoked ticket = %+v", r)
	}

	// Билет другого события сканером этого события не проходит.
	var other string
	if err := e.db.Pool.QueryRow(ctx, `INSERT INTO events (organizer_id, venue_id, seat_map_id, slug, title, status, starts_at, ends_at, published_at)
		SELECT organizer_id, venue_id, seat_map_id, 'other', 'Другое', 'published', starts_at, ends_at, now() FROM events WHERE id = $1
		RETURNING id`, e.event).Scan(&other); err != nil {
		t.Fatal(err)
	}
	otherScanner, err := e.svc.CreateScanner(ctx, e.org, other, "Другой вход")
	if err != nil {
		t.Fatal(err)
	}
	otherSess, err := e.svc.ScannerByToken(ctx, otherScanner.Token)
	if err != nil {
		t.Fatal(err)
	}
	if r := e.scan(t, otherSess, codes[1]); r.Result != ScanWrongEvent {
		t.Errorf("ticket of another event = %+v", r)
	}

	if _, err := e.svc.Scan(ctx, sess, "", nil, time.Now()); err == nil {
		t.Error("empty batch accepted")
	}
}

// Без сети два входа пропустили один билет. Устройство, отсканировавшее
// позже, синхронизировалось первым; засчитывается всё равно самое раннее
// сканирование, второе остаётся повторной попыткой (spec.md).
func TestOfflineSyncFirstScanWins(t *testing.T) {
	e := newEnv(t)
	code := e.issued(t, e.seats[0])[0]
	sess := e.scanner(t)
	t1 := time.Now().Add(-30 * time.Minute).Truncate(time.Microsecond)
	t2 := t1.Add(5 * time.Minute)

	late, err := e.svc.Scan(t.Context(), sess, "gate-b", []ScanInput{{ClientScanID: "b-1", Code: code, Offline: true, ScannedAt: t2}}, time.Now())
	if err != nil || late[0].Result != ScanAccepted {
		t.Fatalf("gate B = %+v, %v", late, err)
	}
	early, err := e.svc.Scan(t.Context(), sess, "gate-a", []ScanInput{{ClientScanID: "a-1", Code: code, Offline: true, ScannedAt: t1}}, time.Now())
	if err != nil || early[0].Result != ScanAccepted || !early[0].FirstScannedAt.Equal(t1) {
		t.Fatalf("gate A = %+v, %v; want accepted at %v", early, err, t1)
	}
	var results []string
	rows, err := e.db.Pool.Query(t.Context(), `SELECT client_scan_id || ':' || result FROM ticket_scans ORDER BY client_scan_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		results = append(results, s)
	}
	if strings.Join(results, ",") != "a-1:accepted,b-1:duplicate" {
		t.Errorf("scans = %v, want the earlier scan accepted", results)
	}
	v, err := e.svc.ByToken(t.Context(), tokenOf(code))
	if err != nil || v.Status != "used" {
		t.Errorf("ticket = %+v, %v; want used", v, err)
	}
	// Будущее время с устройства (сбитые часы) заменяется временем сервера.
	future, err := e.svc.Scan(t.Context(), sess, "gate-c", []ScanInput{{
		ClientScanID: "c-1", Code: e.issued(t, e.seats[1])[0], Offline: true, ScannedAt: time.Now().Add(time.Hour),
	}}, time.Now())
	if err != nil || future[0].FirstScannedAt == nil || future[0].FirstScannedAt.After(time.Now().Add(time.Second)) {
		t.Errorf("future scan = %+v, %v; want time clamped to now", future, err)
	}
}

// Несколько входов онлайн сканируют один билет одновременно: проход
// засчитывается ровно один, остальные контролёры видят «уже прошёл».
func TestConcurrentScansOfOneTicket(t *testing.T) {
	e := newEnv(t)
	code := e.issued(t, e.seats[0])[0]
	sess := e.scanner(t)
	var accepted, duplicate atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 30 {
		wg.Go(func() {
			<-start
			res, err := e.svc.Scan(t.Context(), sess, "gate", []ScanInput{{ClientScanID: uuid.NewString(), Code: code}}, time.Now())
			if err != nil {
				t.Error(i, err)
				return
			}
			switch res[0].Result {
			case ScanAccepted:
				accepted.Add(1)
			case ScanDuplicate:
				duplicate.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()
	if accepted.Load() != 1 || duplicate.Load() != 29 {
		t.Errorf("accepted = %d, duplicate = %d; want 1 and 29", accepted.Load(), duplicate.Load())
	}
	if n := e.acceptedScans(t, code); n != 1 {
		t.Errorf("accepted rows = %d, want 1", n)
	}
}

func TestManifest(t *testing.T) {
	e := newEnv(t)
	codes := e.issued(t, e.seats[0], e.seats[1])
	sess := e.scanner(t)
	e.scan(t, sess, codes[0])
	m, err := e.svc.Manifest(t.Context(), sess, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if m.Event.ID != e.event || len(m.Tickets) != 2 {
		t.Fatalf("manifest = %+v", m)
	}
	statuses := map[string]string{}
	for _, tk := range m.Tickets {
		statuses[tk.Seat] = tk.Status
	}
	if statuses["1"] != "used" || statuses["2"] != "issued" {
		t.Errorf("statuses = %v", statuses)
	}
	// Сканер без сети достаёт id из токена билета и находит его в списке.
	id, _ := e.svc.signer.Parse(tokenOf(codes[1]))
	found := false
	for _, tk := range m.Tickets {
		found = found || tk.ID == id
	}
	if !found {
		t.Error("ticket id from the QR token is not in the manifest")
	}
}

func TestScannerHTTP(t *testing.T) {
	e := newEnv(t)
	codes := e.issued(t, e.seats[0])

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if org := r.Header.Get("X-Test-Org"); org != "" {
				r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindOrganizer, SubjectID: "m", OrganizerID: org}))
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Route("/v1/organizer", func(r chi.Router) {
		r.Use(auth.Require(auth.KindOrganizer))
		e.svc.RegisterOrganizer(r)
	})
	r.Mount("/v1/scanner", e.svc.ScannerRoutes())
	srv := httptest.NewServer(r)
	defer srv.Close()
	do := func(method, path string, hdr map[string]string, body any) (int, []byte) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req, _ := http.NewRequestWithContext(t.Context(), method, srv.URL+path, &buf)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, data
	}
	org := map[string]string{"X-Test-Org": e.org, "Idempotency-Key": uuid.NewString()}

	code, body := do(http.MethodPost, "/v1/organizer/events/"+e.event+"/scanners", org, map[string]string{"name": "Вход А"})
	if code != http.StatusCreated {
		t.Fatalf("create scanner = %d %s", code, body)
	}
	var sc Scanner
	_ = json.Unmarshal(body, &sc)
	scanner := map[string]string{"Authorization": "Scanner " + sc.Token}

	if code, _ := do(http.MethodGet, "/v1/scanner/manifest", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("manifest without token = %d", code)
	}
	if code, body := do(http.MethodGet, "/v1/scanner/manifest", scanner, nil); code != http.StatusOK || !bytes.Contains(body, []byte(`"tickets"`)) {
		t.Errorf("manifest = %d %s", code, body)
	}
	scan := map[string]any{"device_id": "phone", "scans": []map[string]string{{"client_scan_id": "s1", "code": codes[0]}}}
	if code, body := do(http.MethodPost, "/v1/scanner/scans", scanner, scan); code != http.StatusOK || !bytes.Contains(body, []byte(`"result":"accepted"`)) {
		t.Errorf("scan = %d %s", code, body)
	}
	if code, body := do(http.MethodGet, "/v1/organizer/events/"+e.event+"/scanners", map[string]string{"X-Test-Org": e.org}, nil); code != http.StatusOK || bytes.Contains(body, []byte(sc.Token)) {
		t.Errorf("list = %d %s", code, body)
	}
	if code, _ := do(http.MethodDelete, "/v1/organizer/scanners/"+sc.ID, map[string]string{"X-Test-Org": e.org}, nil); code != http.StatusNoContent {
		t.Errorf("revoke = %d", code)
	}
	if code, _ := do(http.MethodPost, "/v1/scanner/scans", scanner, scan); code != http.StatusUnauthorized {
		t.Errorf("scan with revoked link = %d, want 401", code)
	}
}

// Онлайн-сканирование не перебивает засчитанный проход своим временем
// устройства: время онлайн ставит сервер.
func TestOnlineScanIgnoresDeviceClock(t *testing.T) {
	e := newEnv(t)
	code := e.issued(t, e.seats[0])[0]
	sess := e.scanner(t)
	if r := e.scan(t, sess, code); r.Result != ScanAccepted {
		t.Fatalf("first = %+v", r)
	}
	res, err := e.svc.Scan(t.Context(), sess, "gate", []ScanInput{{
		ClientScanID: "old-clock", Code: code, ScannedAt: time.Now().Add(-time.Hour),
	}}, time.Now())
	if err != nil || res[0].Result != ScanDuplicate {
		t.Errorf("online scan with an early device clock = %+v, %v; want duplicate", res, err)
	}
}
