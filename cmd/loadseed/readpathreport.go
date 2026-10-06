package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Отчёт по читающему контуру (ADR 031): прогоны rp__<вариант>__<покупателей>__<повтор>
// серии loadtest/readpath.sh. Главная метрика — процессорное время ведущего
// узла PostgreSQL за штурм стадиона; рядом — реплики, тяжёлые запросы и путь
// покупателя.

var rpOrder = []string{"before", "queries", "replica", "edge"}

var rpNames = map[string]string{
	"before":  "До: сборка из main",
	"queries": "Запросы исправлены",
	"replica": "+ чтения с реплик",
	"edge":    "+ кэш на краю",
}

type rpRun struct {
	Variant string
	DB      map[string]struct {
		CPU     float64 `json:"cpu_s"`
		Primary bool    `json:"primary"`
		Top     []struct {
			Query   string  `json:"query"`
			Calls   int     `json:"calls"`
			TotalMS float64 `json:"total_ms"`
		} `json:"top"`
	}
	K6 struct {
		Won       float64            `json:"won"`
		Tickets   float64            `json:"tickets"`
		Failed    float64            `json:"failed"`
		Order     map[string]float64 `json:"order_ms"`
		Summary   map[string]float64 `json:"summary_ms"`
		Section   map[string]float64 `json:"section_ms"`
		WonAt     map[string]float64 `json:"won_at_ms"`
		QueuePoll float64            `json:"queue_polls"`
	}
	Check checkResult
}

var sqlcName = regexp.MustCompile(`-- name: (\w+)`)

func readPathReport(args []string) error {
	fs := flag.NewFlagSet("readpath-report", flag.ContinueOnError)
	in := fs.String("in", "", "каталог серии loadtest/readpath.sh")
	out := fs.String("out", "", "куда положить таблицы")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return errors.New("-in and -out are required")
	}
	entries, err := os.ReadDir(*in)
	if err != nil {
		return err
	}
	var runs []rpRun
	for _, e := range entries {
		parts := strings.Split(e.Name(), "__")
		if !e.IsDir() || len(parts) != 4 || parts[0] != "rp" {
			continue
		}
		p := filepath.Join(*in, e.Name())
		r := rpRun{Variant: parts[1]}
		if err := readJSON(filepath.Join(p, "db.json"), &r.DB); err != nil {
			fmt.Fprintln(os.Stderr, "skip", p, err)
			continue
		}
		if err := readJSON(filepath.Join(p, "summary.json"), &r.K6); err != nil {
			fmt.Fprintln(os.Stderr, "skip", p, err)
			continue
		}
		_ = readJSON(filepath.Join(p, "check.json"), &r.Check)
		runs = append(runs, r)
	}
	if len(runs) == 0 {
		return errors.New("no runs found")
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*out, "tables.md"), []byte(rpTables(runs)), 0o600)
}

func rpTables(runs []rpRun) string {
	dec := func(v float64) string { return strings.Replace(strconv.FormatFloat(v, 'f', 1, 64), ".", ",", 1) }
	sec := func(ms float64) string { return dec(ms / 1000) }
	var b strings.Builder
	b.WriteString("<!-- Сгенерировано: go run ./cmd/loadseed readpath-report. Не править руками. -->\n\n")
	b.WriteString("Среднее по прогонам. Процессорное время узла — за весь прогон k6 (cgroup контейнера).\n\n")
	b.WriteString("| Вариант | Прогонов | Ведущий, с CPU | Реплики, с CPU | Купили | Билетов | Ошибок | Заказ p95, с | Сводка p95, с | Сектор p95, с | Последняя покупка, с | Двойных броней |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, v := range rpOrder {
		var n, dbl int
		var prim, repl, won, tickets, failed, order, summary, section, last float64
		for _, r := range runs {
			if r.Variant != v {
				continue
			}
			n++
			for _, node := range r.DB {
				if node.Primary {
					prim += node.CPU
				} else {
					repl += node.CPU
				}
			}
			won += r.K6.Won
			tickets += r.K6.Tickets
			failed += r.K6.Failed
			order += r.K6.Order["p(95)"]
			summary += r.K6.Summary["p(95)"]
			section += r.K6.Section["p(95)"]
			last += r.K6.WonAt["max"]
			dbl += r.Check.DoubleBooked + r.Check.Mismatched
		}
		if n == 0 {
			continue
		}
		k := float64(n)
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %d |\n", rpNames[v], n, dec(prim/k), dec(repl/k),
			fmtNum(won/k), fmtNum(tickets/k), fmtNum(failed/k), sec(order/k), sec(summary/k), sec(section/k), sec(last/k), dbl)
	}

	b.WriteString("\n### Тяжёлые запросы ведущего узла\n\nСуммарное время выполнения за прогон (pg_stat_statements, включает ожидание блокировок), среднее по прогонам, секунд.\n\n")
	names := map[string]bool{}
	byVar := map[string]map[string]float64{}
	calls := map[string]map[string]float64{}
	for _, r := range runs {
		for _, node := range r.DB {
			if !node.Primary {
				continue
			}
			for _, q := range node.Top {
				m := sqlcName.FindStringSubmatch(q.Query)
				if m == nil {
					continue // служебные запросы: pg_stat_statements, миграции
				}
				name := m[1]
				names[name] = true
				if byVar[r.Variant] == nil {
					byVar[r.Variant], calls[r.Variant] = map[string]float64{}, map[string]float64{}
				}
				byVar[r.Variant][name] += q.TotalMS / 1000
				calls[r.Variant][name] += float64(q.Calls)
			}
		}
	}
	count := map[string]float64{}
	for _, r := range runs {
		count[r.Variant]++
	}
	var list []string
	for nm := range names {
		list = append(list, nm)
	}
	sort.Slice(list, func(i, j int) bool { return byVar["before"][list[i]] > byVar["before"][list[j]] })
	b.WriteString("| Запрос |")
	sepLine := "|---|"
	for _, v := range rpOrder {
		if count[v] > 0 {
			b.WriteString(" " + rpNames[v] + " |")
			sepLine += "---:|"
		}
	}
	b.WriteString("\n" + sepLine + "\n")
	for _, nm := range list {
		b.WriteString("| `" + nm + "` |")
		for _, v := range rpOrder {
			if count[v] == 0 {
				continue
			}
			t, ok := byVar[v][nm]
			if !ok {
				b.WriteString(" — |")
				continue
			}
			fmt.Fprintf(&b, " %s (%s выз.) |", dec(t/count[v]), fmtNum(calls[v][nm]/count[v]))
		}
		b.WriteString("\n")
	}
	return b.String()
}
