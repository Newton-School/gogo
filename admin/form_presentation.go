package admin

import (
	"context"
	"html/template"
	"maps"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/templates"
)

// Rendering operates on cloned BoundFields. Cleaning, names, disabled state,
// custom widgets and authorization remain owned by the original form.
func adminWidgetHTML(bound forms.BoundField) (template.HTML, error) {
	return adminWidgetWithDescription(bound, "")
}

type adminComponentWidget struct {
	inner       forms.Widget
	description string
}

func (w adminComponentWidget) Render(bound forms.BoundField) (template.HTML, error) {
	bound.Field.Widget = w.inner
	return adminWidgetWithDescription(bound, w.description)
}

func adminWidgetWithDescription(bound forms.BoundField, description string) (template.HTML, error) {
	bound.Field = bound.Field.Clone()
	widget := bound.Field.Widget
	if widget == nil {
		kind := map[forms.Kind]string{
			forms.Boolean: "checkbox", forms.Integer: "number", forms.Float: "number", forms.Decimal: "number",
			forms.Email: "email", forms.URL: "url", forms.Date: "date", forms.Time: "time", forms.DateTime: "datetime-local",
			forms.File: "file", forms.Image: "file", forms.JSON: "textarea", forms.NullBoolean: "select",
			forms.ChoiceKind: "select", forms.TypedChoice: "select", forms.ModelChoice: "select",
			forms.MultipleChoice: "select-multiple", forms.TypedMultipleChoice: "select-multiple", forms.ModelMultipleChoice: "select-multiple",
		}[bound.Field.Kind]
		if bound.Field.Kind == forms.MultiValue || bound.Field.Kind == forms.SplitDateTime {
			widget = forms.MultiWidget{}
		} else {
			if kind == "" {
				kind = "text"
			}
			widget = forms.InputWidget{Type: kind}
		}
	}
	if multi, ok := widget.(*forms.MultiWidget); ok && multi != nil {
		widget = *multi
	}
	if multi, ok := widget.(forms.MultiWidget); ok {
		fields := bound.Field.Fields
		if bound.Field.Kind == forms.SplitDateTime && len(fields) == 0 {
			fields = []forms.Field{forms.NewField("date", forms.Date), forms.NewField("time", forms.Time)}
		}
		if len(multi.Widgets) != 0 && len(multi.Widgets) != len(fields) {
			return multi.Render(bound)
		}
		children := make([]forms.Widget, len(fields))
		for i, field := range fields {
			inner := field.Widget
			if len(multi.Widgets) > 0 {
				inner = multi.Widgets[i]
			}
			children[i] = adminComponentWidget{inner: inner, description: bound.ID + "_help " + bound.ID + "_errors"}
		}
		multi.Widgets = children
		return multi.Render(bound)
	}
	var input forms.InputWidget
	switch value := widget.(type) {
	case forms.InputWidget:
		input = value
	case *forms.InputWidget:
		if value == nil {
			return bound.HTML()
		}
		input = *value
	default:
		return bound.HTML()
	}
	input.Attrs = maps.Clone(input.Attrs)
	if input.Attrs == nil {
		input.Attrs = map[string]string{}
	}
	if description != "" && input.Attrs["aria-describedby"] == "" {
		input.Attrs["aria-describedby"] = description
	}
	class := map[string]string{"text": "vTextField", "email": "vTextField", "url": "vURLField", "number": "vIntegerField", "textarea": "vLargeTextField", "date": "vDateField", "time": "vTimeField", "datetime-local": "vDateTimeField", "file": "gogo-file", "radio": "radiolist"}[input.Type]
	// Django's calendar/clock use ISO text values. The same Go cleaner handles
	// these values; no date parsing or timezone interpretation happens here.
	if (input.Type == "date" || input.Type == "time") && !bound.Field.Disabled {
		layout := "2006-01-02"
		if input.Type == "time" {
			layout = "15:04:05.999999999"
		}
		if value, ok := bound.Value.(time.Time); ok {
			bound.Value = value.Format(layout)
		}
		input.Type = "text"
	}
	input.Attrs["class"] = strings.TrimSpace(input.Attrs["class"] + " " + class)
	bound.Field.Widget = input
	return bound.HTML()
}

func adminFieldRow(bound forms.BoundField) (templates.Context, error) {
	widget, err := adminWidgetHTML(bound)
	checkbox := bound.Field.Kind == forms.Boolean && bound.Field.Widget == nil
	switch input := bound.Field.Widget.(type) {
	case forms.InputWidget:
		checkbox = input.Type == "checkbox"
	case *forms.InputWidget:
		checkbox = input != nil && input.Type == "checkbox"
	}
	return templates.Context{"name": bound.Field.Name, "label": bound.Label(), "id": bound.ID,
		"widget": widget, "help": bound.Field.HelpText, "errors": bound.Errors,
		"grouped": bound.Grouped(), "required": bound.Field.Required, "checkbox": checkbox, "hidden": bound.Hidden()}, err
}

func (s *Site) renderAdminForm(ctx context.Context, form *forms.Form) (template.HTML, error) {
	rows := []any{}
	errors := append(forms.ErrorList(nil), form.Errors()[forms.NonFieldErrors]...)
	for _, field := range form.Fields() {
		bound, _ := form.BoundField(field.Name)
		row, err := adminFieldRow(bound)
		if err != nil {
			return "", err
		}
		if bound.Hidden() {
			for _, problem := range bound.Errors {
				errors = append(errors, forms.Error{Code: problem.Code, Message: bound.Label() + ": " + problem.Message})
			}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 && len(errors) == 0 {
		return "", nil
	}
	body, err := s.engine.Render(ctx, "fieldsets.html", templates.Context{"nonfield_errors": errors, "fieldsets": []any{templates.Context{"rows": rows}}})
	return template.HTML(body), err
}
