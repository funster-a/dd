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
