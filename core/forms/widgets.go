package forms

import (
	"bytes"
	"fmt"
	"html/template"
	"sort"
	"strings"
)

type Widget interface {
	Render(BoundField) (template.HTML, error)
}
type InputWidget struct {
	Type  string
	Attrs map[string]string
}
type BoundField struct {
	Field    Field
	Name, ID string
	Value    any
	Errors   ErrorList
}

func (f *Form) BoundField(name string) (BoundField, bool) {
	f.fullClean()
	for _, field := range f.fields {
		if field.Name == name {
			return BoundField{field.Clone(), f.name(name), "id_" + f.name(name), cloneValue(f.raw(field)), append(ErrorList(nil), f.errors[name]...)}, true
		}
	}
	return BoundField{}, false
}
func (b BoundField) Label() string {
	if b.Field.Label != "" {
		return b.Field.Label
	}
	return strings.ReplaceAll(b.Field.Name, "_", " ")
}
func (b BoundField) HTML() (template.HTML, error) {
	widget := b.Field.Widget
	if widget == nil {
		widget = InputWidget{Type: defaultWidget(b.Field.Kind)}
	}
	return widget.Render(b)
}

func (w InputWidget) Render(b BoundField) (template.HTML, error) {
	typ := w.Type
	if typ == "" {
		typ = "text"
	}
	allowed := map[string]bool{"text": true, "number": true, "email": true, "url": true, "password": true, "hidden": true, "file": true, "textarea": true, "date": true, "datetime-local": true, "time": true, "checkbox": true, "select": true, "select-multiple": true, "radio": true, "checkbox-multiple": true}
	if !allowed[typ] {
		return "", fmt.Errorf("forms: unsupported widget %q", typ)
	}
	var source strings.Builder
	source.WriteString(`<`)
	tag := "input"
	if typ == "textarea" {
		tag = "textarea"
	} else if typ == "select" || typ == "select-multiple" {
		tag = "select"
	}
	source.WriteString(tag + ` name="{{.Name}}" id="{{.ID}}"`)
	if tag == "input" {
		source.WriteString(` type="` + typ + `"`)
	}
	if b.Field.Required && typ != "hidden" {
		source.WriteString(` required`)
	}
	if b.Field.Disabled {
		source.WriteString(` disabled`)
	}
	if len(b.Errors) > 0 {
		source.WriteString(` aria-invalid="true"`)
	}
	if b.Field.HelpText != "" || len(b.Errors) > 0 {
		source.WriteString(` aria-describedby="{{.ID}}_help {{.ID}}_errors"`)
	}
	keys := make([]string, 0, len(w.Attrs))
	for k := range w.Attrs {
		if !safeAttribute(k) {
			return "", fmt.Errorf("forms: unsafe widget attribute %q", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		source.WriteString(fmt.Sprintf(` %s="{{index .Attrs %d}}"`, k, i))
	}
	if typ == "select-multiple" {
		source.WriteString(` multiple`)
	}
	if typ == "checkbox" {
		source.WriteString(` value="true"{{if .Checked}} checked{{end}}`)
	} else if tag == "input" && typ != "password" && typ != "file" {
		source.WriteString(` value="{{.Value}}"`)
	}
	source.WriteString(`>`)
	if tag == "textarea" {
		source.WriteString(`{{.Value}}</textarea>`)
	}
	if tag == "select" {
		source.WriteString(`{{range .Choices}}<option value="{{.Value}}"{{if .Selected}} selected{{end}}>{{.Label}}</option>{{end}}</select>`)
	}
	t, err := template.New("widget").Parse(source.String())
	if err != nil {
		return "", err
	}
	type option struct {
		Value, Label string
		Selected     bool
	}
	choices := []option{}
	selected := map[string]bool{}
	for _, v := range stringValues(b.Value) {
		selected[v] = true
	}
	for _, c := range b.Field.Choices {
		choices = append(choices, option{c.Value, c.Label, selected[c.Value]})
	}
	attrs := make([]string, 0, len(keys))
	for _, k := range keys {
		attrs = append(attrs, w.Attrs[k])
	}
	checked := !empty(b.Value) && stringValue(b.Value) != "false" && stringValue(b.Value) != "0"
	var out bytes.Buffer
	err = t.Execute(&out, map[string]any{"Name": b.Name, "ID": b.ID, "Value": stringValue(b.Value), "Attrs": attrs, "Choices": choices, "Checked": checked})
	return template.HTML(out.String()), err
}
func safeAttribute(s string) bool {
	if strings.HasPrefix(s, "data-") || strings.HasPrefix(s, "aria-") {
		return fieldName.MatchString(s)
	}
	switch s {
	case "class", "title", "placeholder", "autocomplete", "min", "max", "step", "minlength", "maxlength", "rows", "cols", "accept", "pattern", "inputmode":
		return true
	}
	return false
}
func defaultWidget(kind Kind) string {
	switch kind {
	case Boolean:
		return "checkbox"
	case Integer, Float, Decimal:
		return "number"
	case Email:
		return "email"
	case URL:
		return "url"
	case Date:
		return "date"
	case DateTime:
		return "datetime-local"
	case Time:
		return "time"
	case File, Image:
		return "file"
	case JSON:
		return "textarea"
	case ChoiceKind, TypedChoice, ModelChoice, NullBoolean:
		return "select"
	case MultipleChoice, TypedMultipleChoice, ModelMultipleChoice:
		return "select-multiple"
	}
	return "text"
}

// Render emits accessible fields. Submitted password values never appear in output.
func (f *Form) Render(layout string) (template.HTML, error) {
	if layout != "div" && layout != "p" && layout != "ul" && layout != "table" {
		return "", fmt.Errorf("forms: unsupported layout")
	}
	f.fullClean()
	var out bytes.Buffer
	summary := template.Must(template.New("summary").Parse(`{{if .}}<ul class="errorlist" role="alert">{{range .}}<li>{{.Message}}</li>{{end}}</ul>{{end}}`))
	if err := summary.Execute(&out, f.errors[NonFieldErrors]); err != nil {
		return "", err
	}
	if layout == "ul" || layout == "table" {
		out.WriteString("<" + layout + ">")
	}
	for _, field := range f.fields {
		bound, _ := f.BoundField(field.Name)
		widget, err := bound.HTML()
		if err != nil {
			return "", err
		}
		tag := layout
		if layout == "ul" {
			tag = "li"
		}
		if layout == "table" {
			tag = "tr"
		}
		src := `<` + tag + ` class="form-row">`
		if layout == "table" {
			src += `<th>`
		}
		src += `<label for="{{.ID}}">{{.Label}}</label>`
		if layout == "table" {
			src += `</th><td>`
		}
		src += `{{.Widget}}<div id="{{.ID}}_help" class="helptext">{{.Help}}</div><ul id="{{.ID}}_errors" class="errorlist">{{range .Errors}}<li>{{.Message}}</li>{{end}}</ul>`
		if layout == "table" {
			src += `</td>`
		}
		src += `</` + tag + `>`
		t, err := template.New("field").Parse(src)
		if err != nil {
			return "", err
		}
		if err = t.Execute(&out, map[string]any{"ID": bound.ID, "Label": bound.Label(), "Widget": widget, "Help": field.HelpText, "Errors": bound.Errors}); err != nil {
			return "", err
		}
	}
	if layout == "ul" || layout == "table" {
		out.WriteString("</" + layout + ">")
	}
	return template.HTML(out.String()), nil
}

type Media struct{ CSS, JS []string }

func (m Media) Merge(other Media) Media {
	merge := func(a, b []string) []string {
		seen := map[string]bool{}
		out := []string{}
		for _, x := range append(append([]string(nil), a...), b...) {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
		return out
	}
	return Media{merge(m.CSS, other.CSS), merge(m.JS, other.JS)}
}
