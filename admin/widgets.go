package admin

import (
	"bytes"
	"context"
	"html/template"
	"net/url"
	"strings"

	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
)

// relationWidget retains an ordinary validated ID input without JavaScript.
// Native datalist suggestions add keyboard-accessible asynchronous lookup; only
// the server's scoped ModelForm resolver authorizes the submitted ID.
type relationWidget struct{ URL string }

type multipleRelationWidget struct{ URL string }

func (w multipleRelationWidget) CloneWidget() forms.Widget { return w }
func (w multipleRelationWidget) Render(bound forms.BoundField) (template.HTML, error) {
	selectHTML, err := (forms.InputWidget{Type: "select-multiple", Attrs: map[string]string{"size": "6", "aria-describedby": bound.ID + "_lookup_status " + bound.ID + "_help " + bound.ID + "_errors"}}).Render(bound)
	if err != nil {
		return "", err
	}
	t := template.Must(template.New("many-relation").Parse(`<div class="relation-chooser"><label for="{{.ID}}_search">Search related objects</label><input type="search" id="{{.ID}}_search" data-relation-url="{{.URL}}" data-choice-target="{{.ID}}" autocomplete="off" aria-describedby="{{.ID}}_search_lookup_status"><p class="helptext" id="{{.ID}}_search_lookup_status" role="status" aria-live="polite">Search to load choices. Existing selections remain available.</p>{{.Select}}<p class="helptext" id="{{.ID}}_lookup_status">Select one or more related objects. Use Command or Control to select multiple items.</p></div>`))
	var out bytes.Buffer
	err = t.Execute(&out, struct {
		ID, URL string
		Select  template.HTML
	}{bound.ID, w.URL, selectHTML})
	return template.HTML(out.String()), err
}

func (w relationWidget) CloneWidget() forms.Widget { return w }
func (w relationWidget) Render(bound forms.BoundField) (template.HTML, error) {
	input, err := (forms.InputWidget{Type: "text", Attrs: map[string]string{
		"list": bound.ID + "_choices", "data-relation-url": w.URL,
		"autocomplete": "off", "aria-describedby": bound.ID + "_lookup_status " + bound.ID + "_help " + bound.ID + "_errors",
	}}).Render(bound)
	if err != nil {
		return "", err
	}
	t := template.Must(template.New("relation").Parse(`{{.Input}}<datalist id="{{.ID}}_choices"></datalist><p class="helptext" id="{{.ID}}_lookup_status" role="status" aria-live="polite">Type to search, or enter a related object ID.</p>`))
	var out bytes.Buffer
	err = t.Execute(&out, struct {
		Input template.HTML
		ID    string
	}{input, bound.ID})
	return template.HTML(out.String()), err
}

func (s *Site) relationOverrides(_ context.Context, options ModelAdmin, object Object) (map[string]forms.Field, error) {
	overrides := cloneOverrides(options.FormOverrides)
	if overrides == nil {
		overrides = map[string]forms.Field{}
	}
	for _, name := range append(append([]string(nil), options.AutocompleteFields...), options.RawIDFields...) {
		metadata, _ := options.Schema.Field(name)
		field, err := forms.FieldFromModel(metadata)
		if override, ok := overrides[name]; ok {
			field, err = override, nil
		}
		if err != nil {
			return nil, err
		}
		field.Widget = forms.InputWidget{Type: "text"}
		for _, configured := range options.AutocompleteFields {
			if configured != name {
				continue
			}
			values := url.Values{"app_label": {options.Schema.AppLabel}, "model_name": {strings.ToLower(options.Schema.Name)}, "field_name": {name}}
			if object.ID != "" {
				values.Set("object_id", object.ID)
			}
			endpoint := s.config.Prefix + "autocomplete/?" + values.Encode()
			field.Widget = relationWidget{URL: endpoint}
			if metadata.Kind == models.ManyToMany {
				field.Widget = multipleRelationWidget{URL: endpoint}
			}
		}
		overrides[name] = field
	}
	return overrides, nil
}
