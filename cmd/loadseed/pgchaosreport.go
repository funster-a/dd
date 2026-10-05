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

// Отчёт по переключению PostgreSQL посреди продажи (ADR 028): прогоны
// chaos__<репликация>-<отказ>-<сборка>__<поток>__<повтор> серии
// loadtest/pgchaos.sh. Те же метрики, что у chaos-report, плюс время
// повышения реплики и подтверждённые заказы, пропавшие при переключении.

var pgChaosOrder = []string{"async-none-new", "async-kill-old", "async-kill-new", "sync-none-new", "sync-kill-new"}

func pgChaosVariantRu(v string) string {
	parts := strings.Split(v, "-")
	if len(parts) != 3 {
		return v
	}
	mode := map[string]string{"async": "Асинхронная", "sync": "Синхронная"}[parts[0]]
	fault := map[string]string{"none": "без отказа", "kill": "падение ведущего"}[parts[1]]
	build := map[string]string{"old": "сборка до исправления", "new": "с исправлением"}[parts[2]]
	return fmt.Sprintf("%s, %s, %s", mode, fault, build)
}

// pgChaosShortRu — подпись линии на графике: там все варианты с отказом.
func pgChaosShortRu(v string) string {
	switch v {
	case "async-kill-old":
		return "Асинхронная, до исправления"
	case "async-kill-new":
		return "Асинхронная"
	case "sync-kill-new":
		return "Синхронная"
	}
	return v
}

func pgChaosReport(args []string) error {
	fs := flag.NewFlagSet("pgchaos-report", flag.ContinueOnError)
	in := fs.String("in", "", "каталог серии loadtest/pgchaos.sh")
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
	if err := os.WriteFile(filepath.Join(*out, "tables.md"), []byte(pgChaosTables(runs)), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*out, "charts", "failed-attempts.svg"), []byte(pgChaosChart(runs)), 0o600)
}

func pgChaosVariants(runs []chaosRun) []string {
	var vs []string
	for _, v := range pgChaosOrder {
		if slices.ContainsFunc(runs, func(r chaosRun) bool { return r.Variant == v }) {
			vs = append(vs, v)
		}
	}
	return vs
}

func pgChaosTables(runs []chaosRun) string {
	var b strings.Builder
	b.WriteString("<!-- Сгенерировано: go run ./cmd/loadseed pgchaos-report. Не править руками. -->\n\n")
	b.WriteString("Сумма по прогонам варианта. «Ушли без билета» — запрос не удался и после повторов с тем же ключом. " +
		"«Потеряно подтверждённых» — клиент получил 201, а заказа после переключения в базе нет.\n\n")
	b.WriteString("| Вариант | Прогонов | Запросов | Сразу | После повторов | Ушли без билета | Неудачных попыток | Причины | Повышение реплики, с | Восстановление max, с | Потеряно подтверждённых | Двойных выполнений | Двойных броней |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---|---|---:|---:|---:|---:|\n")
	for _, v := range pgChaosVariants(runs) {
		var n, dups, dbl, acked, missing int
		var req, first, retry, gave, failed, recMax float64
		var promote []float64
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
			if !math.IsNaN(r.Promote) {
				promote = append(promote, r.Promote)
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
		prom := "—"
		if len(promote) > 0 {
			prom = fmt.Sprintf("%s–%s", fmtNum(round1(slices.Min(promote))), fmtNum(round1(slices.Max(promote))))
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s | %s | %s | %s | %d из %d | %d | %d |\n", pgChaosVariantRu(v), n, fmtNum(req),
			fmtNum(first), fmtNum(retry), fmtNum(gave), fmtNum(failed), why, prom, fmtNum(round1(recMax)), missing, acked, dups, dbl)
	}
	return b.String()
}

// pgChaosChart — неудачные попытки по секундам вокруг падения ведущего узла,
// по варианту с отказом на линию, в среднем на прогон: прогонов у вариантов
// может быть разное число.
func pgChaosChart(runs []chaosRun) string {
	colors := []string{"#eb6834", "#2a78d6", "#1baf7a", "#8a6bd1"}
	var ss []series
	i := 0
	for _, v := range pgChaosVariants(runs) {
		if !strings.Contains(v, "-kill-") {
			continue
		}
		key := "pgchaos-" + v
		seriesStyle[key] = struct{ color, marker string }{colors[i%len(colors)], []string{"circle", "square", "diamond", "circle"}[i%4]}
		strategyRu[key] = pgChaosShortRu(v)
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
		for sec := -3; sec <= 20; sec++ {
			c := round1(by[sec] / n)
			s.points = append(s.points, point{x: strconv.Itoa(sec), stat: stat{Mean: c, Min: c, Max: c}})
		}
		ss = append(ss, s)
	}
	return lineChart(chartSpec{
		title:    "Неудачные попытки заказа вокруг падения ведущего узла PostgreSQL",
		subtitle: "SIGKILL ведущего узла в момент 0, в среднем на прогон. Повторы клиента с тем же ключом.",
		unit:     "попыток", series: ss, xLabel: "секунд от падения ведущего",
	})
}
