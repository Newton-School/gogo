package forms

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

func TestBoundCleaningAndReadonlyInitial(t *testing.T) {
	calls := 0
	name := NewField("name", Char)
	name.Validators = []Validator{func(context.Context, any) error { calls++; return nil }}
	role := NewField("role", Char)
	role.Disabled = true
	role.Initial = "member"
	data := url.Values{"name": {"Alice"}, "role": {"admin"}, "unexpected": {"ignored"}}
	f, err := New([]Field{name, role}, WithData(data))
	if err != nil {
		t.Fatal(err)
	}
	data.Set("name", "Mutated")
	if !f.IsValid() || !f.IsValid() {
		t.Fatal(f.Errors())
	}
	if calls != 1 {
		t.Fatalf("clean ran %d times", calls)
	}
	if f.CleanedData()["role"] != "member" || f.CleanedData()["name"] != "Alice" {
		t.Fatal(f.CleanedData())
	}
	if _, ok := f.CleanedData()["unexpected"]; ok {
		t.Fatal("mass assignment")
	}
}
func TestUnboundAndEmptyBound(t *testing.T) {
	f, _ := New([]Field{NewField("name", Char)})
	if f.IsBound() || f.IsValid() || len(f.Errors()) != 0 {
		t.Fatal("unbound semantics")
	}
	f, _ = New([]Field{NewField("name", Char)}, WithData(url.Values{}))
	if !f.IsBound() || f.IsValid() || !f.HasError("name", "required") {
		t.Fatal(f.Errors())
	}
}
func TestFormCleanRunsDespiteFieldErrors(t *testing.T) {
	ran := false
	f, _ := New([]Field{NewField("number", Integer)}, WithData(url.Values{"number": {"not-a-number"}}), WithClean(func(f *Form) error { ran = true; f.AddError("", Error{"cross", "Cross field error"}); return nil }))
	if f.IsValid() || !ran || len(f.Errors()) != 2 {
		t.Fatal(f.Errors())
	}
}
func TestErrorsRemoveCleanedData(t *testing.T) {
	f, _ := New([]Field{NewField("name", Char)}, WithData(url.Values{"name": {"ok"}}))
	if !f.IsValid() {
		t.Fatal(f.Errors())
	}
	f.AddError("name", Error{"duplicate", "Already exists"})
	if _, ok := f.CleanedData()["name"]; ok {
		t.Fatal("errored field remained cleaned")
	}
}
func TestWidgetsEscapeAndNeverRedisplayPassword(t *testing.T) {
	field := NewField("password", Char)
	field.Widget = InputWidget{Type: "password"}
	f, _ := New([]Field{field}, WithData(url.Values{"password": {"<script>secret</script>"}}))
	out, err := f.Render("div")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "secret") {
		t.Fatal("password leaked")
	}
	field.Widget = InputWidget{Type: "text", Attrs: map[string]string{"onfocus": "alert(1)"}}
	f, _ = New([]Field{field})
	if _, err := f.Render("div"); err == nil {
		t.Fatal("event attribute accepted")
	}
}
func TestFieldKinds(t *testing.T) {
	cases := []struct {
		kind      Kind
		good, bad string
	}{{Integer, "0", "1.5"}, {Float, "1.2", "NaN"}, {Email, "a@example.com", "A <a@example.com>"}, {URL, "https://example.com", "javascript:alert(1)"}, {UUID, "12345678-1234-1234-1234-123456789abc", "bad"}, {Slug, "hello-world", "has space"}, {IP, "::1", "300.1.1.1"}, {Date, "2026-02-03", "2026-02-31"}, {DateTime, "2026-02-03T10:00:00Z", "bad"}, {Time, "10:00", "25:00"}, {Duration, "1h", "forever"}, {JSON, `{"a":1}`, `{} trailing`}}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			field := NewField("value", tc.kind)
			for _, input := range []struct {
				value string
				want  bool
			}{{tc.good, true}, {tc.bad, false}} {
				f, _ := New([]Field{field}, WithData(url.Values{"value": {input.value}}))
				if f.IsValid() != input.want {
					t.Fatalf("%q: %v", input.value, f.Errors())
				}
			}
		})
	}
}
