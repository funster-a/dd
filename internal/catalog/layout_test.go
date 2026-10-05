package catalog

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseLayoutValid(t *testing.T) {
	raw := `{"sections":[
		{"name":" Партер ","kind":"seat","rows":[
			{"label":"1","seats":[{"label":"1","x":10,"y":20},{"label":" 2 "}]},
			{"label":"2","seats":[{"label":"1"}]}
		]},
		{"name":"Танцпол","kind":"general","capacity":500}
	]}`
	l, err := ParseLayout([]byte(raw))
	if err != nil {
		t.Fatalf("ParseLayout() error = %v", err)
	}
	if got := l.SeatCount(); got != 503 {
		t.Errorf("SeatCount() = %d, want 503", got)
	}
	if l.Sections[0].Name != "Партер" || l.Sections[0].Rows[0].Seats[1].Label != "2" {
		t.Errorf("labels are not trimmed: %+v", l.Sections[0])
	}
}

func TestParseLayoutPlan(t *testing.T) {
	raw := `{"plan":{"width":100,"height":80,"field":[30,20,40,40],"field_label":" Поле "},"sections":[
		{"name":"Сектор 1","stand":" Западная трибуна ","kind":"seat","outline":[[30,62],[70,62],[75,78],[25,78]],
		 "rows":[{"label":"1","seats":[{"label":"1"}]}]}
	]}`
	l, err := ParseLayout([]byte(raw))
	if err != nil {
		t.Fatalf("ParseLayout() error = %v", err)
	}
	if l.Plan.FieldLabel != "Поле" || l.Sections[0].Stand != "Западная трибуна" || len(l.Sections[0].Outline) != 4 {
		t.Errorf("plan is not parsed: %+v %+v", l.Plan, l.Sections[0])
	}
}

func TestParseLayoutInvalid(t *testing.T) {
	seats := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf(`{"label":"%d"}`, i+1)
		}
		return strings.Join(parts, ",")
	}
	tests := []struct {
		name  string
		raw   string
		field string
	}{
		{"not an object", `[]`, "layout"},
		{"unknown field", `{"sections":[],"extra":1}`, "layout"},
		{"no sections", `{"sections":[]}`, "layout.sections"},
		{"empty section name", `{"sections":[{"name":" ","kind":"general","capacity":1}]}`, "layout.sections[0].name"},
		{"duplicate section", `{"sections":[{"name":"A","kind":"general","capacity":1},{"name":"A","kind":"general","capacity":1}]}`, "layout.sections[1].name"},
		{"bad kind", `{"sections":[{"name":"A","kind":"vip"}]}`, "layout.sections[0].kind"},
		{"general with rows", `{"sections":[{"name":"A","kind":"general","capacity":5,"rows":[{"label":"1","seats":[{"label":"1"}]}]}]}`, "layout.sections[0].rows"},
		{"general zero capacity", `{"sections":[{"name":"A","kind":"general","capacity":0}]}`, "layout.sections[0].capacity"},
		{"seated with capacity", `{"sections":[{"name":"A","kind":"seat","capacity":5,"rows":[{"label":"1","seats":[{"label":"1"}]}]}]}`, "layout.sections[0].capacity"},
		{"seated without rows", `{"sections":[{"name":"A","kind":"seat"}]}`, "layout.sections[0].rows"},
		{"duplicate row", `{"sections":[{"name":"A","kind":"seat","rows":[{"label":"1","seats":[{"label":"1"}]},{"label":"1","seats":[{"label":"1"}]}]}]}`, "layout.sections[0].rows[1].label"},
		{"empty row", `{"sections":[{"name":"A","kind":"seat","rows":[{"label":"1","seats":[]}]}]}`, "layout.sections[0].rows[0].seats"},
		{"duplicate seat", `{"sections":[{"name":"A","kind":"seat","rows":[{"label":"1","seats":[{"label":"1"},{"label":"1"}]}]}]}`, "layout.sections[0].rows[0].seats[1].label"},
		{"long label", `{"sections":[{"name":"A","kind":"seat","rows":[{"label":"123456789012345678901","seats":[{"label":"1"}]}]}]}`, "layout.sections[0].rows[0].label"},
		{"x without y", `{"sections":[{"name":"A","kind":"seat","rows":[{"label":"1","seats":[{"label":"1","x":1}]}]}]}`, "layout.sections[0].rows[0].seats[0]"},
		{"outline without plan", `{"sections":[{"name":"A","kind":"general","capacity":1,"outline":[[0,0],[1,0],[1,1]]}]}`, "layout.sections[0].outline"},
		{"plan without outline", `{"plan":{"width":100,"height":100,"field":[10,10,20,20]},"sections":[{"name":"A","kind":"general","capacity":1}]}`, "layout.sections[0].outline"},
		{"empty plan", `{"plan":{"width":0,"height":100,"field":[10,10,20,20]},"sections":[{"name":"A","kind":"general","capacity":1,"outline":[[0,0],[1,0],[1,1]]}]}`, "layout.plan"},
		{"field outside plan", `{"plan":{"width":100,"height":100,"field":[90,10,20,20]},"sections":[{"name":"A","kind":"general","capacity":1,"outline":[[0,0],[1,0],[1,1]]}]}`, "layout.plan.field"},
		{"outline outside plan", `{"plan":{"width":100,"height":100,"field":[10,10,20,20]},"sections":[{"name":"A","kind":"general","capacity":1,"outline":[[0,0],[101,0],[1,1]]}]}`, "layout.sections[0].outline[1]"},
		{"too many seats", `{"sections":[{"name":"A","kind":"general","capacity":50000},{"name":"B","kind":"seat","rows":[{"label":"1","seats":[` + seats(1) + `]}]}]}`, "layout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseLayout([]byte(tt.raw))
			v, ok := errors.AsType[*ValidationError](err)
			if !ok {
				t.Fatalf("err = %v, want ValidationError", err)
			}
			if v.Field != tt.field {
				t.Errorf("field = %q, want %q (%s)", v.Field, tt.field, v.Message)
			}
		})
	}
}
