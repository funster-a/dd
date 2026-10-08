package main

import (
	"bufio"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Отчёт по отказу экземпляра api (ADR 027): прогоны
// chaos__<вариант>__<поток>__<повтор> сценария loadtest/chaos.js в одной или
// нескольких сериях («до» и «после» исправления). Вариант none — без отказа,
// kill — SIGKILL, stop — SIGTERM.

type chaosSummary struct {
	Requests   float64            `json:"requests"`
	OKFirst    float64            `json:"ok_first"`
	OKRetry    float64            `json:"ok_retry"`
	Taken      float64            `json:"taken"`
	GaveUp     float64            `json:"gave_up"`
	Retries    float64            `json:"retries"`
	Attempts   float64            `json:"attempts"`
	Dropped    float64            `json:"dropped"`
	RecoveryMS map[string]float64 `json:"recovery_ms"`
	OrderMS    map[string]float64 `json:"order_ms"`
}

type chaosRun struct {
	Series, Variant string
	Rep             int
	K6              chaosSummary
	Check           checkResult
	Dups            struct {
		Orders int `json:"orders"`
		Dups   int `json:"buyers_with_two_orders"`
		// Подтверждённые клиенту заказы и те из них, которых нет в базе
		// (переключение PostgreSQL, ADR 028).
		Acked        int `json:"acked"`
		AckedMissing int `json:"acked_missing"`
	}
	// Холды Redis против базы после прогона (ADR 029); нули, если сверки не было.
	Holds struct {
		RedisHolds int `json:"redis_holds"`
		DBHeld     int `json:"db_held"`
		GhostFree  int `json:"ghost_free"`
		GhostOther int `json:"ghost_other"`
		Missing    int `json:"missing"`
	}
	// Секунд от отказа до следующего события в events.txt (повышение
	// реплики при переключении PostgreSQL); NaN, если события нет.
	Promote float64
	// Неудачные попытки (не «создан» и не «место заняли») по секундам от
	// отказа экземпляра и их причины.
	FailedBySec map[int]float64
	FailReasons map[string]float64
}

func chaosReport(args []string) error {
	fs := flag.NewFlagSet("chaos-report", flag.ContinueOnError)
	series := fs.String("series", "", "серии через запятую: метка=каталог,метка=каталог")
	out := fs.String("out", "", "куда положить CSV, таблицы и графики")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *series == "" || *out == "" {
		return errors.New("-series and -out are required")
	}
	var runs []chaosRun
	var labels []string
	for _, part := range strings.Split(*series, ",") {
		label, dir, ok := strings.Cut(part, "=")
		if !ok {
			return fmt.Errorf("bad series %q: want label=dir", part)
		}
		labels = append(labels, label)
		rs, err := loadChaosRuns(label, dir)
		if err != nil {
			return err
		}
		runs = append(runs, rs...)
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
	if err := os.WriteFile(filepath.Join(*out, "tables.md"), []byte(chaosTables(labels, runs)), 0o600); err != nil {
		return err
	}
	for name, svg := range chaosCharts(labels, runs) {
		if err := os.WriteFile(filepath.Join(*out, "charts", name), []byte(svg), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func loadChaosRuns(label, dir string) ([]chaosRun, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var runs []chaosRun
	for _, e := range entries {
		parts := strings.Split(e.Name(), "__")
		if !e.IsDir() || len(parts) != 4 || parts[0] != "chaos" {
			continue
		}
		rep, _ := strconv.Atoi(parts[3])
		r := chaosRun{Series: label, Variant: parts[1], Rep: rep}
		p := filepath.Join(dir, e.Name())
		if err := readJSON(filepath.Join(p, "summary.json"), &r.K6); err != nil {
			fmt.Fprintln(os.Stderr, "skip", p, err)
			continue
		}
		if err := readJSON(filepath.Join(p, "check.json"), &r.Check); err != nil {
			return nil, err
		}
		if err := readJSON(filepath.Join(p, "dups.json"), &r.Dups); err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(p, "holds.json")); err == nil {
			if err := readJSON(filepath.Join(p, "holds.json"), &r.Holds); err != nil {
				return nil, err
			}
		}
		if err := r.loadPoints(p); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		runs = append(runs, r)
	}
	slices.SortFunc(runs, func(a, b chaosRun) int {
		if c := strings.Compare(chaosVariantKey(a.Variant), chaosVariantKey(b.Variant)); c != 0 {
			return c
		}
		return a.Rep - b.Rep
	})
	return runs, nil
}

func chaosVariantKey(v string) string {
	return map[string]string{"none": "0", "kill": "1", "stop": "2"}[v] + v
}

func chaosVariantRu(v string) string {
	switch v {
	case "none":
		return "Без отказа"
	case "kill":
		return "Падение экземпляра (SIGKILL)"
	case "stop":
		return "Остановка экземпляра (SIGTERM)"
	}
	return v
}

// loadPoints читает точки k6 (CSV) и момент отказа (events.txt) и считает
// неудачные попытки по секундам относительно отказа.
func (r *chaosRun) loadPoints(dir string) error {
	r.FailedBySec = map[int]float64{}
	r.FailReasons = map[string]float64{}
	killAt := math.NaN()
	r.Promote = math.NaN()
	if f, err := os.Open(filepath.Join(dir, "events.txt")); err == nil { //nolint:gosec // путь задаёт автор отчёта
		sc := bufio.NewScanner(f)
		if sc.Scan() {
			if ts, _, ok := strings.Cut(sc.Text(), " "); ok {
				killAt, _ = strconv.ParseFloat(ts, 64)
			}
		}
		if sc.Scan() {
			if ts, what, ok := strings.Cut(sc.Text(), " "); ok && strings.HasPrefix(what, "promoted") {
				at, _ := strconv.ParseFloat(ts, 64)
				r.Promote = at - killAt
			}
		}
		_ = f.Close()
	}
	f, err := os.Open(filepath.Join(dir, "points.csv")) //nolint:gosec // путь задаёт автор отчёта
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	cr := csv.NewReader(f)
	head, err := cr.Read()
	if err != nil {
		return err
	}
	col := func(name string) int { return slices.Index(head, name) }
	iName, iTS, iTags := col("metric_name"), col("timestamp"), col("extra_tags")
	var first float64
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		ts, _ := strconv.ParseFloat(rec[iTS], 64)
		if first == 0 || ts < first {
			first = ts
		}
		if rec[iName] != "chaos_attempt" {
			continue
		}
		outcome := ""
		for _, kv := range strings.Split(rec[iTags], "&") {
			if v, ok := strings.CutPrefix(kv, "outcome="); ok {
				outcome = v
			}
		}
		if outcome == "created" || outcome == "seat_taken" {
			continue
		}
		r.FailReasons[outcome]++
		ref := killAt
		if math.IsNaN(ref) {
			ref = first + 20 // без отказа — та же шкала, что у прогонов с отказом
		}
		r.FailedBySec[int(math.Floor(ts-ref))]++
	}
	return nil
}

func writeChaosCSV(path string, runs []chaosRun) error {
	f, err := os.Create(path) //nolint:gosec // путь задаёт автор отчёта
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"series", "variant", "rep", "requests", "ok_first", "ok_retry", "taken", "gave_up", "retries", "dropped",
		"failed_attempts", "fail_reasons", "recovery_p95_ms", "recovery_max_ms", "order_p95_ms", "orders", "buyers_with_two_orders", "double_booked"})
	ff := func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
	for _, r := range runs {
		var reasons []string
		var failed float64
		for k, v := range r.FailReasons {
			reasons = append(reasons, fmt.Sprintf("%s:%.0f", k, v))
			failed += v
		}
		slices.Sort(reasons)
		k := r.K6
		_ = w.Write([]string{r.Series, r.Variant, strconv.Itoa(r.Rep), ff(k.Requests), ff(k.OKFirst), ff(k.OKRetry), ff(k.Taken), ff(k.GaveUp),
			ff(k.Retries), ff(k.Dropped), ff(failed), strings.Join(reasons, " "), ff(k.RecoveryMS["p(95)"]), ff(k.RecoveryMS["max"]),
			ff(k.OrderMS["p(95)"]), strconv.Itoa(r.Dups.Orders), strconv.Itoa(r.Dups.Dups), strconv.Itoa(r.Check.DoubleBooked + r.Check.Mismatched)})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func chaosTables(labels []string, runs []chaosRun) string {
	var b strings.Builder
	b.WriteString("<!-- Сгенерировано: go run ./cmd/loadseed chaos-report. Не править руками. -->\n\n")
	b.WriteString("Сумма по прогонам серии. «Ушли без билета» — запрос не удался и после повторов с тем же ключом.\n\n")
	b.WriteString("| Серия | Вариант | Прогонов | Запросов | Сразу | После повторов | Ушли без билета | Неудачных попыток | Причины | Восстановление max, с | Двойных выполнений | Двойных броней |\n")
	b.WriteString("|---|---|---:|---:|---:|---:|---:|---:|---|---:|---:|---:|\n")
	for _, label := range labels {
		for _, v := range []string{"none", "kill", "stop"} {
			var n int
			var req, first, retry, gave, failed, recMax float64
			var dups, dbl int
			reasons := map[string]float64{}
			for _, r := range runs {
				if r.Series != label || r.Variant != v {
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
				for k, c := range r.FailReasons {
					reasons[k] += c
					failed += c
				}
			}
			if n == 0 {
				continue
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
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %s | %s | %s | %s | %s | %s | %d | %d |\n", label, chaosVariantRu(v), n, fmtNum(req),
				fmtNum(first), fmtNum(retry), fmtNum(gave), fmtNum(failed), why, fmtNum(recMax), dups, dbl)
		}
	}
	return b.String()
}

func reasonRu(k string) string {
	switch k {
	case "network":
		return "обрыв"
	case "request_in_progress":
		return "«запрос ещё выполняется»"
	case "http_500", "http_502", "http_503", "http_504":
		return strings.TrimPrefix(k, "http_")
	}
	return k
}

// chaosCharts — неудачные попытки по секундам вокруг падения экземпляра:
// серии «до» и «после» на одном графике, сумма по прогонам.
func chaosCharts(labels []string, runs []chaosRun) map[string]string {
	colors := []string{"#eb6834", "#2a78d6", "#1baf7a", "#8a6bd1"}
	var ss []series
	for i, label := range labels {
		key := "chaos-" + label
		seriesStyle[key] = struct{ color, marker string }{colors[i%len(colors)], []string{"circle", "square", "diamond", "circle"}[i%4]}
		strategyRu[key] = label
		by := map[int]float64{}
		for _, r := range runs {
			if r.Series == label && r.Variant == "kill" {
				for s, c := range r.FailedBySec {
					by[s] += c
				}
			}
		}
		s := series{strategy: key}
		for sec := -5; sec <= 30; sec++ {
			v := by[sec]
			s.points = append(s.points, point{x: strconv.Itoa(sec), stat: stat{Mean: v, Min: v, Max: v}})
		}
		ss = append(ss, s)
	}
	return map[string]string{
		"failed-attempts.svg": lineChart(chartSpec{
			title:    "Неудачные попытки заказа вокруг падения экземпляра",
			subtitle: "SIGKILL одного из двух экземпляров в момент 0, сумма по прогонам. Повторы клиента с тем же ключом.",
			unit:     "попыток", series: ss, xLabel: "секунд от падения экземпляра",
		}),
	}
}
