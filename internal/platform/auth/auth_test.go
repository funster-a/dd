package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequire(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := Require(KindOrganizer)(ok)

	tests := []struct {
		name      string
		principal *Principal
		want      int
	}{
		{"anonymous", nil, http.StatusUnauthorized},
		{"wrong kind", &Principal{Kind: KindBuyer, SubjectID: "b"}, http.StatusForbidden},
		{"allowed", &Principal{Kind: KindOrganizer, SubjectID: "m", OrganizerID: "o"}, http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tt.principal != nil {
				r = r.WithContext(WithPrincipal(r.Context(), *tt.principal))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
