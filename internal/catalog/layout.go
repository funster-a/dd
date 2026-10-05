package catalog

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// Лимиты схемы зала. Целевой организатор — площадки на 100–3000 мест
// (spec.md); лимиты взяты с большим запасом и защищают от ошибок и мусора.
const (
	maxSections     = 200
	maxRowsPerSec   = 500
	maxSeatsPerRow  = 500
	maxSeatsPerMap  = 50_000
	maxLabelLen     = 20
	maxSectionName  = 100
	maxGeneralSeats = 50_000
	maxStandName    = 100
	maxOutlinePts   = 64
	maxPlanSide     = 10_000
)

// Layout — схема зала: сектора с рядами и местами и входные зоны
// (виртуальные места, spec.md). Хранится как документ в seat_maps.layout;
// при публикации события из неё генерируются строки event_seats (ADR 005).
type Layout struct {
	// Plan — план большой площадки (ADR 024): размер и поле. Если он есть,
	// сайт сначала показывает план с секторами, а места — внутри выбранного
	// сектора. У залов без плана схема рисуется целиком, как раньше.
	Plan     *Plan     `json:"plan,omitempty"`
	Sections []Section `json:"sections"`
}

// Plan — система координат плана площадки и то, вокруг чего стоят трибуны.
type Plan struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	// Field — прямоугольник поля или сцены: x, y, ширина, высота.
	Field [4]float64 `json:"field"`
	// FieldLabel — подпись поля: «Поле», «Сцена», «Ринг».
	FieldLabel string `json:"field_label"`
	// Stage — сцена на плане концерта (ADR 025): x, y, ширина, высота.
	Stage *[4]float64 `json:"stage,omitempty"`
}

// Point — точка контура сектора в координатах плана.
type Point [2]float64

// Section — сектор. kind = "seat": ряды и места; kind = "general":
// входная зона только с вместимостью.
type Section struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Rows     []Row  `json:"rows,omitempty"`
	Capacity int    `json:"capacity,omitempty"`
	// Stand — трибуна, к которой относится сектор («Западная трибуна»).
	Stand string `json:"stand,omitempty"`
	// Outline — контур сектора на плане, многоугольник. Обязателен, если у
	// схемы есть план, и запрещён без плана.
	Outline []Point `json:"outline,omitempty"`
}

// Row — ряд сектора с местами.
type Row struct {
	Label string `json:"label"`
	Seats []Seat `json:"seats"`
}

// Seat — место. Координаты нужны редактору и схеме на странице события.
type Seat struct {
	Label string   `json:"label"`
	X     *float64 `json:"x,omitempty"`
	Y     *float64 `json:"y,omitempty"`
}

// Section kinds.
const (
	KindSeat    = "seat"
	KindGeneral = "general"
)

// SeatCount — сколько мест даст схема: места секторов плюс вместимость зон.
func (l Layout) SeatCount() int {
	n := 0
	for _, s := range l.Sections {
		if s.Kind == KindGeneral {
			n += s.Capacity
			continue
		}
		for _, r := range s.Rows {
			n += len(r.Seats)
		}
	}
	return n
}

// ParseLayout разбирает и проверяет схему; поле ошибки указывает путь,
// например sections[2].rows[0].seats[5].label.
func ParseLayout(raw json.RawMessage) (Layout, error) {
	var l Layout
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return Layout{}, &ValidationError{Field: "layout", Message: "must be an object with sections: " + err.Error()}
	}
	// validate нормализует названия на месте, поэтому вызывается до return.
	if err := l.validate(); err != nil {
		return Layout{}, err
	}
	return l, nil
}

func (l *Layout) validate() error {
	if len(l.Sections) == 0 || len(l.Sections) > maxSections {
		return &ValidationError{Field: "layout.sections", Message: fmt.Sprintf("must contain 1-%d sections", maxSections)}
	}
	if l.Plan != nil {
		if err := l.Plan.validate(); err != nil {
			return err
		}
	}
	names := make(map[string]bool, len(l.Sections))
	total := 0
	for i := range l.Sections {
		s := &l.Sections[i]
		path := fmt.Sprintf("layout.sections[%d]", i)
		if err := s.validateGeometry(l.Plan, path); err != nil {
			return err
		}
		s.Name = strings.TrimSpace(s.Name)
		if n := utf8.RuneCountInString(s.Name); n == 0 || n > maxSectionName {
			return &ValidationError{Field: path + ".name", Message: fmt.Sprintf("must be 1-%d characters", maxSectionName)}
		}
		if names[s.Name] {
			return &ValidationError{Field: path + ".name", Message: "section names must be unique"}
		}
		names[s.Name] = true

		switch s.Kind {
		case KindGeneral:
			if len(s.Rows) > 0 {
				return &ValidationError{Field: path + ".rows", Message: "general admission section has no rows"}
			}
			if s.Capacity < 1 || s.Capacity > maxGeneralSeats {
				return &ValidationError{Field: path + ".capacity", Message: fmt.Sprintf("must be 1-%d", maxGeneralSeats)}
			}
			total += s.Capacity
		case KindSeat:
			if s.Capacity != 0 {
				return &ValidationError{Field: path + ".capacity", Message: "seated section is sized by its rows"}
			}
			n, err := validateRows(s.Rows, path)
			if err != nil {
				return err
			}
			total += n
		default:
			return &ValidationError{Field: path + ".kind", Message: `must be "seat" or "general"`}
		}
		if total > maxSeatsPerMap {
			return &ValidationError{Field: "layout", Message: fmt.Sprintf("must have at most %d seats in total", maxSeatsPerMap)}
		}
	}
	return nil
}

func validateRows(rows []Row, path string) (int, error) {
	if len(rows) == 0 || len(rows) > maxRowsPerSec {
		return 0, &ValidationError{Field: path + ".rows", Message: fmt.Sprintf("must contain 1-%d rows", maxRowsPerSec)}
	}
	labels := make(map[string]bool, len(rows))
	n := 0
	for j := range rows {
		r := &rows[j]
		rpath := fmt.Sprintf("%s.rows[%d]", path, j)
		r.Label = strings.TrimSpace(r.Label)
		if err := checkLabel(r.Label, rpath+".label"); err != nil {
			return 0, err
		}
		if labels[r.Label] {
			return 0, &ValidationError{Field: rpath + ".label", Message: "row labels must be unique within a section"}
		}
		labels[r.Label] = true
		if len(r.Seats) == 0 || len(r.Seats) > maxSeatsPerRow {
			return 0, &ValidationError{Field: rpath + ".seats", Message: fmt.Sprintf("must contain 1-%d seats", maxSeatsPerRow)}
		}
		seats := make(map[string]bool, len(r.Seats))
		for k := range r.Seats {
			st := &r.Seats[k]
			spath := fmt.Sprintf("%s.seats[%d]", rpath, k)
			st.Label = strings.TrimSpace(st.Label)
			if err := checkLabel(st.Label, spath+".label"); err != nil {
				return 0, err
			}
			if seats[st.Label] {
				return 0, &ValidationError{Field: spath + ".label", Message: "seat labels must be unique within a row"}
			}
			seats[st.Label] = true
			if (st.X == nil) != (st.Y == nil) || !finite(st.X) || !finite(st.Y) {
				return 0, &ValidationError{Field: spath, Message: "x and y must be set together and be finite numbers"}
			}
		}
		n += len(r.Seats)
	}
	return n, nil
}

func (p *Plan) validate() error {
	if !(p.Width > 0 && p.Width <= maxPlanSide && p.Height > 0 && p.Height <= maxPlanSide) {
		return &ValidationError{Field: "layout.plan", Message: fmt.Sprintf("width and height must be in (0, %d]", maxPlanSide)}
	}
	f := p.Field
	if !(f[2] > 0 && f[3] > 0 && f[0] >= 0 && f[1] >= 0 && f[0]+f[2] <= p.Width && f[1]+f[3] <= p.Height) {
		return &ValidationError{Field: "layout.plan.field", Message: "must be [x, y, width, height] inside the plan"}
	}
	if st := p.Stage; st != nil && !(st[2] > 0 && st[3] > 0 && st[0] >= 0 && st[1] >= 0 && st[0]+st[2] <= p.Width && st[1]+st[3] <= p.Height) {
		return &ValidationError{Field: "layout.plan.stage", Message: "must be [x, y, width, height] inside the plan"}
	}
	p.FieldLabel = strings.TrimSpace(p.FieldLabel)
	if n := utf8.RuneCountInString(p.FieldLabel); n > maxLabelLen {
		return &ValidationError{Field: "layout.plan.field_label", Message: fmt.Sprintf("must be at most %d characters", maxLabelLen)}
	}
	return nil
}

func (s *Section) validateGeometry(plan *Plan, path string) error {
	s.Stand = strings.TrimSpace(s.Stand)
	if utf8.RuneCountInString(s.Stand) > maxStandName {
		return &ValidationError{Field: path + ".stand", Message: fmt.Sprintf("must be at most %d characters", maxStandName)}
	}
	if plan == nil {
		if len(s.Outline) > 0 {
			return &ValidationError{Field: path + ".outline", Message: "outline needs layout.plan"}
		}
		return nil
	}
	if len(s.Outline) < 3 || len(s.Outline) > maxOutlinePts {
		return &ValidationError{Field: path + ".outline", Message: fmt.Sprintf("must have 3-%d points on a plan", maxOutlinePts)}
	}
	for k, pt := range s.Outline {
		if !(pt[0] >= 0 && pt[0] <= plan.Width && pt[1] >= 0 && pt[1] <= plan.Height) {
			return &ValidationError{Field: fmt.Sprintf("%s.outline[%d]", path, k), Message: "must lie inside the plan"}
		}
	}
	return nil
}

func checkLabel(label, field string) error {
	if n := utf8.RuneCountInString(label); n == 0 || n > maxLabelLen {
		return &ValidationError{Field: field, Message: fmt.Sprintf("must be 1-%d characters", maxLabelLen)}
	}
	return nil
}

func finite(f *float64) bool {
	return f == nil || (!math.IsNaN(*f) && !math.IsInf(*f, 0))
}
