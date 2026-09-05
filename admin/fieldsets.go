package admin

import (
	"context"
	"errors"
	"html/template"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/templates"
)

func (s *Site) renderModelForm(ctx context.Context, options ModelAdmin, object Object, form *forms.ModelForm, readonly []string, readonlyValues map[string]any) (template.HTML, error) {
	if len(options.Fieldsets) == 0 {
		return form.Render("div")
	}
	groups := []any{}
	for _, fieldset := range options.Fieldsets {
		rows := []any{}
		for _, name := range fieldset.Fields {
			metadata, _ := options.Schema.Field(name)
			label := metadata.Label
			if label == "" {
				label = name
			}
			if bound, ok := form.BoundField(name); ok {
				widget, err := bound.HTML()
				if err != nil {
					return "", err
				}
				rows = append(rows, templates.Context{"label": label, "id": bound.ID, "widget": widget, "help": metadata.HelpText, "errors": bound.Errors, "grouped": bound.Grouped()})
			} else if slices.Contains(readonly, name) || !metadata.IsEditable() {
				value, ok := readonlyValues[name]
				if !ok {
					if metadata.Kind == models.ManyToMany {
						return "", errors.New("admin: scoped readonly relation snapshot required")
					}
					var err error
					value, err = object.Record.Get(name)
					if err != nil {
						return "", err
					}
				}
				rows = append(rows, templates.Context{"label": label, "value": value, "readonly": true})
			}
		}
		groups = append(groups, templates.Context{"name": fieldset.Name, "description": fieldset.Description, "classes": strings.Join(fieldset.Classes, " "), "collapse": slices.Contains(fieldset.Classes, "collapse"), "rows": rows})
	}
	output, err := s.engine.Render(ctx, "fieldsets.html", templates.Context{"fieldsets": groups, "nonfield_errors": form.Errors()[forms.NonFieldErrors]})
	return template.HTML(output), err
}
