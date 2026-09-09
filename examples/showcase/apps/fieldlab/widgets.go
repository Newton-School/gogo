package fieldlab

import (
	"html/template"
	"time"

	"github.com/Newton-School/gogo/core/forms"
)

type WidgetCase struct {
	Name   string
	Field  forms.Field
	Widget forms.Widget
	Value  any
}

// WidgetCases covers every InputWidget type accepted by this release and both
// visible and hidden composite arrangements using the public MultiWidget.
func WidgetCases() []WidgetCase {
	types := []string{"text", "number", "email", "url", "password", "hidden", "multiple-hidden", "file", "textarea", "date", "datetime-local", "time", "checkbox", "select", "select-multiple", "radio", "checkbox-multiple"}
	result := make([]WidgetCase, 0, len(types)+4)
	for _, kind := range types {
		field := forms.NewField("widget_"+kind, forms.Char)
		field.Choices = []forms.Choice{{Value: "1", Label: "One"}, {Value: "2", Label: "Two"}}
		field.HelpText = "An escaped, explicit widget declaration."
		var value any = "Example"
		switch kind {
		case "number":
			field.Kind, value = forms.Integer, 42
		case "email":
			field.Kind, value = forms.Email, "developer@example.com"
		case "url":
			field.Kind, value = forms.URL, "https://example.com"
		case "date", "datetime-local", "time":
			value = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
		case "checkbox":
			field.Kind, value = forms.Boolean, true
		case "select", "radio":
			field.Kind, value = forms.ChoiceKind, "1"
		case "select-multiple", "checkbox-multiple", "multiple-hidden":
			field.Kind, value = forms.MultipleChoice, []string{"1", "2"}
		case "file":
			field.Kind, value = forms.File, nil
		}
		result = append(result, WidgetCase{kind, field, forms.InputWidget{Type: kind}, value})
	}
	null := forms.NewField("widget_null_boolean", forms.NullBoolean)
	null.Required = false
	result = append(result, WidgetCase{"null-boolean-select", null, forms.InputWidget{Type: "select"}, "false"})
	pair := forms.NewField("widget_pair", forms.MultiValue)
	pair.Fields = []forms.Field{forms.NewField("first", forms.Integer), forms.NewField("second", forms.Integer)}
	result = append(result, WidgetCase{"multi-widget", pair, forms.MultiWidget{}, []any{1, 2}})
	split := forms.NewField("widget_split", forms.SplitDateTime)
	hiddenSplit := forms.NewField("widget_split_hidden", forms.SplitDateTime)
	instant := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	result = append(result,
		WidgetCase{"split-datetime", split, forms.MultiWidget{}, instant},
		WidgetCase{"split-hidden-datetime", hiddenSplit, forms.MultiWidget{Widgets: []forms.Widget{forms.InputWidget{Type: "hidden"}, forms.InputWidget{Type: "hidden"}}}, instant},
	)
	return result
}

// RenderWidgets is read-only UI output from framework renderers. Caller must
// place it in a trusted HTML template; user strings are never promoted to HTML.
func RenderWidgets() (template.HTML, error) {
	fields := make([]forms.Field, 0, len(WidgetCases()))
	for _, example := range WidgetCases() {
		field := example.Field
		field.Widget, field.Initial, field.Label = example.Widget, example.Value, example.Name
		// Widget gallery displays declarations; it is not a second submission form.
		field.Required = false
		fields = append(fields, field)
	}
	form, err := forms.New(fields)
	if err != nil {
		return "", err
	}
	return form.Render("div")
}
