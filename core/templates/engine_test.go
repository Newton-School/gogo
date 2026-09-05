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
