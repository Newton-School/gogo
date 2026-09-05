package forms

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestChoiceGroupWidgetsBindValidateAndEscape(t *testing.T) {
	for _, kind := range []Kind{ChoiceKind, MultipleChoice} {
		field := NewField("color", kind)
		field.Choices = []Choice{{"red", "<script>Red</script>"}, {"blue", "Blue"}}
		typ := "radio"
		if kind == MultipleChoice {
			typ = "checkbox-multiple"
		}
		field.Widget = InputWidget{Type: typ}
		form, err := New([]Field{field}, WithData(url.Values{"color": {"red"}}))
		if err != nil {
			t.Fatal(err)
		}
		if !form.IsValid() {
			t.Fatal(form.Errors())
		}
		out, err := form.Render("div")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(out), "type=\""+strings.TrimSuffix(typ, "-multiple")+"\"") != 2 || strings.Contains(string(out), "<script>") || !strings.Contains(string(out), `value="red" checked`) {
			t.Fatal(out)
		}
		if !strings.Contains(string(out), `aria-labelledby="id_color_label"`) || !strings.Contains(string(out), `id="id_color_label"`) {
			t.Fatal("group label missing", out)
		}
	}
}

func TestSplitDateTimeWidgetAndOptionalEmptyValue(t *testing.T) {
	field := NewField("when", SplitDateTime)
	initial := time.Date(2026, 9, 5, 14, 30, 0, 0, time.UTC)
	form, _ := New([]Field{field}, WithInitial(map[string]any{"when": initial}))
	out, err := form.Render("div")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `name="when_0"`) || !strings.Contains(string(out), `value="2026-09-05"`) || !strings.Contains(string(out), `name="when_1"`) || !strings.Contains(string(out), `value="14:30:00"`) {
		t.Fatal(out)
	}
	bound, _ := New([]Field{field}, WithData(url.Values{"when_0": {"2026-09-05"}, "when_1": {"14:30"}}))
	if !bound.IsValid() || !bound.CleanedData()["when"].(time.Time).Equal(initial) {
		t.Fatal(bound.Errors(), bound.CleanedData())
	}
	field.Required = false
	empty, _ := New([]Field{field}, WithData(url.Values{"when_0": {""}, "when_1": {""}}))
	if !empty.IsValid() || empty.CleanedData()["when"] != nil {
		t.Fatal(empty.Errors(), empty.CleanedData())
	}
	partial, _ := New([]Field{field}, WithData(url.Values{"when_0": {"2026-09-05"}, "when_1": {""}}))
	if partial.IsValid() {
		t.Fatal("partial composite accepted")
	}
}

func TestInitialJSONDateTimeAndNullBooleanRenderUsableValues(t *testing.T) {
	fields := []Field{NewField("data", JSON), NewField("when", DateTime), NewField("enabled", NullBoolean)}
	form, _ := New(fields, WithInitial(map[string]any{"data": map[string]any{"safe": "<tag>"}, "when": time.Date(2026, 9, 5, 14, 30, 0, 0, time.UTC)}))
	out, err := form.Render("div")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `value="2026-09-05T14:30:00"`) || !strings.Contains(string(out), `<option value="true"`) || strings.Contains(string(out), "map[") {
		t.Fatal(out)
	}
	field := NewField("data", JSON)
	field.Disabled = true
	form, _ = New([]Field{field}, WithInitial(map[string]any{"data": map[string]any{"key": "value"}}), WithData(url.Values{"data": {"forged"}}))
	if !form.IsValid() || form.CleanedData()["data"].(map[string]any)["key"] != "value" {
		t.Fatal(form.Errors())
	}
}

func TestMultipleHiddenPreservesValuesAndHiddenErrorsHaveSummary(t *testing.T) {
	field := NewField("choices", MultipleChoice)
	field.Choices = []Choice{{"a", "A"}, {"b", "B"}}
	field.Widget = InputWidget{Type: "multiple-hidden"}
	form, _ := New([]Field{field}, WithData(url.Values{"choices": {"a", "b"}}))
	if !form.IsValid() {
		t.Fatal(form.Errors())
	}
	out, err := form.Render("div")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), `type="hidden"`) != 2 || strings.Contains(string(out), `<label`) {
		t.Fatal(out)
	}
	form, _ = New([]Field{field}, WithData(url.Values{"choices": {"foreign"}}))
	out, err = form.Render("div")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `role="alert"`) || !strings.Contains(string(out), "choices: Select a valid choice.") {
		t.Fatal(out)
	}
}
