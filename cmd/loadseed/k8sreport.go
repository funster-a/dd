package main

import (
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Графики эксперимента с автомасштабированием (ADR 032) из сохранённых данных
// workflow «Kubernetes storm»: <in>/<вариант>/hpa.csv и summary.json. Вариант —
// каталог rate<скорость очереди>.

// k8sRun — один прогон: отсчёты HPA и конец штурма.
type k8sRun struct {
	label   string
	color   string
	rows    []k8sRow
	lastWin float64 // секунда последней покупки от старта продаж
}

type k8sRow struct {
	t                   float64
	desired, ready, cpu float64
	hasCPU              bool
}

var k8sVariants = []struct{ dir, label, color string }{
	{"rate150", "Очередь 150 в секунду", "#eb6834"},
	{"rate50", "Очередь 50 в секунду", "#2a78d6"},
}

func k8sReport(args []string) error {
	fs := flag.NewFlagSet("k8s-report", flag.ContinueOnError)
	in := fs.String("in", "", "каталог data эксперимента")
	out := fs.String("out", "", "куда положить charts/")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return errors.New("usage: loadseed k8s-report -in <data> -out <dir>")
	}
	var runs []k8sRun
	for _, v := range k8sVariants {
		r, err := readK8sRun(filepath.Join(*in, v.dir))
		if err != nil {
			return fmt.Errorf("%s: %w", v.dir, err)
		}
		r.label, r.color = v.label, v.color
		runs = append(runs, r)
	}
	dir := filepath.Join(*out, "charts")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	files := map[string]string{
		"api-pods.svg": timeChart(timeSpec{
			title:    "Готовые поды api: штурм и обратное сжатие",
			subtitle: "Пунктир — сколько подов хочет HPA, вертикаль — последняя покупка.",
			unit:     "подов", from: -30, to: 600, tick: 60,
			value: func(r k8sRow) (float64, bool) { return r.ready, true },
			ghost: func(r k8sRow) (float64, bool) { return r.desired, r.desired > 0 },
		}, runs),
		"api-cpu.svg": timeChart(timeSpec{
			title:    "Загрузка процессора api от запроса",
			subtitle: "Среднее по подам, как его видит HPA: metrics-server обновляет раз в 15 секунд. Порог HPA — 60%.",
			unit:     "% от запроса", from: -30, to: 180, tick: 30, threshold: 60,
			value: func(r k8sRow) (float64, bool) { return r.cpu, r.hasCPU },
		}, runs),
	}
	for name, svg := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(svg), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func readK8sRun(dir string) (k8sRun, error) {
	var r k8sRun
	f, err := os.Open(filepath.Join(dir, "hpa.csv")) //nolint:gosec // путь из флага командной строки
	if err != nil {
		return r, err
	}
	defer func() { _ = f.Close() }()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return r, err
	}
	if len(recs) < 2 {
		return r, errors.New("hpa.csv is empty")
	}
	col := map[string]int{}
	for i, name := range recs[0] {
		col[name] = i
	}
	num := func(rec []string, name string) (float64, bool) {
		s := strings.TrimSpace(rec[col[name]])
		if s == "" {
			return 0, false
		}
		v, err := strconv.ParseFloat(s, 64)
		return v, err == nil
	}
	for _, rec := range recs[1:] {
		var row k8sRow
		row.t, _ = num(rec, "t_s")
		row.desired, _ = num(rec, "api_desired")
		row.ready, _ = num(rec, "api_ready")
		row.cpu, row.hasCPU = num(rec, "api_cpu")
		r.rows = append(r.rows, row)
	}
	var sum struct {
		WonAt map[string]float64 `json:"won_at_ms"`
	}
	if err := readJSON(filepath.Join(dir, "summary.json"), &sum); err != nil {
		return r, err
	}
	r.lastWin = sum.WonAt["max"] / 1000
	return r, nil
}

type timeSpec struct {
	title, subtitle, unit string
	from, to, tick        float64
	threshold             float64 // горизонтальная линия порога; 0 — нет
	value                 func(k8sRow) (float64, bool)
	ghost                 func(k8sRow) (float64, bool) // пунктирная вторая линия; nil — нет
}

// timeChart — ступенчатые линии по времени от старта продаж: значение
// держится до следующего отсчёта, как его и видит HPA.
func timeChart(c timeSpec, runs []k8sRun) string {
	const w, h = 800.0, 436.0
	const left, right, top, bottom = 72.0, 40.0, 124.0, 52.0
	pw, ph := w-left-right, h-top-bottom
	var maxV float64
	for _, r := range runs {
		for _, row := range r.rows {
			if row.t < c.from || row.t > c.to {
				continue
			}
			if v, ok := c.value(row); ok {
				maxV = max(maxV, v)
			}
			if c.ghost != nil {
				if v, ok := c.ghost(row); ok {
					maxV = max(maxV, v)
				}
			}
		}
	}
	maxV = max(maxV, c.threshold)
	yMax, step := niceScale(maxV * 1.1)
	xAt := func(t float64) float64 { return left + (t-c.from)/(c.to-c.from)*pw }
	yAt := func(v float64) float64 { return top + ph - v/yMax*ph }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" font-family="system-ui,-apple-system,'Segoe UI',sans-serif" role="img" aria-label="%s">`, w, h, w, h, html.EscapeString(c.title))
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" fill="%s"/>`, surface)
	fmt.Fprintf(&b, `<text x="24" y="34" font-size="18" font-weight="600" fill="%s">%s</text>`, textMain, html.EscapeString(c.title))
	fmt.Fprintf(&b, `<text x="24" y="56" font-size="12.5" fill="%s">%s</text>`, textMuted, html.EscapeString(c.subtitle))

	lx := 24.0
	for _, r := range runs {
		fmt.Fprintf(&b, `<line x1="%.0f" y1="82" x2="%.0f" y2="82" stroke="%s" stroke-width="2.5" stroke-linecap="round"/>`, lx, lx+22, r.color)
		fmt.Fprintf(&b, `<text x="%.0f" y="86" font-size="12.5" fill="%s">%s</text>`, lx+30, textMain, html.EscapeString(r.label))
		lx += 30 + float64(len([]rune(r.label)))*7.4 + 26
	}

	for v := 0.0; v <= yMax+step/2; v += step {
		y := yAt(v)
		fmt.Fprintf(&b, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" stroke="%s" stroke-width="1"/>`, left, y, left+pw, y, gridColor)
		fmt.Fprintf(&b, `<text x="%.0f" y="%.1f" font-size="11.5" fill="%s" text-anchor="end">%s</text>`, left-10, y+4, textMuted, fmtNum(v))
	}
	fmt.Fprintf(&b, `<text x="24" y="%.0f" font-size="11.5" fill="%s">%s</text>`, top-16, textMuted, html.EscapeString(c.unit))
	for t := c.from - mod(c.from, c.tick); t <= c.to; t += c.tick {
		if t < c.from {
			continue
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" font-size="11.5" fill="%s" text-anchor="middle">%s</text>`, xAt(t), top+ph+22, textMuted, fmtNum(t))
	}
	fmt.Fprintf(&b, `<text x="%.0f" y="%.0f" font-size="11.5" fill="%s" text-anchor="middle">секунд от старта продаж</text>`, left+pw/2, h-10, textMuted)
	// Старт продаж.
	fmt.Fprintf(&b, `<line x1="%.1f" y1="%.0f" x2="%.1f" y2="%.0f" stroke="%s" stroke-width="1"/>`, xAt(0), top, xAt(0), top+ph, textMuted)
	if c.threshold > 0 {
		y := yAt(c.threshold)
		fmt.Fprintf(&b, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" stroke="%s" stroke-width="1.5" stroke-dasharray="6 4"/>`, left, y, left+pw, y, textMain)
		fmt.Fprintf(&b, `<text x="%.0f" y="%.1f" font-size="11.5" fill="%s" text-anchor="end">порог HPA</text>`, left+pw-4, y-6, textMain)
	}

	step2 := func(get func(k8sRow) (float64, bool), r k8sRun) string {
		var p strings.Builder
		started := false
		var lastY float64
		for _, row := range r.rows {
			if row.t < c.from || row.t > c.to {
				continue
			}
			v, ok := get(row)
			if !ok {
				continue
			}
			x, y := xAt(row.t), yAt(v)
			if !started {
				fmt.Fprintf(&p, "M%.1f %.1f", x, y)
				started = true
			} else {
				fmt.Fprintf(&p, "L%.1f %.1fL%.1f %.1f", x, lastY, x, y)
			}
			lastY = y
		}
		if started {
			fmt.Fprintf(&p, "L%.1f %.1f", xAt(c.to), lastY)
		}
		return p.String()
	}
	for _, r := range runs {
		if c.ghost != nil {
			fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="1.5" stroke-dasharray="4 3" opacity="0.8"/>`, step2(c.ghost, r), r.color)
		}
		fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="2.5" stroke-linejoin="round"/>`, step2(c.value, r), r.color)
		if r.lastWin >= c.from && r.lastWin <= c.to {
			x := xAt(r.lastWin)
			fmt.Fprintf(&b, `<line x1="%.1f" y1="%.0f" x2="%.1f" y2="%.0f" stroke="%s" stroke-width="1.5" stroke-dasharray="2 3"/>`, x, top, x, top+ph, r.color)
			fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" font-size="11.5" fill="%s" text-anchor="start">%s с</text>`, x+4, top+12, r.color, strings.Replace(strconv.FormatFloat(r.lastWin, 'f', 1, 64), ".", ",", 1))
		}
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// mod — остаток, всегда неотрицательный.
func mod(a, b float64) float64 {
	m := a - b*float64(int(a/b))
	if m < 0 {
		m += b
	}
	return m
}
