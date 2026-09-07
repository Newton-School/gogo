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
	return s.renderModelFormWithExtra(ctx, options, object, form, readonly, readonlyValues, nil)
}

func (s *Site) renderModelFormWithExtra(ctx context.Context, options ModelAdmin, object Object, form *forms.ModelForm, readonly []string, readonlyValues map[string]any, extra *forms.Form) (template.HTML, error) {
	if len(options.Fieldsets) == 0 {
		html, err := form.Render("div")
		if err != nil || extra == nil {
			return html, err
		}
		additional, err := extra.Render("div")
		return html + additional, err
	}
	groups := []any{}
	for _, fieldset := range options.Fieldsets {
		rows := []any{}
		for _, name := range fieldset.Fields {
			if slices.Contains(options.Exclude, name) {
				continue
			}
			metadata, _ := options.Schema.Field(name)
			label := metadata.Label
			if label == "" {
				label = name
			}
			bound, boundExists := form.BoundField(name)
			if stockGrantField(options, name) {
				label, metadata.HelpText = grantFieldLabel(AccountGrantKind(name)), grantHelp
				if extra != nil {
					bound, boundExists = extra.BoundField(name)
				}
				if !boundExists {
					if _, visible := readonlyValues[name]; !visible {
						continue
					}
				}
			}
			if boundExists {
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
				rows = append(rows, templates.Context{"label": label, "value": s.displayValue(options, name, value), "readonly": true})
			}
		}
		if len(rows) > 0 {
			groups = append(groups, templates.Context{"name": fieldset.Name, "description": fieldset.Description, "classes": strings.Join(fieldset.Classes, " "), "collapse": slices.Contains(fieldset.Classes, "collapse"), "rows": rows})
		}
	}
	output, err := s.engine.Render(ctx, "fieldsets.html", templates.Context{"fieldsets": groups, "nonfield_errors": form.Errors()[forms.NonFieldErrors]})
	return template.HTML(output), err
}
