package admin

import (
	"encoding/json"
	"errors"
	"html/template"
	"maps"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
)

func clonePrepopulated(values map[string][]string) map[string][]string {
	if values == nil {
		return nil
	}
	result := make(map[string][]string, len(values))
	for name, sources := range values {
		result[name] = slices.Clone(sources)
	}
	return result
}

func prepopulationInput(options ModelAdmin, name string, target bool) (forms.Field, forms.InputWidget, error) {
	metadata, exists := options.Schema.Field(name)
	if !exists || !metadata.IsEditable() || metadata.PrimaryKey || slices.ContainsFunc(options.Schema.PKFields(), func(field models.Field) bool { return field.Name == name }) || !slices.Contains(options.Fields, name) || slices.Contains(options.Exclude, name) || slices.Contains(options.ReadonlyFields, name) || sensitiveField(options, name) || metadata.Relation != nil || len(metadata.Choices) > 0 || (target && metadata.Kind != models.Slug) || (!target && metadata.Kind != models.Char && metadata.Kind != models.Text && metadata.Kind != models.Slug) {
		return forms.Field{}, forms.InputWidget{}, errors.New("admin: prepopulation requires declared editable text sources and slug targets")
	}
	field, err := forms.FieldFromModel(metadata)
	if override, ok := options.FormOverrides[name]; ok {
		field, err = override.Clone(), nil
	}
	if err != nil {
		return field, forms.InputWidget{}, err
	}
	if field.Disabled || len(field.Choices) > 0 || field.Kind != forms.Char && field.Kind != forms.Slug {
		return field, forms.InputWidget{}, errors.New("admin: prepopulation requires an enabled text form field")
	}
	if metadata.MaxLength > 0 && (field.MaxLength == 0 || metadata.MaxLength < field.MaxLength) {
		field.MaxLength = metadata.MaxLength
	}
	widget := forms.InputWidget{Type: "text"}
	if field.Widget != nil {
		switch value := field.Widget.(type) {
		case forms.InputWidget:
			widget = value
		case *forms.InputWidget:
			if value == nil {
				return field, widget, errors.New("admin: invalid prepopulation widget")
			}
			widget = *value
		default:
			return field, widget, errors.New("admin: prepopulation requires a standard text widget")
		}
	}
	if widget.Type != "" && widget.Type != "text" && (target || widget.Type != "textarea") {
		return field, widget, errors.New("admin: prepopulation requires a standard text widget")
	}
	for name := range widget.Attrs {
		if name == "readonly" || name == "disabled" || strings.HasPrefix(name, "data-prepopulate-") {
			return field, widget, errors.New("admin: incompatible prepopulation widget attributes")
		}
	}
	return field, widget, nil
}

func validatePrepopulated(options ModelAdmin) error {
	if len(options.PrepopulatedFields) > 32 {
		return errors.New("admin: at most 32 prepopulated fields are supported")
	}
	for target, sources := range options.PrepopulatedFields {
		if _, _, err := prepopulationInput(options, target, true); err != nil {
			return err
		}
		if len(sources) == 0 || len(sources) > 16 {
			return errors.New("admin: prepopulation requires one to 16 ordered source fields")
		}
		seen := map[string]bool{}
		for _, source := range sources {
			_, generated := options.PrepopulatedFields[source]
			if source == target || seen[source] || generated {
				return errors.New("admin: prepopulation sources must be unique and cannot depend on another prepopulated field")
			}
			seen[source] = true
			if _, _, err := prepopulationInput(options, source, false); err != nil {
				return err
			}
		}
	}
	return nil
}

type prepopulatedWidget struct {
	Input        forms.InputWidget
	Sources      []string
	AllowUnicode bool
}

func (w prepopulatedWidget) CloneWidget() forms.Widget {
	w.Input.Attrs, w.Sources = maps.Clone(w.Input.Attrs), slices.Clone(w.Sources)
	return w
}
func (w prepopulatedWidget) Render(bound forms.BoundField) (template.HTML, error) {
	attrs := maps.Clone(w.Input.Attrs)
	if attrs == nil {
		attrs = map[string]string{}
	}
	prefix := strings.TrimSuffix(bound.ID, bound.Field.Name)
	ids := make([]string, len(w.Sources))
	for index, source := range w.Sources {
		ids[index] = prefix + source
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return "", err
	}
	attrs["data-prepopulate-from"] = string(encoded)
	limit, _ := json.Marshal(bound.Field.MaxLength)
	attrs["data-prepopulate-maxlength"] = string(limit)
	allowUnicode, _ := json.Marshal(w.AllowUnicode)
	attrs["data-prepopulate-unicode"] = string(allowUnicode)
	input := w.Input
	input.Attrs = attrs
	return input.Render(bound)
}

func prepopulatedOverrides(options ModelAdmin, object Object, readonly []string, overrides map[string]forms.Field) (map[string]forms.Field, error) {
	// Saved objects are never attached to the browser suggestion engine, even
	// when their target is blank. Dynamic read-only sources cannot leak into it.
	if object.ID != "" || object.Record == nil || object.Record.State().Persisted {
		return overrides, nil
	}
	for target, sources := range options.PrepopulatedFields {
		if slices.Contains(readonly, target) || slices.ContainsFunc(sources, func(name string) bool { return slices.Contains(readonly, name) }) {
			continue
		}
		field, widget, err := prepopulationInput(options, target, true)
		if err != nil {
			return nil, err
		}
		metadata, _ := options.Schema.Field(target)
		field.Widget = prepopulatedWidget{Input: widget, Sources: slices.Clone(sources), AllowUnicode: metadata.AllowUnicode && (field.Kind == forms.Char || field.AllowUnicode)}
		if overrides == nil {
			overrides = map[string]forms.Field{}
		}
		overrides[target] = field
	}
	return overrides, nil
}
