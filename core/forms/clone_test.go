package forms

import (
	"context"
	"net/url"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestFormClonesDeclarationAndStructuredInitial(t *testing.T) {
	choices := []Choice{{Value: "one", Label: "One"}}
	attrs := map[string]string{"class": "original"}
	messages := map[string]string{"required": "Original required"}
	field := NewField("choice", ChoiceKind)
	field.Choices = choices
	field.Widget = InputWidget{Type: "select", Attrs: attrs}
	field.ErrorMessages = messages
	f, err := New([]Field{field}, WithData(url.Values{"choice": {"one"}}))
	if err != nil {
		t.Fatal(err)
	}
	choices[0].Value = "changed"
	attrs["class"] = "changed"
	messages["required"] = "changed"
	if !f.IsValid() {
		t.Fatal("declaration mutation affected bound form", f.Errors())
	}
	copy := f.Fields()
	copy[0].Choices[0].Value = "copy mutation"
	copy[0].Widget.(InputWidget).Attrs["class"] = "copy mutation"
	bound, _ := f.BoundField("choice")
	if bound.Field.Choices[0].Value != "one" || bound.Field.Widget.(InputWidget).Attrs["class"] != "original" {
		t.Fatal("public field view aliased declaration")
	}
	initial := map[string]any{"data": map[string]any{"items": []any{"original"}}}
	jsonField := NewField("data", JSON)
	jsonField.Disabled = true
	jsonForm, _ := New([]Field{jsonField}, WithData(url.Values{}), WithInitial(initial))
	initial["data"].(map[string]any)["items"].([]any)[0] = "changed"
	jsonBound, _ := jsonForm.BoundField("data")
	if jsonBound.Value.(map[string]any)["items"].([]any)[0] != "original" {
		t.Fatal("nested initial mutation leaked")
	}
}

func TestChoiceModelValidationPreservesRequestContextAndRunsOnce(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "actor")
	calls := 0
	schema := models.Schema{AppLabel: "test", Name: "Choice", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("level", models.WithChoices(models.Choice{Value: int64(1), Label: "One"}), models.WithValidators(func(got context.Context, value any) error {
		calls++
		if got.Value(key{}) != "actor" {
			t.Error("request context lost")
		}
		if value != int64(1) {
			t.Errorf("choice type lost: %T", value)
		}
		return nil
	}))}}
	record, err := models.NewRecord(schema)
	if err != nil {
		t.Fatal(err)
	}
	if err = record.Set("level", int64(1)); err != nil {
		t.Fatal(err)
	}
	form, err := NewModelForm(ctx, record, ModelFormOptions{Fields: []string{"level"}}, WithData(url.Values{"level": {"1"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !form.IsValid() || calls != 1 {
		t.Fatal(form.Errors(), calls)
	}
	if !form.IsValid() || calls != 1 {
		t.Fatal("cached validation reran validators", calls)
	}
}
