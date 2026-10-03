package main

import (
	"cmp"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Отчёт по серии масштабирования (ADR 023): прогоны
// scale__api<экземпляров>__<поток в секунду>__<повтор> сценария
// loadtest/capacity.js. Ёмкость конфигурации — самый большой поток, при
// котором p95 ниже SLO, ошибок меньше 1% и k6 успевал выпускать запросы.

type capacitySummary struct {
	Rate        float64            `json:"rate"`
	Requests    float64            `json:"requests"`
	AchievedRPS float64            `json:"achieved_rps"`
	Created     float64            `json:"created"`
	Taken       float64            `json:"taken"`
	Errors      float64            `json:"errors"`
	Dropped     float64            `json:"dropped"`
	OrderMS     map[string]float64 `json:"order_ms"`
}

type scaleRun struct {
	Instances, Rate int
	K6              capacitySummary
}

// sloP95MS — порог p95 для ёмкости: покупатель не должен ждать заказа дольше
// полсекунды.
const sloP95MS = 500

func (r scaleRun) errorShare() float64 {
	if r.K6.Requests == 0 {
		return 0
	}
	return r.K6.Errors / r.K6.Requests
}

func (r scaleRun) withinSLO() bool {
	return r.K6.OrderMS["p(95)"] < sloP95MS && r.errorShare() < 0.01 && r.K6.Dropped == 0
}

func scaleReport(args []string) error {
	fs := flag.NewFlagSet("scale-report", flag.ContinueOnError)
	dir := fs.String("dir", "", "каталог серии (loadtest/results/raw/scale)")
	out := fs.String("out", "", "куда положить CSV, таблицы и графики")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *out == "" {
		return errors.New("-dir and -out are required")
	}
	entries, err := os.ReadDir(*dir)
	if err != nil {
		return err
	}
	var runs []scaleRun
	for _, e := range entries {
		parts := strings.Split(e.Name(), "__")
		if !e.IsDir() || len(parts) != 4 || parts[0] != "scale" {
			continue
		}
		n, _ := strconv.Atoi(strings.TrimPrefix(parts[1], "api"))
		rate, _ := strconv.Atoi(parts[2])
		r := scaleRun{Instances: n, Rate: rate}
		if err := readJSON(filepath.Join(*dir, e.Name(), "summary.json"), &r.K6); err != nil {
			fmt.Fprintln(os.Stderr, "skip", e.Name(), err)
			continue
		}
		runs = append(runs, r)
	}
	if len(runs) == 0 {
		return errors.New("no runs found")
	}
	slices.SortFunc(runs, func(a, b scaleRun) int {
		return cmp.Or(cmp.Compare(a.Instances, b.Instances), cmp.Compare(a.Rate, b.Rate))
	})
	if err := os.MkdirAll(filepath.Join(*out, "charts"), 0o750); err != nil {
		return err
	}
	if err := writeScaleCSV(filepath.Join(*out, "results.csv"), runs); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "tables.md"), []byte(scaleTables(runs)), 0o600); err != nil {
		return err
	}
	for name, svg := range scaleCharts(runs) {
		if err := os.WriteFile(filepath.Join(*out, "charts", name), []byte(svg), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func writeScaleCSV(path string, runs []scaleRun) error {
	f, err := os.Create(path) //nolint:gosec // путь задаёт автор отчёта
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"instances", "rate", "achieved_rps", "requests", "created", "taken", "errors", "dropped", "p50_ms", "p95_ms", "p99_ms", "within_slo"})
	ff := func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
	for _, r := range runs {
		_ = w.Write([]string{strconv.Itoa(r.Instances), strconv.Itoa(r.Rate), ff(r.K6.AchievedRPS), ff(r.K6.Requests),
			ff(r.K6.Created), ff(r.K6.Taken), ff(r.K6.Errors), ff(r.K6.Dropped),
			ff(r.K6.OrderMS["med"]), ff(r.K6.OrderMS["p(95)"]), ff(r.K6.OrderMS["p(99)"]), strconv.FormatBool(r.withinSLO())})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// capacityOf — самый большой поток в пределах SLO для числа экземпляров n.
func capacityOf(runs []scaleRun, n int) int {
	best := 0
	for _, r := range runs {
		if r.Instances == n && r.withinSLO() && r.Rate > best {
			best = r.Rate
		}
	}
	return best
}

func instancesRu(n int) string {
	return fmt.Sprintf("%d %s", n, pluralRu(n, "экземпляр", "экземпляра", "экземпляров"))
}

func pluralRu(n int, one, few, many string) string {
	switch {
	case n%10 == 1 && n%100 != 11:
		return one
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		return few
	default:
		return many
	}
}

func levels(runs []scaleRun) []int {
	var ns []int
	for _, r := range runs {
		if !slices.Contains(ns, r.Instances) {
			ns = append(ns, r.Instances)
		}
	}
	return ns
}

func scaleTables(runs []scaleRun) string {
	var b strings.Builder
	b.WriteString("<!-- Сгенерировано: go run ./cmd/loadseed scale-report. Не править руками. -->\n\n")
	fmt.Fprintf(&b, "Ёмкость — самый большой поток заказов, при котором p95 < %d мс, ошибок < 1%% и k6 успевал выпускать запросы.\n\n", sloP95MS)
	b.WriteString("| Экземпляров api | Ёмкость, заказов в секунду |\n|---|---:|\n")
	for _, n := range levels(runs) {
		c := capacityOf(runs, n)
		cs := fmtNum(float64(c))
		if c == 0 {
			cs = "меньше минимальной ступени"
		}
		fmt.Fprintf(&b, "| %d | %s |\n", n, cs)
	}
	b.WriteString("\n| Экземпляров | Поток, задано | Поток, получено | p50, мс | p95, мс | p99, мс | Ошибок | Не выпущено k6 | В пределах SLO |\n")
	b.WriteString("|---:|---:|---:|---:|---:|---:|---:|---:|---|\n")
	for _, r := range runs {
		ok := "нет"
		if r.withinSLO() {
			ok = "да"
		}
		fmt.Fprintf(&b, "| %d | %d | %s | %s | %s | %s | %s (%.1f%%) | %s | %s |\n", r.Instances, r.Rate, fmtNum(round1(r.K6.AchievedRPS)),
			fmtNum(round1(r.K6.OrderMS["med"])), fmtNum(round1(r.K6.OrderMS["p(95)"])), fmtNum(round1(r.K6.OrderMS["p(99)"])),
			fmtNum(r.K6.Errors), r.errorShare()*100, fmtNum(r.K6.Dropped), ok)
	}
	return b.String()
}

func scaleCharts(runs []scaleRun) map[string]string {
	var ss []series
	var caps []bar
	colors := []string{"#2a78d6", "#eb6834", "#1baf7a", "#8a6bd1"}
	for i, n := range levels(runs) {
		key := "api" + strconv.Itoa(n)
		seriesStyle[key] = struct{ color, marker string }{colors[i%len(colors)], []string{"circle", "square", "diamond", "circle"}[i%4]}
		strategyRu[key] = instancesRu(n)
		s := series{strategy: key}
		for _, r := range runs {
			if r.Instances == n {
				v := r.K6.OrderMS["p(95)"]
				s.points = append(s.points, point{x: strconv.Itoa(r.Rate), stat: stat{Mean: v, Min: v, Max: v}})
			}
		}
		ss = append(ss, s)
		c := float64(capacityOf(runs, n))
		caps = append(caps, bar{label: instancesRu(n), stat: stat{Mean: c, Min: c, Max: c}, color: colors[i%len(colors)]})
	}
	return map[string]string{
		"capacity.svg": barChart("Ёмкость: заказов в секунду в пределах SLO",
			fmt.Sprintf("Самый большой поток, при котором p95 < %d мс и ошибок < 1%%. У каждого экземпляра — пол-ядра.", sloP95MS), "заказов в секунду", caps),
		"p95-by-rate.svg": lineChart(chartSpec{
			title: "Время ответа p95 при постоянном потоке заказов", subtitle: "Ступени по 15 секунд. Ось Y обрезана сверху: за SLO линии уходят в секунды.",
			unit: "мс", series: ss, xLabel: "заказов в секунду (задано)",
		}),
	}
}
