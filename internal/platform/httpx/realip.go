package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// RealIP подставляет в r.RemoteAddr адрес клиента, когда запрос пришёл
// через доверенный обратный прокси (nginx сайта, ADR 020). Без этого за
// прокси у всех покупателей один адрес — адрес nginx, и лимиты по IP
// становятся общими на всех.
//
// Адрес берётся из X-Forwarded-For справа налево: первый недоверенный адрес
// и есть клиент. Левые значения мог подставить сам клиент, им верить нельзя.
// Запрос не от доверенного прокси заголовком не управляет вовсе.
func RealIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip, ok := clientFromForwarded(r.RemoteAddr, r.Header.Values("X-Forwarded-For"), trusted); ok {
				r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientFromForwarded(remote string, forwarded []string, trusted []netip.Prefix) (netip.Addr, bool) {
	peer, ok := parseAddr(remote)
	if !ok || !isTrusted(peer, trusted) {
		return netip.Addr{}, false
	}
	var hops []string
	for _, h := range forwarded {
		hops = append(hops, strings.Split(h, ",")...)
	}
	var last netip.Addr
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // мусор в цепочке: дальше влево верить нечему
		}
		a = a.Unmap()
		last = a
		if !isTrusted(a, trusted) {
			return a, true
		}
	}
	// Вся цепочка из доверенных адресов (клиент во внутренней сети) —
	// самый левый достоверный адрес.
	return last, last.IsValid()
}

func parseAddr(hostport string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP — адрес клиента запроса без порта (после RealIP).
func ClientIP(r *http.Request) string {
	if a, ok := parseAddr(r.RemoteAddr); ok {
		return a.String()
	}
	return r.RemoteAddr
}
