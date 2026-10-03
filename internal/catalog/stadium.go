package catalog

import (
	"fmt"
	"math"
	"strconv"
)

// Схема Центрального стадиона Алматы (ADR 024) — первый шаблон большой
// площадки. Из открытых источников взяты:
//   - вместимость — 23 804 места;
//   - четыре трибуны и нумерация секторов: западная 1–17, северная 18–28,
//     восточная 30–48, южная 49–60 (сектора 29 нет);
//   - центр западной трибуны — сектора 12–15, угловые сектора у западной
//     трибуны — 18–19 и 59–60 (по ценовым категориям матчей «Кайрата»).
//
// Геометрия схематическая: чаша вокруг поля с беговой дорожкой, прямые
// западная и восточная трибуны, закруглённые северная и южная за воротами.
// Западная трибуна в два яруса: нижний — сектора 1–9, верхний — 10–17.
// Число рядов и мест в секторах подобрано по длине рядов так, чтобы сумма
// совпала с вместимостью стадиона; точная раскладка рядов — у владельца
// площадки, организатор правит её перед продажей.

const (
	almatyCentralSeats = 23_804

	// План: западная трибуна внизу, восточная вверху, северная слева,
	// южная справа — как смотрит зритель с главной трибуны.
	stadiumW  = 1100.0
	stadiumH  = 780.0
	stadiumCX = 550.0
	stadiumCY = 390.0
	// Внутренняя граница чаши — внешний край беговой дорожки: прямые длиной
	// 2×straightHalf и полукруги радиуса bowlR.
	straightHalf = 185.0
	bowlR        = 185.0
	// Проход между дорожкой и первым рядом.
	bowlGap = 12.0
	// Доля длины сектора, которая уходит на проходы между секторами.
	aisleShare = 0.06
)

// stand — трибуна: дуга кольца чаши, которую сектора делят поровну.
// Доли кольца у трибун идут подряд по ходу нумерации секторов: запад,
// север, восток, юг. Ярусы одной трибуны занимают одну дугу.
type stand struct {
	name         string
	first, count int     // номера секторов
	share        float64 // доля кольца; 0 — та же дуга, что у предыдущей
	inner, depth float64 // от внутренней границы чаши
	rows         int
}

var almatyCentralStands = []stand{
	{name: "Западная трибуна", first: 1, count: 9, share: 0.26, inner: bowlGap, depth: 70, rows: 14},
	{name: "Западная трибуна", first: 10, count: 8, inner: bowlGap + 84, depth: 92, rows: 18},
	{name: "Северная трибуна", first: 18, count: 11, share: 0.20, inner: bowlGap, depth: 150, rows: 26},
	{name: "Восточная трибуна", first: 30, count: 19, share: 0.34, inner: bowlGap, depth: 150, rows: 26},
	{name: "Южная трибуна", first: 49, count: 12, share: 0.20, inner: bowlGap, depth: 150, rows: 26},
}

// ringRef — радиус, по которому меряются доли кольца: первый ряд.
const ringRef = bowlR + bowlGap

func ringPerimeter() float64 { return 4*straightHalf + 2*math.Pi*ringRef }

// ringPoint — точка чаши на расстоянии r от центра закругления. u —
// положение вдоль кольца в долях периметра первого ряда: 0 — правый край
// западной прямой, дальше по ходу нумерации (запад справа налево, север
// снизу вверх, восток слева направо, юг сверху вниз). На прямых точки
// разных рядов лежат на перпендикуляре, на закруглениях — на радиусе.
func ringPoint(u, r float64) Point {
	p := ringPerimeter()
	d := math.Mod(u, 1)
	if d < 0 {
		d++
	}
	d *= p
	x0, x1 := stadiumCX-straightHalf, stadiumCX+straightHalf
	straight, arc := 2*straightHalf, math.Pi*ringRef
	switch {
	case d < straight:
		return Point{x1 - d, stadiumCY + r}
	case d < straight+arc:
		a := math.Pi/2 + (d-straight)/ringRef
		return Point{x0 + r*math.Cos(a), stadiumCY + r*math.Sin(a)}
	case d < 2*straight+arc:
		return Point{x0 + (d - straight - arc), stadiumCY - r}
	default:
		a := -math.Pi/2 + (d-2*straight-arc)/ringRef
		return Point{x1 + r*math.Cos(a), stadiumCY + r*math.Sin(a)}
	}
}

// ringLength — длина ряда на расстоянии r между долями u0 и u1.
func ringLength(u0, u1, r float64) float64 {
	const steps = 64
	n := 0.0
	prev := ringPoint(u0, r)
	for i := 1; i <= steps; i++ {
		pt := ringPoint(u0+(u1-u0)*float64(i)/steps, r)
		n += math.Hypot(pt[0]-prev[0], pt[1]-prev[1])
		prev = pt
	}
	return n
}

type sectorShape struct {
	st     stand
	number int
	u0, u1 float64
}

func (sh sectorShape) outline() []Point {
	ri, ro := bowlR+sh.st.inner, bowlR+sh.st.inner+sh.st.depth
	const steps = 6 // дуга — ломаной; на прямой лишние точки безвредны
	pts := make([]Point, 0, 2*(steps+1))
	for i := 0; i <= steps; i++ {
		pts = append(pts, roundPoint(ringPoint(sh.u0+(sh.u1-sh.u0)*float64(i)/steps, ri)))
	}
	for i := steps; i >= 0; i-- {
		pts = append(pts, roundPoint(ringPoint(sh.u0+(sh.u1-sh.u0)*float64(i)/steps, ro)))
	}
	return pts
}

// rowLengths — длина каждого ряда сектора, от поля наверх.
func (sh sectorShape) rowLengths() []float64 {
	out := make([]float64, sh.st.rows)
	for k := range out {
		r := bowlR + sh.st.inner + (float64(k)+0.5)*sh.st.depth/float64(sh.st.rows)
		out[k] = ringLength(sh.u0, sh.u1, r)
	}
	return out
}

func roundPoint(p Point) Point {
	return Point{math.Round(p[0]*10) / 10, math.Round(p[1]*10) / 10}
}

func almatyCentralShapes() []sectorShape {
	var out []sectorShape
	// Западная трибуна — по центру нижней прямой, остальные — следом.
	mid := straightHalf / ringPerimeter()
	from, to := 0.0, mid-almatyCentralStands[0].share/2
	for _, st := range almatyCentralStands {
		if st.share > 0 {
			from, to = to, to+st.share
		}
		w := (to - from) / float64(st.count)
		for i := range st.count {
			u0 := from + float64(i)*w + w*aisleShare/2
			out = append(out, sectorShape{st: st, number: st.first + i, u0: u0, u1: u0 + w*(1-aisleShare)})
		}
	}
	return out
}

// almatyCentralLayout строит схему: места в ряду — длина ряда, делённая на
// шаг места. Шаг подбирается так, чтобы мест было не меньше вместимости, а
// лишние снимаются с последних рядов восточной трибуны — так сумма точно
// равна 23 804.
func almatyCentralLayout() Layout {
	shapes := almatyCentralShapes()
	count := func(pitch float64) int {
		n := 0
		for _, sh := range shapes {
			for _, l := range sh.rowLengths() {
				n += max(1, int(l/pitch))
			}
		}
		return n
	}
	lo, hi := 0.5, 20.0 // шаг места в единицах плана
	for range 60 {
		mid := (lo + hi) / 2
		if count(mid) >= almatyCentralSeats {
			lo = mid
		} else {
			hi = mid
		}
	}
	pitch := lo
	extra := count(pitch) - almatyCentralSeats

	seats := make([][]int, len(shapes))
	for i, sh := range shapes {
		for _, l := range sh.rowLengths() {
			seats[i] = append(seats[i], max(1, int(l/pitch)))
		}
	}
	// Лишние места — по одному с верхнего ряда восточных секторов по кругу.
	for extra > 0 {
		for i, sh := range shapes {
			if extra == 0 {
				break
			}
			if sh.st.name == "Восточная трибуна" {
				seats[i][len(seats[i])-1]--
				extra--
			}
		}
	}

	l := Layout{Plan: &Plan{
		Width: stadiumW, Height: stadiumH, FieldLabel: "Поле",
		Field: [4]float64{stadiumCX - 150, stadiumCY - 97, 300, 194},
	}}
	for i, sh := range shapes {
		sec := Section{
			Name: fmt.Sprintf("Сектор %d", sh.number), Kind: KindSeat, Stand: sh.st.name,
			Outline: sh.outline(),
		}
		for k, n := range seats[i] {
			row := Row{Label: strconv.Itoa(k + 1), Seats: make([]Seat, n)}
			for j := range n {
				row.Seats[j] = Seat{Label: strconv.Itoa(j + 1)}
			}
			sec.Rows = append(sec.Rows, row)
		}
		l.Sections = append(l.Sections, sec)
	}
	return l
}
