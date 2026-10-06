package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Отчёт по отказу RabbitMQ (ADR 030): прогоны mq__<стенд>-<отказ>__<поток>__<повтор>
// серии loadtest/mqchaos.sh. Метрика — путь от оплаты до билета: сколько
// оплаченных заказов получили билеты и через сколько.

var mqOrder = []string{"single-none", "single-kill", "cluster-none", "cluster-kill", "cluster-lose"}

var mqNames = map[string]string{
	"single-none":  "Один узел, без отказа",
	"single-kill":  "Один узел, падение на 20 с",
	"cluster-none": "Кластер, без отказа",
	"cluster-kill": "Кластер, падение узла на 20 с",
	"cluster-lose": "Кластер, узел не вернулся",
}

var mqShort = map[string]string{
	"single-kill": "Один узел, падение", "cluster-kill": "Кластер, падение узла", "cluster-lose": "Кластер, узел потерян",
}

type mqRun struct {
	Variant string
	Tickets struct {
		Orders      int                `json:"orders"`
		Paid        int                `json:"paid"`
		WithTickets int                `json:"with_tickets"`
		Missing     int                `json:"missing"`
		Latency     map[string]float64 `json:"latency_s"`
		MaxBySec    map[string]float64 `json:"max_latency_by_s"`
	}
	Dead int
}

func mqChaosReport(args []string) error {
	fs := flag.NewFlagSet("mqchaos-report", flag.ContinueOnError)
	in := fs.String("in", "", "каталог серии loadtest/mqchaos.sh")
	out := fs.String("out", "", "куда положить таблицы и график")
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
	var runs []mqRun
	for _, e := range entries {
		parts := strings.Split(e.Name(), "__")
		if !e.IsDir() || len(parts) != 4 || parts[0] != "mq" {
			continue
		}
		p := filepath.Join(*in, e.Name())
		r := mqRun{Variant: parts[1]}
		if err := readJSON(filepath.Join(p, "tickets.json"), &r.Tickets); err != nil {
			fmt.Fprintln(os.Stderr, "skip", p, err)
			continue
		}
		var queues []struct {
			Name     string `json:"name"`
			Messages int    `json:"messages"`
		}
		if err := readJSON(filepath.Join(p, "queues.json"), &queues); err == nil {
			for _, q := range queues {
				if strings.HasSuffix(q.Name, ".dead") {
					r.Dead += q.Messages
				}
			}
		}
		runs = append(runs, r)
	}
	if len(runs) == 0 {
		return errors.New("no runs found")
	}
	if err := os.MkdirAll(filepath.Join(*out, "charts"), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "tables.md"), []byte(mqTables(runs)), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*out, "charts", "ticket-latency.svg"), []byte(mqChart(runs)), 0o600)
}

func mqTables(runs []mqRun) string {
	var b strings.Builder
	b.WriteString("<!-- Сгенерировано: go run ./cmd/loadseed mqchaos-report. Не править руками. -->\n\n")
	b.WriteString("Задержка — от записи оплаты в outbox до выпуска билета. Перцентили — среднее по прогонам, максимум — по всем прогонам. " +
		"«Без билета» — оплаченные заказы без билетов через 3 минуты после конца потока.\n\n")
	b.WriteString("| Вариант | Прогонов | Оплат | Без билета | В очередях .dead | Задержка p50, с | p95, с | p99, с | max, с |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, v := range mqOrder {
		var n, orders, missing, dead int
		var p50, p95, p99, mx float64
		for _, r := range runs {
			if r.Variant != v {
				continue
			}
			n++
			orders += r.Tickets.Orders
			missing += r.Tickets.Missing
			dead += r.Dead
			p50 += r.Tickets.Latency["p50"]
			p95 += r.Tickets.Latency["p95"]
			p99 += r.Tickets.Latency["p99"]
			mx = max(mx, r.Tickets.Latency["max"])
		}
		if n == 0 {
			continue
		}
		k := float64(n)
		dec := func(v float64) string { return strings.Replace(strconv.FormatFloat(v, 'f', 1, 64), ".", ",", 1) }
		fmt.Fprintf(&b, "| %s | %d | %s | %d | %d | %s | %s | %s | %s |\n", mqNames[v], n, fmtNum(float64(orders)), missing, dead,
			dec(p50/k), dec(p95/k), dec(p99/k), dec(mx))
	}
	return b.String()
}

// mqChart — худшая задержка билета по секунде оплаты, в среднем по прогонам.
func mqChart(runs []mqRun) string {
	colors := []string{"#eb6834", "#2a78d6", "#1baf7a"}
	var ss []series
	i := 0
	for _, v := range mqOrder {
		label, ok := mqShort[v]
		if !ok || !slices.ContainsFunc(runs, func(r mqRun) bool { return r.Variant == v }) {
			continue
		}
		key := "mqchaos-" + v
		seriesStyle[key] = struct{ color, marker string }{colors[i%len(colors)], []string{"circle", "square", "diamond"}[i%3]}
		strategyRu[key] = label
		i++
		s := series{strategy: key}
		for sec := 10; sec <= 50; sec++ {
			var sum, n float64
			for _, r := range runs {
				if r.Variant == v {
					sum += r.Tickets.MaxBySec[strconv.Itoa(sec)]
					n++
				}
			}
			c := round1(sum / n)
			s.points = append(s.points, point{x: strconv.Itoa(sec), stat: stat{Mean: c, Min: c, Max: c}})
		}
		ss = append(ss, s)
	}
	return lineChart(chartSpec{
		title:    "Худшая задержка билета по секунде оплаты",
		subtitle: "Узел RabbitMQ убит на 20-й секунде, поднят на 40-й. В среднем по прогонам.",
		unit:     "секунд", series: ss, xLabel: "секунда оплаты от начала потока",
	})
}
