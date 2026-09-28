package identity

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/funster-a/dd/internal/platform/auth"
)

// TestHTTPLoginFlow проходит вход покупателя через HTTP так же, как фронтенд:
// код → сессия → /me → выход → старый токен больше не действует.
func TestHTTPLoginFlow(t *testing.T) {
	e := newEnv(t)
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(e.svc.Middleware)
		r.Mount("/auth", e.svc.Routes())
		r.With(auth.Require(auth.KindBuyer)).Get("/me", HandleMe)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	phone := randomPhone()
	// call выполняет запрос и возвращает код ответа и тело.
	call := func(method, path, token string, body any) (int, []byte) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, &buf)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	expect := func(method, path, token string, body any, status int) []byte {
		t.Helper()
		got, data := call(method, path, token, body)
		if got != status {
			t.Fatalf("%s %s: status = %d, want %d; body %s", method, path, got, status, data)
		}
		return data
	}

	expect(http.MethodPost, "/v1/auth/codes", "", map[string]string{"kind": "buyer", "phone": "8700"}, http.StatusBadRequest)
	expect(http.MethodPost, "/v1/auth/codes", "", map[string]string{"kind": "buyer", "phone": phone}, http.StatusAccepted)

	data := expect(http.MethodPost, "/v1/auth/sessions", "", map[string]string{
		"kind": "buyer", "phone": phone, "code": e.sender.code(t, phone),
	}, http.StatusCreated)
	var login loginResponse
	if err := json.Unmarshal(data, &login); err != nil {
		t.Fatal(err)
	}
	if login.Token == "" || login.Principal.Kind != auth.KindBuyer {
		t.Fatalf("login response = %+v", login)
	}

	expect(http.MethodGet, "/v1/me", "", nil, http.StatusUnauthorized)
	expect(http.MethodGet, "/v1/me", login.Token, nil, http.StatusOK)
	expect(http.MethodDelete, "/v1/auth/session", login.Token, nil, http.StatusNoContent)
	expect(http.MethodGet, "/v1/me", login.Token, nil, http.StatusUnauthorized)
}
