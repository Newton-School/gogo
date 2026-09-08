package templates

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type descriptionPanicLoader struct{}

func (descriptionPanicLoader) Load(context.Context, string) (string, error) {
	panic("description invoked a loader")
}

func TestTemplateDescriptionEffectiveNamesAndOwnership(t *testing.T) {
	calls := 0
	tags := map[string]Tag{
		"custom": func(context.Context, Context, []any) (any, error) { panic("description invoked tag") },
		"if":     func(context.Context, Context, []any) (any, error) { panic("builtin tag overridden") },
		"endif":  nil, // A parser delimiter never dispatches this registration.
	}
	filters := map[string]Filter{"lower": func(context.Context, any, any) (any, error) { calls++; return "override", nil }}
	e := New(Config{Loaders: []Loader{descriptionPanicLoader{}}, Tags: tags, Filters: filters,
		Processors: []Processor{func(context.Context) (Context, error) { panic("description invoked processor") }}})
	d, err := e.Describe(4096)
	if err != nil || calls != 0 || !slices.Contains(d.Tags, "custom") || slices.Contains(d.Tags, "endif") || !slices.Contains(d.Filters, "lower") {
		t.Fatal(d, err, calls)
	}
	if !slices.IsSorted(d.Tags) || !slices.IsSorted(d.Filters) || len(slices.Compact(slices.Clone(d.Tags))) != len(d.Tags) {
		t.Fatal("inventory is not sorted and unique", d)
	}
	tags["later"] = tags["custom"]
	filters["later"] = filters["lower"]
	want := Description{Tags: slices.Clone(d.Tags), Filters: slices.Clone(d.Filters)}
	d.Tags[0], d.Filters[0] = "changed", "changed"
	again, err := e.Describe(4096)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatal("description retained caller-owned containers", again, err)
	}
	// A description reports the effective name once; it does not mislabel a
	// replaced filter as the original builtin implementation.
	actual := New(Config{Filters: map[string]Filter{"lower": filters["lower"]}, Tags: tags})
	output, err := actual.RenderString(context.Background(), `{% if True %}{{ "x"|lower }}{% endif %}`, nil)
	if err != nil || output != "override" || calls != 1 {
		t.Fatal("fixture did not prove effective override/builtin-tag precedence", output, err, calls)
	}
}

func TestTemplateDescriptionBuiltinInventoryMatchesWorkingSyntax(t *testing.T) {
	fixtures := map[string]string{
		"autoescape":           `{% autoescape on %}{{ value }}{% endautoescape %}`,
		"block":                `{% block main %}x{% endblock %}`,
		"comment":              `{% comment %}{% not_a_tag %}{% endcomment %}`,
		"csrf_token":           `{% csrf_token %}`,
		"cycle":                `{% cycle "one" "two" %}`,
		"debug":                `{% debug %}`,
		"extends":              `{% extends "base" %}{% block main %}child{% endblock %}`,
		"filter":               `{% filter lower %}UPPER{% endfilter %}`,
		"firstof":              `{% firstof value "fallback" %}`,
		"for":                  `{% for x in items %}{{ x.group }}{% empty %}none{% endfor %}`,
		"get_current_timezone": `{% get_current_timezone as zone %}{{ zone }}`,
		"if":                   `{% if value %}yes{% elif missing %}other{% else %}no{% endif %}`,
		"ifchanged":            `{% ifchanged value %}changed{% else %}same{% endifchanged %}`,
		"include":              `{% include "base" %}`,
		"load":                 `{% load tz %}`,
		"localtime":            `{% localtime on %}x{% endlocaltime %}`,
		"now":                  `{% now "Y" %}`,
		"partialdef":           `{% partialdef item inline %}x{% endpartialdef %}`,
		"querystring":          `{% querystring page=2 %}`,
		"regroup":              `{% regroup items by group as groups %}`,
		"resetcycle":           `{% cycle "one" "two" as named %}{% resetcycle named %}`,
		"spaceless":            `{% spaceless %}<b>x</b> <b>y</b>{% endspaceless %}`,
		"templatetag":          `{% templatetag openblock %}`,
		"timezone":             `{% timezone "UTC" %}x{% endtimezone %}`,
		"verbatim":             `{% verbatim %}{{ not_evaluated }}{% endverbatim %}`,
		"widthratio":           `{% widthratio 1 2 100 %}`,
		"with":                 `{% with x=value %}{{ x }}{% endwith %}`,
	}
	e := New(Config{Debug: true, LocaleResolver: templateLocale(t), Loaders: []Loader{MapLoader{"base": `{% block main %}base{% endblock %}`}}})
	d, err := e.Describe(4096)
	if err != nil || len(d.Tags) != len(fixtures) {
		t.Fatal("builtin inventory differs from public syntax fixtures", d, err)
	}
	for _, name := range d.Tags {
		source, ok := fixtures[name]
		if !ok {
			t.Fatal("builtin lacks executable syntax proof", name)
		}
		t.Run(name, func(t *testing.T) {
			_, err := e.RenderString(context.Background(), source, Context{"value": "sample", "csrf_token": "sample", "items": []any{Context{"group": "a"}}})
			if err != nil {
				t.Fatal("advertised tag is not usable", err)
			}
		})
	}
	wantFilters := make([]string, 0)
	for name := range builtinFilters() {
		wantFilters = append(wantFilters, name)
	}
	slices.Sort(wantFilters)
	if !slices.Equal(d.Filters, wantFilters) {
		t.Fatal("missing effective builtin filters", d.Filters, wantFilters)
	}
}

func TestTemplateDescriptionRefusesWithoutPartialInventory(t *testing.T) {
	for _, engine := range []*Engine{nil, {}, New(Config{Tags: map[string]Tag{"bad": nil}}), New(Config{Filters: map[string]Filter{"lower": nil}}), New(Config{Tags: map[string]Tag{strings.Repeat("x", 129): func(context.Context, Context, []any) (any, error) { return nil, nil }}})} {
		d, err := engine.Describe(4096)
		if err != ErrDescription || len(d.Tags) != 0 || len(d.Filters) != 0 {
			t.Fatal("partial/invalid inventory", d, err)
		}
	}
	e := New(Config{})
	for _, maximum := range []int{-1, 0, 1, 4097} {
		d, err := e.Describe(maximum)
		if err != ErrDescription || len(d.Tags) != 0 || len(d.Filters) != 0 {
			t.Fatal("invalid inventory bound", maximum, d, err)
		}
	}
	d, err := e.Describe(4096)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Describe(len(d.Tags) + len(d.Filters)); err != nil {
		t.Fatal("exact entry budget refused", err)
	}
	if _, err := e.Describe(len(d.Tags) + len(d.Filters) - 1); err != ErrDescription {
		t.Fatal("entry overflow accepted", err)
	}
}
