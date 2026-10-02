package main

import (
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Отчёт по серии с очередью ожидания (ADR 020): прогоны
// queue__<вариант>__<покупателей>__<повтор>, вариант off — без очереди,
// qN — очередь, пропускающая N покупателей в секунду.

type queueSummary struct {
	k6Summary
	Failed503   float64            `json:"failed_503"`
	Queue       bool               `json:"queue"`
	QueueWaitMS map[string]float64 `json:"queue_wait_ms"`
	QueuePolls  float64            `json:"queue_polls"`
	QueuePollMS map[string]float64 `json:"queue_poll_ms"`
	QueueFailed float64            `json:"queue_failed"`
}

type queueRun struct {
	Variant  string
	VUs, Rep int
	K6       queueSummary
	Check    checkResult
	Server   serverStats
}

func queueReport(args []string) error {
	fs := flag.NewFlagSet("queue-report", flag.ContinueOnError)
	dir := fs.String("dir", "", "каталог серии (loadtest/results/raw/queue)")
	out := fs.String("out", "", "куда положить CSV, таблицы и графики")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *out == "" {
		return errors.New("-dir and -out are required")
	}
	runs, err := loadQueueRuns(*dir)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return errors.New("no runs found")
	}
	if err := os.MkdirAll(filepath.Join(*out, "charts"), 0o750); err != nil {
		return err
	}
	if err := writeQueueCSV(filepath.Join(*out, "results.csv"), runs); err != nil {
		return err
	}
	groups := aggregateQueue(runs)
	if err := os.WriteFile(filepath.Join(*out, "tables.md"), []byte(queueTables(groups)), 0o600); err != nil {
		return err
	}
	for name, svg := range queueCharts(groups) {
		if err := os.WriteFile(filepath.Join(*out, "charts", name), []byte(svg), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func loadQueueRuns(dir string) ([]queueRun, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var runs []queueRun
	for _, e := range entries {
		parts := strings.Split(e.Name(), "__")
		if !e.IsDir() || len(parts) != 4 || parts[0] != "queue" {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(p, "check.json")); err != nil {
			fmt.Fprintln(os.Stderr, "skip unfinished run", e.Name())
			continue
		}
		vus, _ := strconv.Atoi(parts[2])
		rep, _ := strconv.Atoi(parts[3])
		r := queueRun{Variant: parts[1], VUs: vus, Rep: rep}
		if err := readJSON(filepath.Join(p, "summary.json"), &r.K6); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := readJSON(filepath.Join(p, "check.json"), &r.Check); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if r.Server, err = serverDelta(filepath.Join(p, "metrics-before.txt"), filepath.Join(p, "metrics-after.txt"), "redis"); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		runs = append(runs, r)
	}
	slices.SortFunc(runs, func(a, b queueRun) int {
		if c := variantOrder(a.Variant) - variantOrder(b.Variant); c != 0 {
			return c
		}
		return a.Rep - b.Rep
	})
	return runs, nil
}

// variantOrder: без очереди первыми, дальше по скорости пропуска.
func variantOrder(v string) int {
	switch v {
	case "off":
		return -2
	case "offcold":
		return -3
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(v, "q"))
	return n
}

func variantRu(v string) string {
	switch v {
	case "off":
		return "Без очереди"
	case "offcold":
		return "Без очереди, api только запущен"
	}
	return "Очередь, " + strings.TrimPrefix(v, "q") + " в секунду"
}

func writeQueueCSV(path string, runs []queueRun) error {
	f, err := os.Create(path) //nolint:gosec // путь задаёт автор отчёта
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"variant", "vus", "rep", "won", "taken", "failed", "failed_503", "failed_network",
		"order_p50_ms", "order_p95_ms", "order_p99_ms", "sold_out_ms",
		"queue_wait_p50_ms", "queue_wait_p95_ms", "queue_wait_max_ms", "queue_polls", "queue_poll_p95_ms",
		"server_p95_ms", "double_booked"})
	ff := func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
	for _, r := range runs {
		_ = w.Write([]string{r.Variant, strconv.Itoa(r.VUs), strconv.Itoa(r.Rep),
			ff(r.K6.Won), ff(r.K6.Taken), ff(r.K6.Failed), ff(r.K6.Failed503), ff(r.K6.FailedNet),
			ff(r.K6.AttemptMS["med"]), ff(r.K6.AttemptMS["p(95)"]), ff(r.K6.AttemptMS["p(99)"]), ff(r.K6.WonAtMS["max"]),
			ff(r.K6.QueueWaitMS["med"]), ff(r.K6.QueueWaitMS["p(95)"]), ff(r.K6.QueueWaitMS["max"]), ff(r.K6.QueuePolls), ff(r.K6.QueuePollMS["p(95)"]),
			ff(r.Server.P95 * 1000), strconv.Itoa(r.Check.DoubleBooked)})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

type queueGroup struct {
	Variant                        string
	VUs, Runs                      int
	Won, Failed, Failed503         stat
	OrderP50, OrderP95, OrderP99   stat
	SoldOut                        stat // секунды от старта до последней покупки
	WaitP50, WaitP95, WaitMax      stat // секунды
	PollsPerBuyer, PollP95, SrvP95 stat
	DoubleBooked                   int
}

func aggregateQueue(runs []queueRun) []queueGroup {
	var out []queueGroup
	for i := 0; i < len(runs); {
		j := i
		for j < len(runs) && runs[j].Variant == runs[i].Variant && runs[j].VUs == runs[i].VUs {
			j++
		}
		rs := runs[i:j]
		col := func(f func(queueRun) float64) stat {
			var v []float64
			for _, r := range rs {
				v = append(v, f(r))
			}
			return statOf(v)
		}
		g := queueGroup{
			Variant: rs[0].Variant, VUs: rs[0].VUs, Runs: len(rs),
			Won:    col(func(r queueRun) float64 { return r.K6.Won }),
			Failed: col(func(r queueRun) float64 { return r.K6.Failed }),
			Failed503: col(func(r queueRun) float64 {
				return r.K6.Failed503
			}),
			OrderP50: col(func(r queueRun) float64 { return r.K6.AttemptMS["med"] }),
			OrderP95: col(func(r queueRun) float64 { return r.K6.AttemptMS["p(95)"] }),
			OrderP99: col(func(r queueRun) float64 { return r.K6.AttemptMS["p(99)"] }),
			SoldOut:  col(func(r queueRun) float64 { return r.K6.WonAtMS["max"] / 1000 }),
			WaitP50:  col(func(r queueRun) float64 { return r.K6.QueueWaitMS["med"] / 1000 }),
			WaitP95:  col(func(r queueRun) float64 { return r.K6.QueueWaitMS["p(95)"] / 1000 }),
			WaitMax:  col(func(r queueRun) float64 { return r.K6.QueueWaitMS["max"] / 1000 }),
			PollsPerBuyer: col(func(r queueRun) float64 {
				return r.K6.QueuePolls / float64(max(r.VUs, 1))
			}),
			PollP95: col(func(r queueRun) float64 { return r.K6.QueuePollMS["p(95)"] }),
			SrvP95:  col(func(r queueRun) float64 { return r.Server.P95 * 1000 }),
		}
		for _, r := range rs {
			g.DoubleBooked += r.Check.DoubleBooked
		}
		out = append(out, g)
		i = j
	}
	return out
}

func queueTables(groups []queueGroup) string {
	var b strings.Builder
	f0 := func(s stat) string { return fmtNum(s.Mean) }
	b.WriteString("| Вариант | Прогонов | Продано мест | Ошибок | из них 503 | Заказ p50, мс | Заказ p95, мс | Заказ p99, мс | Захват на сервере p95, мс | Зал продан за, с | Двойных броней |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, g := range groups {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s | %s | %s | %s | %d |\n", variantRu(g.Variant), g.Runs, f0(g.Won), f0(g.Failed), f0(g.Failed503),
			f0(g.OrderP50), f0(g.OrderP95), f0(g.OrderP99), f0(g.SrvP95), fmtNum(round1(g.SoldOut.Mean)), g.DoubleBooked)
	}
	b.WriteString("\n| Вариант | Ожидание в очереди p50, с | p95, с | максимум, с | Опросов на покупателя | Опрос p95, мс |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|\n")
	for _, g := range groups {
		if !strings.HasPrefix(g.Variant, "q") {
			continue
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", variantRu(g.Variant), fmtNum(round1(g.WaitP50.Mean)), fmtNum(round1(g.WaitP95.Mean)),
			fmtNum(round1(g.WaitMax.Mean)), fmtNum(round1(g.PollsPerBuyer.Mean)), f0(g.PollP95))
	}
	fmt.Fprintf(&b, "\nСреднее по прогонам варианта, %d покупателей, зал на 1000 мест.\n", groups[0].VUs)
	return b.String()
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// Цвета вариантов — из той же палитры, что графики стратегий (svg.go).
var variantColors = map[string]string{"off": "#eb6834", "q100": "#2a78d6", "q200": "#1baf7a"}

func variantColor(v string) string {
	if c, ok := variantColors[v]; ok {
		return c
	}
	return "#8a6bd1"
}

type bar struct {
	label string
	stat  stat
	color string
}

func queueCharts(groups []queueGroup) map[string]string {
	// Холодный старт в графики не идёт: его «быстрые» ответы — это 503 у
	// почти всех покупателей. Он разобран в таблице и в тексте отчёта.
	pick := func(f func(queueGroup) stat) []bar {
		var out []bar
		for _, g := range groups {
			if g.Variant == "offcold" {
				continue
			}
			out = append(out, bar{label: variantRu(g.Variant), stat: f(g), color: variantColor(g.Variant)})
		}
		return out
	}
	return map[string]string{
		"order-p95.svg": barChart("Время ответа на заказ, p95",
			"Штурм зала на 1000 мест в момент старта продаж. Столбец — среднее трёх прогонов, ус — разброс.", "мс",
			pick(func(g queueGroup) stat { return g.OrderP95 })),
		"sold-out.svg": barChart("За сколько продан зал",
			"От старта продаж до последней покупки. С очередью дольше: покупателей пускают с постоянной скоростью.", "с",
			pick(func(g queueGroup) stat { return g.SoldOut })),
	}
}

// barChart — горизонтальные столбцы с подписью значения у конца.
func barChart(title, subtitle, unit string, bars []bar) string {
	const w, left, right, top, rowH, barH = 800.0, 230.0, 90.0, 104.0, 52.0, 26.0
	h := top + rowH*float64(len(bars)) + 40
	pw := w - left - right
	var maxV float64
	for _, b := range bars {
		maxV = max(maxV, b.stat.Max)
	}
	xMax, step := niceScale(maxV * 1.02)
	xAt := func(v float64) float64 { return left + v/xMax*pw }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" font-family="system-ui,-apple-system,'Segoe UI',sans-serif" role="img" aria-label="%s">`, w, h, w, h, html.EscapeString(title))
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" fill="%s"/>`, surface)
	fmt.Fprintf(&b, `<text x="24" y="34" font-size="18" font-weight="600" fill="%s">%s</text>`, textMain, html.EscapeString(title))
	fmt.Fprintf(&b, `<text x="24" y="56" font-size="12.5" fill="%s">%s</text>`, textMuted, html.EscapeString(subtitle))
	bottom := top + rowH*float64(len(bars))
	for v := 0.0; v <= xMax+step/2; v += step {
		x := xAt(v)
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.0f" x2="%.1f" y2="%.0f" stroke="%s" stroke-width="1"/>`, x, top-8, x, bottom, gridColor)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" font-size="11.5" fill="%s" text-anchor="middle">%s</text>`, x, bottom+18, textMuted, fmtNum(v))
	}
	fmt.Fprintf(&b, `<text x="%.0f" y="%.0f" font-size="11.5" fill="%s">%s</text>`, left, top-20, textMuted, html.EscapeString(unit))
	for i, br := range bars {
		y := top + float64(i)*rowH + (rowH-barH)/2
		fmt.Fprintf(&b, `<text x="%.0f" y="%.1f" font-size="13" fill="%s" text-anchor="end">%s</text>`, left-12, y+barH/2+4.5, textMain, html.EscapeString(br.label))
		fmt.Fprintf(&b, `<rect x="%.0f" y="%.1f" width="%.1f" height="%.0f" rx="3" fill="%s"/>`, left, y, max(xAt(br.stat.Mean)-left, 1), barH, br.color)
		if br.stat.Max > br.stat.Min {
			fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1.5"/>`, xAt(br.stat.Min), y+barH/2, xAt(br.stat.Max), y+barH/2, textMain)
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="12.5" font-weight="600" fill="%s">%s</text>`, max(xAt(br.stat.Mean), xAt(br.stat.Max))+8, y+barH/2+4.5, textMain, fmtNum(round1(br.stat.Mean)))
	}
	b.WriteString(`</svg>`)
	return b.String()
}
