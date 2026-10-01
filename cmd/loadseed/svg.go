package main

import (
	"fmt"
	"html"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Графики — статичный SVG для пояснительной записки и GitHub. Палитра —
// первые три категориальных слота эталонной палитры, проверены валидатором
// на различимость при дальтонизме; у каждой стратегии ещё и своя форма
// маркера, чтобы читалось и в чёрно-белой печати.
var seriesStyle = map[string]struct{ color, marker string }{
	"pessimistic": {"#2a78d6", "circle"},
	"optimistic":  {"#eb6834", "square"},
	"redis":       {"#1baf7a", "diamond"},
}

const (
	surface   = "#fcfcfb"
	textMain  = "#0b0b0b"
	textMuted = "#52514e"
	gridColor = "#e6e4df"
)

type point struct {
	x    string
	stat stat
}

type series struct {
	strategy string
	points   []point
}

type chartSpec struct {
	title, subtitle, unit string
	series                []series
}

func charts(groups []group) map[string]string {
	pick := func(scenario string, f func(group) stat) []series {
		var out []series
		for _, st := range strategyNames {
			s := series{strategy: st}
			for _, g := range groups {
				if g.Scenario == scenario && g.Strategy == st {
					s.points = append(s.points, point{x: strconv.Itoa(g.VUs), stat: f(g)})
				}
			}
			if len(s.points) > 0 {
				out = append(out, s)
			}
		}
		return out
	}
	return map[string]string{
		"one-seat-e2e-p95.svg": lineChart(chartSpec{
			title: "Одно место: время ответа p95", subtitle: "Все покупатели берут одно место в момент старта продаж. Точка — среднее трёх прогонов, усы — разброс.",
			unit: "мс", series: pick("one-seat", func(g group) stat { return g.E2EP95 }),
		}),
		"one-seat-server-p95.svg": lineChart(chartSpec{
			title: "Одно место: время захвата на сервере p95", subtitle: "Только оформление заказа, без входа, ключа идемпотентности и сети (метрика dd_booking_attempt_seconds).",
			unit: "мс", series: pick("one-seat", func(g group) stat { return g.SrvP95 }),
		}),
		"hall-throughput.svg": lineChart(chartSpec{
			title: "Зал на 1000 мест: успешных захватов в секунду", subtitle: "Каждый покупатель берёт случайное место, при отказе пробует другое, до трёх раз.",
			unit: "в секунду", series: pick("hall", func(g group) stat { return g.Throughput }),
		}),
		"hall-e2e-p99.svg": lineChart(chartSpec{
			title: "Зал на 1000 мест: время ответа p99", subtitle: "Хвост задержки — то, что видит самый невезучий покупатель.",
			unit: "мс", series: pick("hall", func(g group) stat { return g.E2EP99 }),
		}),
	}
}

// niceScale подбирает круглый шаг сетки (1, 2, 2.5, 5 × 10ⁿ) и верх оси:
// 4–6 делений с понятными подписями.
func niceScale(v float64) (yMax, step float64) {
	if v <= 0 {
		return 1, 0.25
	}
	raw := v / 5
	exp := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		step = m * exp
		if step >= raw {
			break
		}
	}
	return math.Ceil(v/step) * step, step
}

func fmtNum(v float64) string {
	s := strconv.FormatFloat(math.Round(v), 'f', 0, 64)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func marker(kind, color string, x, y float64) string {
	ring := fmt.Sprintf(`fill=%q stroke=%q stroke-width="2"`, color, surface)
	switch kind {
	case "square":
		return fmt.Sprintf(`<rect x="%.1f" y="%.1f" width="10" height="10" rx="1.5" %s/>`, x-5, y-5, ring)
	case "diamond":
		return fmt.Sprintf(`<path d="M%.1f %.1fl6 6-6 6-6-6z" %s/>`, x, y-6, ring)
	default:
		return fmt.Sprintf(`<circle cx="%.1f" cy="%.1f" r="5" %s/>`, x, y, ring)
	}
}

func lineChart(c chartSpec) string {
	const w, h = 800.0, 436.0
	const left, right, top, bottom = 72.0, 220.0, 124.0, 52.0
	pw, ph := w-left-right, h-top-bottom
	var maxV float64
	var xs []string
	for _, s := range c.series {
		for _, p := range s.points {
			maxV = max(maxV, p.stat.Max)
			if !slices.Contains(xs, p.x) {
				xs = append(xs, p.x)
			}
		}
	}
	slices.SortFunc(xs, func(a, b string) int { ai, _ := strconv.Atoi(a); bi, _ := strconv.Atoi(b); return ai - bi })
	yMax, step := niceScale(maxV * 1.02)
	xAt := func(x string) float64 {
		i := slices.Index(xs, x)
		if len(xs) == 1 {
			return left + pw/2
		}
		return left + 24 + float64(i)*(pw-48)/float64(len(xs)-1)
	}
	yAt := func(v float64) float64 { return top + ph - v/yMax*ph }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" font-family="system-ui,-apple-system,'Segoe UI',sans-serif" role="img" aria-label="%s">`, w, h, w, h, html.EscapeString(c.title))
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" fill="%s"/>`, surface)
	fmt.Fprintf(&b, `<text x="24" y="34" font-size="18" font-weight="600" fill="%s">%s</text>`, textMain, html.EscapeString(c.title))
	fmt.Fprintf(&b, `<text x="24" y="56" font-size="12.5" fill="%s">%s</text>`, textMuted, html.EscapeString(c.subtitle))

	// Легенда: всегда, ключ — линия с маркером рядом с подписью.
	lx := 24.0
	for _, s := range c.series {
		st := seriesStyle[s.strategy]
		fmt.Fprintf(&b, `<line x1="%.0f" y1="82" x2="%.0f" y2="82" stroke="%s" stroke-width="2" stroke-linecap="round"/>`, lx, lx+22, st.color)
		b.WriteString(marker(st.marker, st.color, lx+11, 82))
		name := strategyRu[s.strategy]
		fmt.Fprintf(&b, `<text x="%.0f" y="86" font-size="12.5" fill="%s">%s</text>`, lx+30, textMain, html.EscapeString(name))
		lx += 30 + float64(len([]rune(name)))*7.4 + 26
	}

	// Сетка и ось Y: тонкие сплошные линии, приглушённые.
	for v := 0.0; v <= yMax+step/2; v += step {
		y := yAt(v)
		fmt.Fprintf(&b, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" stroke="%s" stroke-width="1"/>`, left, y, left+pw, y, gridColor)
		fmt.Fprintf(&b, `<text x="%.0f" y="%.1f" font-size="11.5" fill="%s" text-anchor="end">%s</text>`, left-10, y+4, textMuted, fmtNum(v))
	}
	fmt.Fprintf(&b, `<text x="24" y="%.0f" font-size="11.5" fill="%s">%s</text>`, top-16, textMuted, html.EscapeString(c.unit))
	for _, x := range xs {
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" font-size="11.5" fill="%s" text-anchor="middle">%s</text>`, xAt(x), top+ph+22, textMuted, fmtNum(func() float64 { v, _ := strconv.Atoi(x); return float64(v) }()))
	}
	fmt.Fprintf(&b, `<text x="%.0f" y="%.0f" font-size="11.5" fill="%s" text-anchor="middle">одновременных покупателей</text>`, left+pw/2, h-10, textMuted)

	type endLabel struct {
		y, ty float64
		text  string
		color string
		x     float64
	}
	var ends []endLabel
	for si, s := range c.series {
		st := seriesStyle[s.strategy]
		// Лёгкий сдвиг по X, чтобы усы разных серий не сливались.
		dx := float64(si-1) * 7
		var path strings.Builder
		for i, p := range s.points {
			x, y := xAt(p.x)+dx, yAt(p.stat.Mean)
			if i == 0 {
				fmt.Fprintf(&path, "M%.1f %.1f", x, y)
			} else {
				fmt.Fprintf(&path, "L%.1f %.1f", x, y)
			}
		}
		fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`, path.String(), st.color)
		for _, p := range s.points {
			x := xAt(p.x) + dx
			if p.stat.Max > p.stat.Min {
				fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1.5" stroke-opacity="0.55"/>`, x, yAt(p.stat.Min), x, yAt(p.stat.Max), st.color)
			}
		}
		for _, p := range s.points {
			b.WriteString(marker(st.marker, st.color, xAt(p.x)+dx, yAt(p.stat.Mean)))
		}
		last := s.points[len(s.points)-1]
		ends = append(ends, endLabel{y: yAt(last.stat.Mean), text: strategyRu[s.strategy] + " · " + fmtNum(last.stat.Mean), color: st.color, x: xAt(last.x) + dx})
	}
	// Подписи у концов линий: при сближении — разводим и ведём выноску.
	slices.SortFunc(ends, func(a, b endLabel) int { return int(a.y - b.y) })
	for i := range ends {
		ends[i].ty = ends[i].y
		if i > 0 && ends[i].ty-ends[i-1].ty < 18 {
			ends[i].ty = ends[i-1].ty + 18
		}
	}
	for _, e := range ends {
		tx := left + pw + 18
		if math.Abs(e.ty-e.y) > 3 {
			fmt.Fprintf(&b, `<path d="M%.1f %.1fL%.1f %.1f" stroke="%s" stroke-width="1" fill="none"/>`, e.x+8, e.y, tx-4, e.ty-4, textMuted)
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="12" fill="%s">%s</text>`, tx, e.ty, textMain, html.EscapeString(e.text))
	}
	b.WriteString(`</svg>`)
	return b.String()
}
