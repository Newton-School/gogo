package admin

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cybersaksham/gogo/forms"
	"github.com/cybersaksham/gogo/models"
)

type adminRowModel struct {
	meta   models.Metadata
	values map[string]any
}

func (m *adminRowModel) ModelMeta() models.Metadata {
	if m == nil {
		return models.Metadata{}
	}
	return m.meta.Clone()
}

func (m *adminRowModel) ModelValue(name string) (any, bool) {
	if m == nil || m.values == nil {
		return nil, false
	}
	value, ok := m.values[name]
	return value, ok
}

func (m *adminRowModel) SetModelValue(name string, value any) error {
	if m.values == nil {
		m.values = map[string]any{}
	}
	m.values[name] = value
	return nil
}

func validateAdminModelForm(ctx context.Context, request *http.Request, modelAdmin ModelAdmin, existing map[string]any, data map[string]any) (map[string]any, error) {
	initial := cloneRow(existing)
	row := &adminRowModel{meta: modelAdmin.Model, values: cloneRow(initial)}
	form := forms.NewModelForm(forms.ModelFormOptions{
		Model:   row,
		Meta:    modelAdmin.Model,
		Include: adminEditableFormFields(modelAdmin, request),
		Exclude: append([]string(nil), modelAdmin.Exclude...),
		Data:    data,
		Initial: initial,
		Context: ctx,
	})
	if !form.IsValid() {
		return nil, fmt.Errorf("%w: %s", forms.ErrValidation, form.RenderErrors())
	}
	cleaned := make(map[string]any, len(form.FieldNames()))
	for _, name := range form.FieldNames() {
		if value, ok := form.CleanedData[name]; ok {
			cleaned[name] = value
		}
	}
	return cleaned, nil
}

func adminEditableFormFields(modelAdmin ModelAdmin, request *http.Request) []string {
	var fields []string
	if modelAdmin.Hooks.GetFields != nil {
		fields = append(fields, modelAdmin.Hooks.GetFields(request)...)
	} else if len(modelAdmin.Fields) > 0 {
		fields = append(fields, modelAdmin.Fields...)
	} else {
		fieldsets := modelAdmin.Fieldsets
		if modelAdmin.Hooks.GetFieldsets != nil {
			fieldsets = modelAdmin.Hooks.GetFieldsets(request)
		}
		for _, fieldset := range fieldsets {
			fields = append(fields, fieldset.Fields...)
		}
	}
	if len(fields) == 0 {
		for _, field := range modelAdmin.Model.Fields {
			if !field.PrimaryKey {
				fields = append(fields, field.Name)
			}
		}
	}
	excluded := setFromSlice(modelAdmin.Exclude)
	for _, field := range modelAdmin.GetReadonlyFields(request) {
		excluded[field] = struct{}{}
	}
	seen := map[string]struct{}{}
	editable := make([]string, 0, len(fields))
	for _, field := range fields {
		if field == "" {
			continue
		}
		if _, ok := excluded[field]; ok {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		editable = append(editable, field)
	}
	return editable
}
