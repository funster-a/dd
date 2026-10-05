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

// Отчёт по штурму стадиона (ADR 026): прогоны storm__<вариант>__<покупателей>__<повтор>
// сценария loadtest/stadium.js. Варианты: nocache — без очереди и без кэша
// сводки, off — без очереди, qN — очередь на N покупателей в секунду.

type stormSummary struct {
	Buyers        float64            `json:"buyers"`
	Queue         bool               `json:"queue"`
	SeatsTotal    float64            `json:"seats_total"`
	Won           float64            `json:"won"`
	Tickets       float64            `json:"tickets"`
	ZoneTickets   float64            `json:"zone_tickets"`
	Conflicts     float64            `json:"conflicts"`
	ConflictSeat  float64            `json:"conflict_seat_taken"`
	ConflictZone  float64            `json:"conflict_not_enough"`
	ConflictView  float64            `json:"conflict_no_seats_in_view"`
	ConflictOther float64            `json:"conflict_other"`
	GaveUpSoldOut float64            `json:"gave_up_sold_out"`
	GaveUpTries   float64            `json:"gave_up_tries"`
	Failed        float64            `json:"failed"`
	Failed5xx     float64            `json:"failed_5xx"`
	FailedNetwork float64            `json:"failed_network"`
	Attempts      float64            `json:"attempts"`
	WonAtMS       map[string]float64 `json:"won_at_ms"`
	EventMS       map[string]float64 `json:"event_ms"`
	SummaryMS     map[string]float64 `json:"summary_ms"`
	SectionMS     map[string]float64 `json:"section_ms"`
	OrderMS       map[string]float64 `json:"order_ms"`
	DataReceived  float64            `json:"data_received"`
	QueueWaitMS   map[string]float64 `json:"queue_wait_ms"`
	QueuePolls    float64            `json:"queue_polls"`
}

type stormRun struct {
	Variant string
	Rep     int
	K6      stormSummary
	Check   checkResult
}

func stormVariantOrder(v string) int {
	switch v {
	case "nocache":
		return -2
	case "off":
		return -1
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(v, "q"))
	return n
}

func stormVariantRu(v string) string {
	switch v {
	case "nocache":
		return "Без очереди, без кэша сводки"
	case "off":
		return "Без очереди"
	}
	return "Очередь, " + strings.TrimPrefix(v, "q") + " в секунду"
}

var stormColors = map[string]string{"nocache": "#c0321d", "off": "#eb6834", "q150": "#2a78d6", "q300": "#1baf7a"}

func stormReport(args []string) error {
	fs := flag.NewFlagSet("storm-report", flag.ContinueOnError)
	dir := fs.String("dir", "", "каталог серии (loadtest/results/raw/storm)")
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
	var runs []stormRun
	for _, e := range entries {
		parts := strings.Split(e.Name(), "__")
		if !e.IsDir() || len(parts) != 4 || parts[0] != "storm" {
			continue
		}
		rep, _ := strconv.Atoi(parts[3])
		r := stormRun{Variant: parts[1], Rep: rep}
		if err := readJSON(filepath.Join(*dir, e.Name(), "summary.json"), &r.K6); err != nil {
			fmt.Fprintln(os.Stderr, "skip", e.Name(), err)
			continue
		}
		if err := readJSON(filepath.Join(*dir, e.Name(), "check.json"), &r.Check); err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		runs = append(runs, r)
	}
	if len(runs) == 0 {
		return errors.New("no runs found")
	}
	slices.SortFunc(runs, func(a, b stormRun) int {
		return cmp.Or(cmp.Compare(stormVariantOrder(a.Variant), stormVariantOrder(b.Variant)), cmp.Compare(a.Rep, b.Rep))
	})
	if err := os.MkdirAll(filepath.Join(*out, "charts"), 0o750); err != nil {
		return err
	}
	if err := writeStormCSV(filepath.Join(*out, "results.csv"), runs); err != nil {
		return err
	}
	groups := aggregateStorm(runs)
	if err := os.WriteFile(filepath.Join(*out, "tables.md"), []byte(stormTables(groups)), 0o600); err != nil {
		return err
	}
	for name, svg := range stormCharts(groups) {
		if err := os.WriteFile(filepath.Join(*out, "charts", name), []byte(svg), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func writeStormCSV(path string, runs []stormRun) error {
	f, err := os.Create(path) //nolint:gosec // путь задаёт автор отчёта
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"variant", "rep", "buyers", "won", "tickets", "zone_tickets", "seats_total", "conflicts", "conflict_seat_taken",
		"conflict_not_enough", "conflict_no_seats_in_view", "conflict_other", "gave_up_sold_out", "gave_up_tries", "failed", "failed_5xx",
		"failed_network", "attempts", "order_p50_ms", "order_p95_ms", "summary_p95_ms", "section_p95_ms", "event_p95_ms",
		"won_at_p50_ms", "won_at_max_ms", "queue_wait_p95_ms", "queue_polls", "kb_per_buyer", "double_booked", "mismatched"})
	ff := func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
	for _, r := range runs {
		k := r.K6
		_ = w.Write([]string{r.Variant, strconv.Itoa(r.Rep), ff(k.Buyers), ff(k.Won), ff(k.Tickets), ff(k.ZoneTickets), ff(k.SeatsTotal),
			ff(k.Conflicts), ff(k.ConflictSeat), ff(k.ConflictZone), ff(k.ConflictView), ff(k.ConflictOther), ff(k.GaveUpSoldOut),
			ff(k.GaveUpTries), ff(k.Failed), ff(k.Failed5xx), ff(k.FailedNetwork), ff(k.Attempts),
			ff(k.OrderMS["med"]), ff(k.OrderMS["p(95)"]), ff(k.SummaryMS["p(95)"]), ff(k.SectionMS["p(95)"]), ff(k.EventMS["p(95)"]),
			ff(k.WonAtMS["med"]), ff(k.WonAtMS["max"]), ff(k.QueueWaitMS["p(95)"]), ff(k.QueuePolls), ff(k.DataReceived / max(k.Buyers, 1) / 1024),
			strconv.Itoa(r.Check.DoubleBooked), strconv.Itoa(r.Check.Mismatched)})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

type stormGroup struct {
	Variant                                        string
	Runs                                           int
	Buyers, Seats                                  float64
	Won, Tickets, Conflicts, Failed, GaveUp        stat
	ConflictSeat, ConflictZone, ConflictView       stat
	OrderP50, OrderP95, SummaryP95, SectionP95     stat
	EventP95, WonAtP50, WonAtMax, QueueWaitP95, KB stat
	Polls                                          stat
	DoubleBooked                                   int
}

func aggregateStorm(runs []stormRun) []stormGroup {
	by := map[string][]stormRun{}
	var order []string
	for _, r := range runs {
		if _, ok := by[r.Variant]; !ok {
			order = append(order, r.Variant)
		}
		by[r.Variant] = append(by[r.Variant], r)
	}
	var out []stormGroup
	for _, v := range order {
		rs := by[v]
		col := func(f func(stormSummary) float64) stat {
			vals := make([]float64, len(rs))
			for i, r := range rs {
				vals[i] = f(r.K6)
			}
			return statOf(vals)
		}
		g := stormGroup{Variant: v, Runs: len(rs), Buyers: rs[0].K6.Buyers, Seats: rs[0].K6.SeatsTotal,
			Won:          col(func(k stormSummary) float64 { return k.Won }),
			Tickets:      col(func(k stormSummary) float64 { return k.Tickets }),
			Conflicts:    col(func(k stormSummary) float64 { return k.Conflicts }),
			ConflictSeat: col(func(k stormSummary) float64 { return k.ConflictSeat }),
			ConflictZone: col(func(k stormSummary) float64 { return k.ConflictZone }),
			ConflictView: col(func(k stormSummary) float64 { return k.ConflictView }),
			Failed:       col(func(k stormSummary) float64 { return k.Failed }),
			GaveUp:       col(func(k stormSummary) float64 { return k.GaveUpSoldOut + k.GaveUpTries }),
			OrderP50:     col(func(k stormSummary) float64 { return k.OrderMS["med"] }),
			OrderP95:     col(func(k stormSummary) float64 { return k.OrderMS["p(95)"] }),
			SummaryP95:   col(func(k stormSummary) float64 { return k.SummaryMS["p(95)"] }),
			SectionP95:   col(func(k stormSummary) float64 { return k.SectionMS["p(95)"] }),
			EventP95:     col(func(k stormSummary) float64 { return k.EventMS["p(95)"] }),
			WonAtP50:     col(func(k stormSummary) float64 { return k.WonAtMS["med"] / 1000 }),
			WonAtMax:     col(func(k stormSummary) float64 { return k.WonAtMS["max"] / 1000 }),
			QueueWaitP95: col(func(k stormSummary) float64 { return k.QueueWaitMS["p(95)"] / 1000 }),
			KB:           col(func(k stormSummary) float64 { return k.DataReceived / max(k.Buyers, 1) / 1024 }),
			Polls:        col(func(k stormSummary) float64 { return k.QueuePolls / max(k.Buyers, 1) }),
		}
		for _, r := range rs {
			g.DoubleBooked += r.Check.DoubleBooked + r.Check.Mismatched
		}
		out = append(out, g)
	}
	return out
}

// sv — среднее по прогонам; разброс, если он заметен.
func sv(s stat) string {
	m := fmtNum(round1(s.Mean))
	if s.Max-s.Min > max(0.05*s.Mean, 1) {
		return fmt.Sprintf("%s (%s–%s)", m, fmtNum(round1(s.Min)), fmtNum(round1(s.Max)))
	}
	return m
}

func stormTables(groups []stormGroup) string {
	var b strings.Builder
	b.WriteString("<!-- Сгенерировано: go run ./cmd/loadseed storm-report. Не править руками. -->\n\n")
	if len(groups) > 0 {
		fmt.Fprintf(&b, "%s покупателей, %s мест. Среднее по прогонам, в скобках — разброс.\n\n", fmtNum(groups[0].Buyers), fmtNum(groups[0].Seats))
	}
	b.WriteString("| Вариант | Прогонов | Купили | Билетов | Ошибок | Ушли без билета | Заказ p50, мс | Заказ p95, мс | Последняя покупка, с | Двойных броней |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, g := range groups {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s | %s | %s | %d |\n", stormVariantRu(g.Variant), g.Runs, sv(g.Won), sv(g.Tickets),
			sv(g.Failed), sv(g.GaveUp), sv(g.OrderP50), sv(g.OrderP95), sv(g.WonAtMax), g.DoubleBooked)
	}
	b.WriteString("\nОтказы заказа и чтение занятости:\n\n")
	b.WriteString("| Вариант | Отказов всего | Место заняли | В фан-зоне не хватило | В снимке сектора нет мест рядом | Сводка p95, мс | Сектор p95, мс | Страница события p95, мс | КБ на покупателя |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, g := range groups {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", stormVariantRu(g.Variant), sv(g.Conflicts), sv(g.ConflictSeat),
			sv(g.ConflictZone), sv(g.ConflictView), sv(g.SummaryP95), sv(g.SectionP95), sv(g.EventP95), sv(g.KB))
	}
	b.WriteString("\nОчередь:\n\n| Вариант | Ожидание p95, с | Опросов на покупателя | Половина покупок — к, с |\n|---|---:|---:|---:|\n")
	for _, g := range groups {
		if !strings.HasPrefix(g.Variant, "q") {
			continue
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", stormVariantRu(g.Variant), sv(g.QueueWaitP95), sv(g.Polls), sv(g.WonAtP50))
	}
	return b.String()
}

func stormCharts(groups []stormGroup) map[string]string {
	pick := func(f func(stormGroup) stat) []bar {
		var out []bar
		for _, g := range groups {
			c, ok := stormColors[g.Variant]
			if !ok {
				c = "#8a6bd1"
			}
			out = append(out, bar{label: stormVariantRu(g.Variant), stat: f(g), color: c})
		}
		return out
	}
	n := "?"
	if len(groups) > 0 {
		n = fmtNum(groups[0].Buyers)
	}
	return map[string]string{
		"order-p95.svg": barChart("Время ответа на заказ, p95",
			n+" покупателей в момент старта продаж концерта на стадионе. Столбец — среднее прогонов, ус — разброс.", "мс",
			pick(func(g stormGroup) stat { return g.OrderP95 })),
		"last-sale.svg": barChart("Когда прошла последняя покупка",
			"Секунд от старта продаж. С очередью дольше: покупателей пускают с постоянной скоростью.", "с",
			pick(func(g stormGroup) stat { return g.WonAtMax })),
	}
}
