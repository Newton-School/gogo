package templates

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

func TestInheritanceLoopsIncludesAndEscaping(t *testing.T) {
	engine := New(Config{Loaders: []Loader{MapLoader{"base.html": `<title>{% block title %}Base{% endblock %}</title><main>{% block content %}Empty{% endblock %}</main>`, "child.html": `{% extends "base.html" %}{% block title %}{{ title }}{% endblock %}{% block content %}{% for row in rows %}{% include "row.html" with row=row only %}{% empty %}None{% endfor %}{% endblock %}`, "row.html": `<p>{{ row|upper }}</p>`}}})
	out, err := engine.Render(context.Background(), "child.html", Context{"title": "<script>alert(1)</script>", "rows": []string{"one", "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "<script>") || !strings.Contains(out, "<p>ONE</p><p>TWO</p>") {
		t.Fatal(out)
	}
}
func TestContextualEscaping(t *testing.T) {
	engine := New(Config{})
	out, err := engine.RenderString(context.Background(), `<a href="{{ url }}">{{ label }}</a><script>const value={{ value }};</script>`, Context{"url": "javascript:alert(1)", "label": "<img src=x onerror=alert(1)>", "value": "</script><script>alert(1)</script>"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "#ZgotmplZ") || strings.Contains(out, "<img") || strings.Contains(out, "</script><script>") {
		t.Fatal(out)
	}
}
func TestTraversalAndUnknownTags(t *testing.T) {
	engine := New(Config{Loaders: []Loader{FSLoader{fstest.MapFS{"safe.html": {Data: []byte("safe")}}}}})
	if _, err := engine.Render(context.Background(), "../safe.html", nil); err == nil {
		t.Fatal("path traversal")
	}
	if _, err := engine.RenderString(context.Background(), `{% execute "shell" %}`, nil); err == nil {
		t.Fatal("unregistered tag")
	}
}
func TestNoTemplateMethodCalls(t *testing.T) {
	engine := New(Config{Strict: true})
	if _, err := engine.RenderString(context.Background(), `{{ value.String }}`, Context{"value": methodValue{}}); err == nil {
		t.Fatal("method access allowed")
	}
}

func TestElifAndDirectValuesNeverCallMethods(t *testing.T) {
	engine := New(Config{})
	out, err := engine.RenderString(context.Background(), `{% if False %}a{% elif True %}b{% else %}c{% endif %}tail`, nil)
	if err != nil || out != "btail" {
		t.Fatal(out, err)
	}
	out, err = engine.RenderString(context.Background(), `{{ value }}`, Context{"value": methodValue{}})
	if err != nil || strings.Contains(out, "secret") {
		t.Fatal(out, err)
	}
}

type methodValue struct{}

func (methodValue) String() string { return "secret" }
func TestSafeFilterRequiresTrustedValue(t *testing.T) {
	engine := New(Config{})
	if _, err := engine.RenderString(context.Background(), `{{ value|safe }}`, Context{"value": "<b>untrusted</b>"}); err == nil {
		t.Fatal("untrusted safe")
	}
	out, err := engine.RenderString(context.Background(), `{{ value|safe }}`, Context{"value": SafeHTML("<b>trusted</b>")})
	if err != nil || out != "<b>trusted</b>" {
		t.Fatal(out, err)
	}
}
func TestDepthAndLoopBounds(t *testing.T) {
	engine := New(Config{MaxDepth: 4, Loaders: []Loader{MapLoader{"loop": `{% include "loop" %}`}}})
	if _, err := engine.Render(context.Background(), "loop", nil); err == nil {
		t.Fatal("recursive template")
	}
	engine = New(Config{MaxIterations: 2})
	if _, err := engine.RenderString(context.Background(), `{% for v in values %}{{ v }}{% endfor %}`, Context{"values": []int{1, 2, 3}}); err == nil {
		t.Fatal("iteration bound")
	}
}
func TestConditionAndContextIsolation(t *testing.T) {
	engine := New(Config{})
	data := Context{"flag": true, "name": "base"}
	out, err := engine.RenderString(context.Background(), `{% if flag and name == "base" %}{% with name="local" %}{{ name }}{% endwith %}{% else %}bad{% endif %}{{ name }}`, data)
	if err != nil || out != "localbase" || data["name"] != "base" {
		t.Fatal(out, err, data)
	}
}

func TestCommentsSkipInvalidTemplateSyntaxAndNamedVerbatim(t *testing.T) {
	engine := New(Config{})
	out, err := engine.RenderString(context.Background(), `a{% comment "explanation" %}{{ broken {% nonsense {% endcomment %}b{%verbatim literal%}{{ raw }}{% endverbatim %}{% endverbatim literal %}c`, nil)
	if err != nil || out != `ab{{ raw }}{% endverbatim %}c` {
		t.Fatal(out, err)
	}
}

func TestNumericConditionsDoNotLoseIntegerPrecision(t *testing.T) {
	engine := New(Config{})
	out, err := engine.RenderString(context.Background(), `{% for item in items %}{% if forloop.counter == 1 %}first{% endif %}{% endfor %}{% if high > low %}greater{% endif %}{% if high == low %}wrong{% endif %}`, Context{"items": []string{"a", "b"}, "high": int64(9007199254740993), "low": int64(9007199254740992)})
	if err != nil || out != "firstgreater" {
		t.Fatal(out, err)
	}
}

func TestExtensionResultsNeverInvokeApplicationMethods(t *testing.T) {
	engine := New(Config{Tags: map[string]Tag{"opaque": func(context.Context, Context, []any) (any, error) { return methodValue{}, nil }}, Filters: map[string]Filter{"opaque": func(context.Context, any, any) (any, error) { return methodValue{}, nil }}})
	out, err := engine.RenderString(context.Background(), `{% opaque %}{{ value|opaque|upper }}{% opaque as object %}{{ object }}`, Context{"value": "safe"})
	if err != nil || strings.Contains(out, "secret") {
		t.Fatal(out, err)
	}
}

func TestFinalOutputHasBoundIncludingDynamicValues(t *testing.T) {
	engine := New(Config{})
	_, err := engine.RenderString(context.Background(), `{% for item in items %}{{ value }}{% endfor %}`, Context{"items": []int{1, 2, 3, 4, 5}, "value": strings.Repeat("x", 4<<20)})
	if err == nil {
		t.Fatal("final output exceeded limit through small placeholders")
	}
}

func TestStatefulControlTags(t *testing.T) {
	engine := New(Config{})
	cases := []struct{ source, want string }{
		{`{% for value in values %}{% cycle "odd" "even" %}:{% ifchanged value %}{{ value }}{% else %}-{% endifchanged %};{% endfor %}`, "odd:a;even:-;odd:b;"},
		{`{% cycle "red" "blue" as color silent %}{{ color }}|{% cycle color %}{{ color }}|{% resetcycle color %}{% cycle color %}{{ color }}`, "red|blue|red"},
		{`{% for value in values %}{% ifchanged %}{{ value }}{% else %}-{% endifchanged %}{% endfor %}`, "a-b"},
		{`{% for outer in values %}{% for inner in pair %}{% ifchanged inner %}{{ inner }}{% else %}-{% endifchanged %}{% endfor %};{% endfor %}`, "x-;x-;x-;"},
		{`{% filter upper %}hello {{ name }}{% endfilter %}`, "HELLO WORLD"},
	}
	for _, test := range cases {
		out, err := engine.RenderString(context.Background(), test.source, Context{"values": []string{"a", "a", "b"}, "pair": []string{"x", "x"}, "name": "world"})
		if err != nil || out != test.want {
			t.Errorf("%s: got %q want %q (%v)", test.source, out, test.want, err)
		}
	}
	out, err := engine.RenderString(context.Background(), `{% regroup people by team as groups %}{% for group in groups %}{{ group.grouper }}:{{ group.list|length }};{% endfor %}`, Context{"people": []Context{{"team": "A"}, {"team": "A"}, {"team": "B"}, {"team": "A"}}})
	if err != nil || out != "A:2;B:1;A:1;" {
		t.Fatal(out, err)
	}
}
