package main

import (
	"cmp"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// Отчёт по серии прогонов: CSV по каждому прогону, таблицы средних по трём
// повторам и SVG-графики для пояснительной записки.

type k6Summary struct {
	Scenario  string             `json:"scenario"`
	VUs       int                `json:"vus"`
	Won       float64            `json:"won"`
	Taken     float64            `json:"taken"`
	Failed    float64            `json:"failed"`
	Attempts  float64            `json:"attempts"`
	AttemptMS map[string]float64 `json:"attempt_ms"`
	WonAtMS   map[string]float64 `json:"won_at_ms"`
}

type checkResult struct {
	DoubleBooked int `json:"double_booked"`
	Mismatched   int `json:"mismatched"`
	OrderItems   int `json:"order_items"`
	HeldOrSold   int `json:"seats_held_or_sold"`
}

// serverStats — приращения метрик api за прогон.
type serverStats struct {
	Attempts, OK, TakenRedis, TakenDB, Retries float64
	P50, P95, P99                              float64 // секунды, время захвата на сервере
}

type runResult struct {
	Scenario, Strategy string
	VUs, Rep           int
	K6                 k6Summary
	Check              checkResult
	Server             serverStats
}

func report(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	dir := fs.String("dir", "", "каталог серии (loadtest/results/raw/…)")
	out := fs.String("out", "", "куда положить CSV, таблицы и графики")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *out == "" {
		return errors.New("-dir and -out are required")
	}
	runs, err := loadRuns(*dir)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return errors.New("no runs found")
	}
	if err := os.MkdirAll(filepath.Join(*out, "charts"), 0o750); err != nil {
		return err
	}
	if err := writeCSV(filepath.Join(*out, "results.csv"), runs); err != nil {
		return err
	}
	groups := aggregate(runs)
	if err := os.WriteFile(filepath.Join(*out, "tables.md"), []byte(tables(groups)), 0o600); err != nil {
		return err
	}
	for name, svg := range charts(groups) {
		if err := os.WriteFile(filepath.Join(*out, "charts", name), []byte(svg), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func loadRuns(dir string) ([]runResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var runs []runResult
	for _, e := range entries {
		parts := strings.Split(e.Name(), "__")
		if !e.IsDir() || len(parts) != 4 {
			continue
		}
		vus, _ := strconv.Atoi(parts[2])
		rep, _ := strconv.Atoi(parts[3])
		r := runResult{Scenario: parts[0], Strategy: parts[1], VUs: vus, Rep: rep}
		p := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(p, "check.json")); err != nil {
			fmt.Fprintln(os.Stderr, "skip unfinished run", e.Name())
			continue
		}
		if err := readJSON(filepath.Join(p, "summary.json"), &r.K6); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := readJSON(filepath.Join(p, "check.json"), &r.Check); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if r.Server, err = serverDelta(filepath.Join(p, "metrics-before.txt"), filepath.Join(p, "metrics-after.txt"), r.Strategy); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		runs = append(runs, r)
	}
	return runs, nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path) //nolint:gosec // путь из каталога серии
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func parseMetrics(path string) (map[string]*dto.MetricFamily, error) {
	f, err := os.Open(path) //nolint:gosec // путь из каталога серии
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	p := expfmt.NewTextParser(model.UTF8Validation)
	return p.TextToMetricFamilies(f)
}

func label(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

// serverDelta считает приращения счётчиков и квантили времени захвата по
// разнице гистограмм до и после прогона — так же, как histogram_quantile.
func serverDelta(beforePath, afterPath, strategy string) (serverStats, error) {
	before, err := parseMetrics(beforePath)
	if err != nil {
		return serverStats{}, err
	}
	after, err := parseMetrics(afterPath)
	if err != nil {
		return serverStats{}, err
	}
	sum := func(fams map[string]*dto.MetricFamily, name string, match func(*dto.Metric) bool) float64 {
		var v float64
		for _, m := range fams[name].GetMetric() {
			if label(m, "strategy") == strategy && match(m) {
				v += m.GetCounter().GetValue()
			}
		}
		return v
	}
	delta := func(name string, match func(*dto.Metric) bool) float64 {
		return sum(after, name, match) - sum(before, name, match)
	}
	all := func(*dto.Metric) bool { return true }
	s := serverStats{
		Attempts:   delta("dd_booking_attempts_total", all),
		OK:         delta("dd_booking_attempts_total", func(m *dto.Metric) bool { return label(m, "result") == "ok" }),
		TakenRedis: delta("dd_booking_attempts_total", func(m *dto.Metric) bool { return label(m, "reject_by") == "redis" }),
		TakenDB:    delta("dd_booking_attempts_total", func(m *dto.Metric) bool { return label(m, "reject_by") == "db" }),
		Retries:    delta("dd_booking_retries_total", all),
	}
	buckets := func(fams map[string]*dto.MetricFamily) (map[float64]float64, float64) {
		b := map[float64]float64{}
		var total float64
		for _, m := range fams["dd_booking_attempt_seconds"].GetMetric() {
			if label(m, "strategy") != strategy {
				continue
			}
			h := m.GetHistogram()
			total += float64(h.GetSampleCount())
			for _, bk := range h.GetBucket() {
				b[bk.GetUpperBound()] += float64(bk.GetCumulativeCount())
			}
		}
		return b, total
	}
	ba, ta := buckets(after)
	bb, tb := buckets(before)
	for ub := range ba {
		ba[ub] -= bb[ub]
	}
	n := ta - tb
	s.P50, s.P95, s.P99 = quantile(ba, n, 0.5), quantile(ba, n, 0.95), quantile(ba, n, 0.99)
	return s, nil
}

// quantile — линейная интерполяция внутри корзины, как в Prometheus.
func quantile(cum map[float64]float64, total, q float64) float64 {
	if total == 0 {
		return 0
	}
	bounds := make([]float64, 0, len(cum))
	for ub := range cum {
		bounds = append(bounds, ub)
	}
	slices.Sort(bounds)
	rank := q * total
	prevBound, prevCount := 0.0, 0.0
	for _, ub := range bounds {
		c := cum[ub]
		if c >= rank {
			if math.IsInf(ub, 1) {
				return prevBound
			}
			if c == prevCount {
				return ub
			}
			return prevBound + (ub-prevBound)*(rank-prevCount)/(c-prevCount)
		}
		prevBound, prevCount = ub, c
	}
	return prevBound
}

// throughput — успешных захватов в секунду: число побед на время от старта
// до последней победы.
func (r runResult) throughput() float64 {
	last := r.K6.WonAtMS["max"]
	if last <= 0 {
		return 0
	}
	return r.K6.Won / (last / 1000)
}

func writeCSV(path string, runs []runResult) error {
	f, err := os.Create(path) //nolint:gosec // путь задаёт запускающий
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"scenario", "strategy", "vus", "rep", "won", "taken", "failed", "attempts",
		"e2e_p50_ms", "e2e_p95_ms", "e2e_p99_ms", "e2e_max_ms", "server_p50_ms", "server_p95_ms", "server_p99_ms",
		"throughput_per_s", "rejected_by_redis", "rejected_by_db", "retries", "double_booked", "mismatched"})
	slices.SortFunc(runs, func(a, b runResult) int {
		return cmp.Or(cmp.Compare(a.Scenario, b.Scenario), cmp.Compare(strategyOrder(a.Strategy), strategyOrder(b.Strategy)), cmp.Compare(a.VUs, b.VUs), cmp.Compare(a.Rep, b.Rep))
	})
	f2 := func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
	for _, r := range runs {
		_ = w.Write([]string{r.Scenario, r.Strategy, strconv.Itoa(r.VUs), strconv.Itoa(r.Rep),
			f2(r.K6.Won), f2(r.K6.Taken), f2(r.K6.Failed), f2(r.K6.Attempts),
			f2(r.K6.AttemptMS["med"]), f2(r.K6.AttemptMS["p(95)"]), f2(r.K6.AttemptMS["p(99)"]), f2(r.K6.AttemptMS["max"]),
			f2(r.Server.P50 * 1000), f2(r.Server.P95 * 1000), f2(r.Server.P99 * 1000),
			f2(r.throughput()), f2(r.Server.TakenRedis), f2(r.Server.TakenDB), f2(r.Server.Retries),
			strconv.Itoa(r.Check.DoubleBooked), strconv.Itoa(r.Check.Mismatched)})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

var strategyNames = []string{"pessimistic", "optimistic", "redis"}

func strategyOrder(s string) int { return slices.Index(strategyNames, s) }

// stat — среднее и разброс по повторам.
type stat struct{ Mean, Min, Max float64 }

func statOf(vals []float64) stat {
	if len(vals) == 0 {
		return stat{}
	}
	s := stat{Min: math.Inf(1), Max: math.Inf(-1)}
	for _, v := range vals {
		s.Mean += v
		s.Min = min(s.Min, v)
		s.Max = max(s.Max, v)
	}
	s.Mean /= float64(len(vals))
	return s
}

type group struct {
	Scenario, Strategy string
	VUs, Runs          int
	E2EP50, E2EP95     stat
	E2EP99             stat
	SrvP95, SrvP99     stat
	Throughput         stat
	Won, Failed        stat
	RedisShare         stat // доля отказов, отсечённых Redis
	RetriesPerAttempt  stat
	DoubleBooked       int
	Mismatched         int
}

func aggregate(runs []runResult) []group {
	type key struct {
		sc, st string
		n      int
	}
	byKey := map[key][]runResult{}
	for _, r := range runs {
		k := key{r.Scenario, r.Strategy, r.VUs}
		byKey[k] = append(byKey[k], r)
	}
	var out []group
	for k, rs := range byKey {
		g := group{Scenario: k.sc, Strategy: k.st, VUs: k.n, Runs: len(rs)}
		pick := func(f func(runResult) float64) stat {
			v := make([]float64, len(rs))
			for i, r := range rs {
				v[i] = f(r)
			}
			return statOf(v)
		}
		g.E2EP50 = pick(func(r runResult) float64 { return r.K6.AttemptMS["med"] })
		g.E2EP95 = pick(func(r runResult) float64 { return r.K6.AttemptMS["p(95)"] })
		g.E2EP99 = pick(func(r runResult) float64 { return r.K6.AttemptMS["p(99)"] })
		g.SrvP95 = pick(func(r runResult) float64 { return r.Server.P95 * 1000 })
		g.SrvP99 = pick(func(r runResult) float64 { return r.Server.P99 * 1000 })
		g.Throughput = pick(runResult.throughput)
		g.Won = pick(func(r runResult) float64 { return r.K6.Won })
		g.Failed = pick(func(r runResult) float64 { return r.K6.Failed })
		g.RedisShare = pick(func(r runResult) float64 {
			if t := r.Server.TakenRedis + r.Server.TakenDB; t > 0 {
				return r.Server.TakenRedis / t
			}
			return 0
		})
		g.RetriesPerAttempt = pick(func(r runResult) float64 {
			if r.Server.Attempts > 0 {
				return r.Server.Retries / r.Server.Attempts
			}
			return 0
		})
		for _, r := range rs {
			g.DoubleBooked += r.Check.DoubleBooked
			g.Mismatched += r.Check.Mismatched
		}
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b group) int {
		return cmp.Or(cmp.Compare(a.Scenario, b.Scenario), cmp.Compare(strategyOrder(a.Strategy), strategyOrder(b.Strategy)), cmp.Compare(a.VUs, b.VUs))
	})
	return out
}

var strategyRu = map[string]string{"pessimistic": "Пессимистичная", "optimistic": "Оптимистичная", "redis": "Redis + БД"}

func tables(groups []group) string {
	var b strings.Builder
	ms := func(s stat) string { return fmt.Sprintf("%.0f", s.Mean) }
	b.WriteString("<!-- Сгенерировано: go run ./cmd/loadseed report. Не править руками. -->\n\n")
	b.WriteString("### Одно место (one-seat)\n\n")
	b.WriteString("| Стратегия | Покупателей | Прогонов | Победителей | Двойных броней | p50, мс | p95, мс | p99, мс | Захват на сервере p95, мс | Отсечено Redis | Повторов на попытку |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, g := range groups {
		if g.Scenario != "one-seat" {
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %.0f | %d | %s | %s | %s | %s | %.0f%% | %.2f |\n",
			strategyRu[g.Strategy], g.VUs, g.Runs, g.Won.Mean, g.DoubleBooked+g.Mismatched, ms(g.E2EP50), ms(g.E2EP95), ms(g.E2EP99), ms(g.SrvP95), g.RedisShare.Mean*100, g.RetriesPerAttempt.Mean)
	}
	b.WriteString("\n### Зал на 1000 мест (hall)\n\n")
	b.WriteString("| Стратегия | Покупателей | Прогонов | Продано мест | Захватов в секунду | Двойных броней | Ошибок | p95, мс | p99, мс | Захват на сервере p95, мс | Отсечено Redis | Повторов на попытку |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, g := range groups {
		if g.Scenario != "hall" {
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %.0f | %.0f | %d | %.0f | %s | %s | %s | %.0f%% | %.2f |\n",
			strategyRu[g.Strategy], g.VUs, g.Runs, g.Won.Mean, g.Throughput.Mean, g.DoubleBooked+g.Mismatched, g.Failed.Mean, ms(g.E2EP95), ms(g.E2EP99), ms(g.SrvP95), g.RedisShare.Mean*100, g.RetriesPerAttempt.Mean)
	}
	return b.String()
}
