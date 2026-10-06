package db

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Соединения пула реплик начинают перебор с разных хостов по кругу (ADR 031),
// записи одного хоста (с TLS и без, sslmode=prefer) не разрываются, ни один
// хост не теряется.
func TestRotatedHosts(t *testing.T) {
	cfg, err := pgx.ParseConfig("postgres://u@h1:1,h2:2,h3:3/db")
	if err != nil {
		t.Fatal(err)
	}
	groups := hostGroups(cfg)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	for start, want := range []string{"h1 h1 h2 h2 h3 h3", "h2 h2 h3 h3 h1 h1", "h3 h3 h1 h1 h2 h2"} {
		c := cfg.Copy()
		setHosts(c, rotated(groups, start))
		got := []string{c.Host}
		for _, f := range c.Fallbacks {
			got = append(got, f.Host)
		}
		if strings.Join(got, " ") != want {
			t.Errorf("start %d: hosts = %v, want %s", start, got, want)
		}
	}
}
