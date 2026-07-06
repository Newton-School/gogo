package humanize

import (
	"testing"
	"time"

	"github.com/Newton-School/gogo/templates"
)

func TestHumanizeFiltersAndTemplateIntegration(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	filters := Filters(Options{Now: func() time.Time { return now }})
	tests := map[string]struct {
		got  string
		want string
	}{
		"apnumber":    {filters.Apnumber(3), "three"},
		"intcomma":    {filters.Intcomma(1234567), "1,234,567"},
		"intword":     {filters.Intword(2_500_000), "2.5 million"},
		"naturalday":  {filters.Naturalday(now.AddDate(0, 0, -1)), "yesterday"},
		"naturaltime": {filters.Naturaltime(now.Add(-2 * time.Hour)), "2 hours ago"},
		"ordinal":     {filters.Ordinal(23), "23rd"},
		"invalid":     {filters.Intcomma("bad"), "bad"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("got %q, want %q", test.got, test.want)
			}
		})
	}
	engine := templates.NewEngine(
		templates.WithTemplates(map[string]string{"page": `{{intcomma .value}} {{ordinal 2}}`}),
		templates.WithFuncMap(TemplateFilters(Options{Now: func() time.Time { return now }})),
	)
	rendered, err := engine.Render("page", templates.Context{"value": 1234})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if rendered != "1,234 2nd" {
		t.Fatalf("rendered = %q", rendered)
	}
}
