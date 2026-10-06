package db

import (
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Пул реплик перемешивает хосты: экземпляры api расходятся по репликам
// (ADR 031), но ни один хост не теряется.
func TestShuffleHostsKeepsAll(t *testing.T) {
	seenFirst := map[string]bool{}
	for range 50 {
		cfg, err := pgx.ParseConfig("postgres://u@h1:1,h2:2,h3:3/db")
		if err != nil {
			t.Fatal(err)
		}
		shuffleHosts(cfg)
		// Записи одного хоста (с TLS и без, sslmode=prefer) идут подряд.
		all := []string{cfg.Host}
		for _, f := range cfg.Fallbacks {
			all = append(all, f.Host)
		}
		order := slices.Compact(slices.Clone(all))
		sorted := slices.Sorted(slices.Values(order))
		if strings.Join(sorted, ",") != "h1,h2,h3" || len(all) != 6 {
			t.Fatalf("hosts after shuffle = %v", all)
		}
		seenFirst[cfg.Host] = true
	}
	if len(seenFirst) < 2 {
		t.Errorf("first host never changed: %v", seenFirst)
	}
}
