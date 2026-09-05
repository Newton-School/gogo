package admin

import (
	"context"
	"errors"
	"html/template"
	"net/url"
	"slices"
	"strconv"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/templates"
)

type listEditState struct {
	objects  []Object
	forms    []*forms.ModelForm
	writable []bool
	proposed []listEditRowToken
	valid    bool
	token    string
	saved    int
	outcome  string
}

func (s *Site) listEditableFields(ctx context.Context, p auth.Principal, options ModelAdmin, object Object) (readonly []string, writable bool, err error) {
	defer func() {
		if recover() != nil {
			readonly, writable, err = nil, false, errors.New("admin: list permissions unavailable")
		}
	}()
	before, err := listRowToken(object)
	if err != nil {
		return nil, false, err
	}
	if err := s.allowed(ctx, p, "view", options, object); err != nil {
		return nil, false, err
	}
	writable = true
	if err := s.allowed(ctx, p, "change", options, object); err != nil {
		if !errors.Is(err, auth.ErrPermissionDenied) {
			return nil, false, err
		}
		writable = false
	}
	readonly = slices.Clone(options.ReadonlyFields)
	if options.GetReadonlyFields != nil {
		readonly = append(readonly, options.GetReadonlyFields(ctx, object)...)
	}
	if !writable {
		readonly = append(readonly, options.ListEditable...)
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	after, err := listRowToken(object)
	if err != nil || after != before {
		return nil, false, ErrConflict
	}
	return readonly, writable, nil
}

func (s *Site) bindListEdit(ctx context.Context, p auth.Principal, options ModelAdmin, objects []Object, posted url.Values) (*listEditState, error) {
	state := &listEditState{objects: objects, valid: true}
	initial := make([]listEditRowToken, len(objects))
	for index, object := range objects {
		var err error
		initial[index], err = listRowToken(object)
		if err != nil {
			return nil, err
		}
	}
	for index, object := range objects {
		// An earlier row's callback must not modify a future row before that
		// row has captured its own proposed/validated snapshot.
		if err := sameListObject(object, initial[index]); err != nil {
			return nil, err
		}
		if object.Record == nil || object.Record.Schema().Key() != options.Schema.Key() {
			return nil, ErrConflict
		}
		readonly, writable, err := s.listEditableFields(ctx, p, options, object)
		if err != nil {
			return nil, err
		}
		settings := []forms.Option{forms.WithPrefix("form-" + strconv.Itoa(index))}
		if posted != nil && writable {
			settings = append(settings, forms.WithData(posted))
		}
		form, err := forms.NewModelForm(ctx, object.Record, forms.ModelFormOptions{Fields: options.ListEditable, Exclude: options.Exclude, Readonly: readonly, Overrides: options.FormOverrides, Checker: options.ConstraintChecker}, settings...)
		if err != nil {
			return nil, err
		}
		state.writable = append(state.writable, writable)
		state.forms = append(state.forms, form)
		if posted != nil && writable {
			valid := form.IsValid()
			if err := form.Err(); err != nil {
				return nil, err
			}
			state.valid = state.valid && valid
		}
		proposed, err := listRowToken(object)
		if err != nil {
			return nil, err
		}
		state.proposed = append(state.proposed, proposed)
	}
	if err := state.checkProposals(); err != nil {
		return nil, err
	}
	return state, nil
}

func (state *listEditState) checkProposals() error {
	for index, object := range state.objects {
		if err := sameListObject(object, state.proposed[index]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Site) listEditCell(ctx context.Context, form *forms.ModelForm, object Object, name string) (template.HTML, bool, error) {
	field, ok := form.BoundField(name)
	if !ok {
		return "", false, nil
	}
	widget, err := field.HTML()
	if err != nil {
		return "", false, err
	}
	html, err := s.engine.Render(ctx, "list_edit_cell.html", templates.Context{"id": field.ID, "label": field.Label(), "object_label": object.Label, "widget": widget, "errors": field.Errors, "help": field.Field.HelpText, "grouped": field.Grouped()})
	return template.HTML(html), true, err
}
