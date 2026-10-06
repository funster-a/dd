package main

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Отчёты по отказу хранилища посреди продажи: прогоны
// chaos__<вариант>__<поток>__<повтор> серий loadtest/pgchaos.sh (ADR 028) и
// loadtest/redischaos.sh (ADR 029). Те же метрики, что у chaos-report, плюс
// время до восстановления записи и то, что серия проверила после прогона:
// подтверждённые заказы, пропавшие при переключении, или холды Redis против
// базы.

type failoverSpec struct {
	command   string
	order     []string          // варианты в порядке таблицы
	names     map[string]string // вариант → название в таблице
	short     map[string]string // вариант с отказом → подпись на графике
	switchCol string            // заголовок колонки времени до восстановления записи
	acked     bool              // колонка «потеряно подтверждённых»
	holds     bool              // колонки холдов Redis
	title     string
	subtitle  string
	xLabel    string
}

var pgFailover = failoverSpec{
	command: "pgchaos-report",
	order:   []string{"async-none-new", "async-kill-old", "async-kill-new", "sync-none-new", "sync-kill-new"},
	names: map[string]string{
		"async-none-new": "Асинхронная, без отказа, с исправлением",
		"async-kill-old": "Асинхронная, падение ведущего, сборка до исправления",
		"async-kill-new": "Асинхронная, падение ведущего, с исправлением",
		"sync-none-new":  "Синхронная, без отказа, с исправлением",
		"sync-kill-new":  "Синхронная, падение ведущего, с исправлением",
	},
	short:     map[string]string{"async-kill-old": "Асинхронная, до исправления", "async-kill-new": "Асинхронная", "sync-kill-new": "Синхронная"},
	switchCol: "Повышение реплики, с",
	acked:     true,
	title:     "Неудачные попытки заказа вокруг падения ведущего узла PostgreSQL",
	subtitle:  "SIGKILL ведущего узла в момент 0, в среднем на прогон. Повторы клиента с тем же ключом.",
	xLabel:    "секунд от падения ведущего",
}

var redisFailover = failoverSpec{
	command: "redischaos-report",
	order:   []string{"single-rdb-none", "single-rdb-kill", "single-aof-kill", "sentinel-none", "sentinel-kill"},
	names: map[string]string{
		"single-rdb-none": "Один Redis, без отказа",
		"single-rdb-kill": "Один Redis без журнала, падение",
		"single-aof-kill": "Один Redis с журналом, падение",
		"sentinel-none":   "Sentinel, без отказа",
		"sentinel-kill":   "Sentinel, падение ведущего",
	},
	short:     map[string]string{"single-rdb-kill": "Один Redis без журнала", "single-aof-kill": "Один Redis с журналом", "sentinel-kill": "Sentinel"},
	switchCol: "Запись снова доступна, с",
	holds:     true,
	title:     "Неудачные попытки заказа вокруг падения Redis",
	subtitle:  "SIGKILL ведущего Redis в момент 0, без Sentinel он поднимается на 20-й секунде. В среднем на прогон.",
	xLabel:    "секунд от падения Redis",
}

func pgChaosReport(args []string) error    { return failoverReport(pgFailover, args) }
func redisChaosReport(args []string) error { return failoverReport(redisFailover, args) }

func failoverReport(spec failoverSpec, args []string) error {
	fs := flag.NewFlagSet(spec.command, flag.ContinueOnError)
	in := fs.String("in", "", "каталог серии")
	out := fs.String("out", "", "куда положить CSV, таблицы и графики")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return errors.New("-in and -out are required")
	}
	runs, err := loadChaosRuns("", *in)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return errors.New("no runs found")
	}
	if err := os.MkdirAll(filepath.Join(*out, "charts"), 0o750); err != nil {
		return err
	}
	if err := writeChaosCSV(filepath.Join(*out, "results.csv"), runs); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "tables.md"), []byte(spec.tables(runs)), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*out, "charts", "failed-attempts.svg"), []byte(spec.chart(runs)), 0o600)
}

func (spec failoverSpec) variants(runs []chaosRun) []string {
	var vs []string
	for _, v := range spec.order {
		if slices.ContainsFunc(runs, func(r chaosRun) bool { return r.Variant == v }) {
			vs = append(vs, v)
		}
	}
	return vs
}

func (spec failoverSpec) name(v string) string {
	if n, ok := spec.names[v]; ok {
		return n
	}
	return v
}

func (spec failoverSpec) tables(runs []chaosRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- Сгенерировано: go run ./cmd/loadseed %s. Не править руками. -->\n\n", spec.command)
	b.WriteString("Сумма по прогонам варианта. «Ушли без билета» — запрос не удался и после повторов с тем же ключом.")
	if spec.acked {
		b.WriteString(" «Потеряно подтверждённых» — клиент получил 201, а заказа после переключения в базе нет.")
	}
	if spec.holds {
		b.WriteString(" «Призрачные холды» — ключ в Redis есть, а в базе место свободно или держится другим заказом; " +
			"«пропало холдов» — место держится в базе, а ключа в Redis нет.")
	}
	b.WriteString("\n\n| Вариант | Прогонов | Запросов | Сразу | После повторов | Ушли без билета | Неудачных попыток | Причины | " +
		spec.switchCol + " | Восстановление max, с |")
	sep := "|---|---:|---:|---:|---:|---:|---:|---|---|---:|"
	if spec.acked {
		b.WriteString(" Потеряно подтверждённых |")
		sep += "---:|"
	}
	b.WriteString(" Двойных выполнений | Двойных броней |")
	sep += "---:|---:|"
	if spec.holds {
		b.WriteString(" Призрачных холдов | Пропало холдов |")
		sep += "---:|---:|"
	}
	b.WriteString("\n" + sep + "\n")
	for _, v := range spec.variants(runs) {
		var n, dups, dbl, acked, missing, ghost, lostHolds int
		var req, first, retry, gave, failed, recMax float64
		var sw []float64
		reasons := map[string]float64{}
		for _, r := range runs {
			if r.Variant != v {
				continue
			}
			n++
			req += r.K6.Requests
			first += r.K6.OKFirst
			retry += r.K6.OKRetry
			gave += r.K6.GaveUp
			recMax = max(recMax, r.K6.RecoveryMS["max"]/1000)
			dups += r.Dups.Dups
			dbl += r.Check.DoubleBooked + r.Check.Mismatched
			acked += r.Dups.Acked
			missing += r.Dups.AckedMissing
			ghost += r.Holds.GhostFree + r.Holds.GhostOther
			lostHolds += r.Holds.Missing
			if !math.IsNaN(r.Promote) {
				sw = append(sw, r.Promote)
			}
			for k, c := range r.FailReasons {
				reasons[k] += c
				failed += c
			}
		}
		var rs []string
		for k, c := range reasons {
			rs = append(rs, fmt.Sprintf("%s %s", reasonRu(k), fmtNum(c)))
		}
		slices.Sort(rs)
		why := strings.Join(rs, ", ")
		if why == "" {
			why = "—"
		}
		swCol := "—"
		if len(sw) > 0 {
			swCol = fmt.Sprintf("%s–%s", fmtNum(round1(slices.Min(sw))), fmtNum(round1(slices.Max(sw))))
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s | %s | %s | %s |", spec.name(v), n, fmtNum(req),
			fmtNum(first), fmtNum(retry), fmtNum(gave), fmtNum(failed), why, swCol, fmtNum(round1(recMax)))
		if spec.acked {
			fmt.Fprintf(&b, " %d из %d |", missing, acked)
		}
		fmt.Fprintf(&b, " %d | %d |", dups, dbl)
		if spec.holds {
			fmt.Fprintf(&b, " %d | %d |", ghost, lostHolds)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// chart — неудачные попытки по секундам вокруг отказа, по варианту с отказом
// на линию, в среднем на прогон: прогонов у вариантов может быть разное
// число.
func (spec failoverSpec) chart(runs []chaosRun) string {
	colors := []string{"#eb6834", "#2a78d6", "#1baf7a", "#8a6bd1"}
	var ss []series
	i := 0
	for _, v := range spec.variants(runs) {
		label, ok := spec.short[v]
		if !ok {
			continue
		}
		key := spec.command + "-" + v
		seriesStyle[key] = struct{ color, marker string }{colors[i%len(colors)], []string{"circle", "square", "diamond", "circle"}[i%4]}
		strategyRu[key] = label
		i++
		by := map[int]float64{}
		n := 0.0
		for _, r := range runs {
			if r.Variant == v {
				n++
				for s, c := range r.FailedBySec {
					by[s] += c
				}
			}
		}
		s := series{strategy: key}
		for sec := -3; sec <= 30; sec++ {
			c := round1(by[sec] / n)
			s.points = append(s.points, point{x: strconv.Itoa(sec), stat: stat{Mean: c, Min: c, Max: c}})
		}
		ss = append(ss, s)
	}
	return lineChart(chartSpec{title: spec.title, subtitle: spec.subtitle, unit: "попыток", series: ss, xLabel: spec.xLabel})
}
