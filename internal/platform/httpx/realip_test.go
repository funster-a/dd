package httpx

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestRealIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("127.0.0.0/8")}
	tests := []struct {
		name      string
		remote    string
		forwarded []string
		want      string
	}{
		{"direct client, no proxy", "203.0.113.7:5555", nil, "203.0.113.7"},
		{"direct client cannot spoof", "203.0.113.7:5555", []string{"1.2.3.4"}, "203.0.113.7"},
		{"behind nginx", "172.18.0.5:40000", []string{"203.0.113.7"}, "203.0.113.7"},
		{"client-supplied value on the left is ignored", "172.18.0.5:40000", []string{"6.6.6.6, 203.0.113.7"}, "203.0.113.7"},
		{"several headers", "172.18.0.5:40000", []string{"6.6.6.6", "203.0.113.7"}, "203.0.113.7"},
		{"chain of trusted proxies", "172.18.0.5:40000", []string{"203.0.113.7, 172.18.0.9"}, "203.0.113.7"},
		{"client inside the trusted network", "172.18.0.5:40000", []string{"172.18.0.20"}, "172.18.0.20"},
		{"garbage stops the walk", "172.18.0.5:40000", []string{"203.0.113.7, junk, 198.51.100.1"}, "198.51.100.1"},
		{"proxy without header", "172.18.0.5:40000", nil, "172.18.0.5"},
		{"ipv6 client", "127.0.0.1:1", []string{"2001:db8::1"}, "2001:db8::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			h := RealIP(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = ClientIP(r) }))
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			for _, v := range tt.forwarded {
				r.Header.Add("X-Forwarded-For", v)
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
			if got != tt.want {
				t.Errorf("client ip = %q, want %q", got, tt.want)
			}
		})
	}
}
