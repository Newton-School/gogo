package forms

import (
	"bytes"
	"fmt"
	"html/template"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"
)

// MultiWidget decomposes a value into independently escaped component widgets.
// Names use field_0, field_1, matching MultiValue and SplitDateTime binding.
type MultiWidget struct {
	Widgets    []Widget
	Decompress func(any) ([]any, error)
}

func (w MultiWidget) CloneWidget() Widget {
	copy := MultiWidget{Decompress: w.Decompress, Widgets: make([]Widget, len(w.Widgets))}
	for i, widget := range w.Widgets {
		copy.Widgets[i] = (Field{Widget: widget}).Clone().Widget
	}
	return copy
}
func (w MultiWidget) Render(b BoundField) (template.HTML, error) {
	fields := b.Field.Fields
	if b.Field.Kind == SplitDateTime && len(fields) == 0 {
		fields = []Field{NewField("date", Date), NewField("time", Time)}
	}
	if len(fields) == 0 {
		return "", fmt.Errorf("forms: composite widget requires component fields")
	}
	if len(w.Widgets) != 0 && len(w.Widgets) != len(fields) {
		return "", fmt.Errorf("forms: widget/component count mismatch")
	}
	var values []any
	if w.Decompress != nil {
		var err error
		values, err = w.Decompress(b.Value)
		if err != nil {
			return "", err
		}
	} else if date, ok := b.Value.(time.Time); ok && b.Field.Kind == SplitDateTime {
		values = []any{date, date}
	} else if b.Value != nil {
		v := reflect.ValueOf(b.Value)
		if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
			for i := 0; i < v.Len(); i++ {
				values = append(values, v.Index(i).Interface())
			}
		}
	}
	var children strings.Builder
	for i, field := range fields {
		field = field.Clone()
		field.Disabled = b.Field.Disabled
		field.Required = b.Field.Required && field.Required
		var value any
		if i < len(values) {
			value = values[i]
		}
		widget := field.Widget
		if len(w.Widgets) > 0 {
			widget = w.Widgets[i]
		}
		if widget == nil {
			widget = InputWidget{Type: defaultWidget(field.Kind)}
		}
		if input, ok := widget.(InputWidget); ok {
			input.Attrs = maps.Clone(input.Attrs)
			if input.Attrs == nil {
				input.Attrs = map[string]string{}
			}
			input.Attrs["aria-describedby"] = b.ID + "_help " + b.ID + "_errors"
			widget = input
		}
		part := BoundField{Field: field, Name: fmt.Sprintf("%s_%d", b.Name, i), ID: fmt.Sprintf("%s_%d", b.ID, i), Value: value, Errors: b.Errors}
		html, err := widget.Render(part)
		if err != nil {
			return "", err
		}
		label := field.Label
		if label == "" {
			label = field.Name
		}
		t := template.Must(template.New("component").Parse(`<label class="multi-label" for="{{.ID}}">{{.Label}}</label>{{.HTML}}`))
		if err = t.Execute(&children, map[string]any{"ID": part.ID, "Label": label, "HTML": html}); err != nil {
			return "", err
		}
	}
	t := template.Must(template.New("group").Parse(`<div id="{{.ID}}" class="multi-widget" role="group" aria-labelledby="{{.ID}}_label">{{.Children}}</div>`))
	var out bytes.Buffer
	err := t.Execute(&out, map[string]any{"ID": b.ID, "Children": template.HTML(children.String())})
	return template.HTML(out.String()), err
}

func (w InputWidget) renderChoices(b BoundField) (template.HTML, error) {
	multiple := w.Type == "checkbox-multiple"
	typ, role := "radio", "radiogroup"
	if multiple {
		typ, role = "checkbox", "group"
	}
	attrs := maps.Clone(w.Attrs)
	keys := []string{}
	for key := range attrs {
		if !safeAttribute(key) {
			return "", fmt.Errorf("forms: unsafe widget attribute %q", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var src strings.Builder
	src.WriteString(`<div id="{{.ID}}" class="choice-widget" role="` + role + `" aria-labelledby="{{.ID}}_label"{{if .Required}} aria-required="true"{{end}}{{if .Invalid}} aria-invalid="true"{{end}}>`)
	src.WriteString(`{{range .Choices}}<label for="{{.ID}}"><input type="` + typ + `" id="{{.ID}}" name="{{$.Name}}" value="{{.Value}}"{{if .Selected}} checked{{end}}{{if $.Disabled}} disabled{{end}}`)
	if !multiple {
		src.WriteString(`{{if $.Required}} required{{end}}`)
	}
	for i, key := range keys {
		src.WriteString(fmt.Sprintf(` %s="{{index $.Attrs %d}}"`, key, i))
	}
	src.WriteString(`> {{.Label}}</label>{{end}}</div>`)
	t, err := template.New("choices").Parse(src.String())
	if err != nil {
		return "", err
	}
	selected := map[string]bool{}
	for _, value := range stringValues(b.Value) {
		selected[value] = true
	}
	type option struct {
		ID, Value, Label string
		Selected         bool
	}
	choices := []option{}
	for i, choice := range b.Field.Choices {
		choices = append(choices, option{fmt.Sprintf("%s_%d", b.ID, i), choice.Value, choice.Label, selected[choice.Value]})
	}
	values := []string{}
	for _, key := range keys {
		values = append(values, attrs[key])
	}
	var out bytes.Buffer
	err = t.Execute(&out, map[string]any{"Name": b.Name, "ID": b.ID, "Choices": choices, "Required": b.Field.Required, "Disabled": b.Field.Disabled, "Invalid": len(b.Errors) > 0, "Attrs": values})
	return template.HTML(out.String()), err
}
