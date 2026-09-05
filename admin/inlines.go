package admin

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/templates"
)

type Inline struct {
	Name, Label, FKName       string
	Schema                    models.Schema
	Fields, Exclude, Readonly []string
	Extra, Minimum, Maximum   int
	CanDelete                 bool
	FormOverrides             map[string]forms.Field
	ConstraintChecker         models.ConstraintChecker
	ResolveRelation           func(context.Context, models.Field, []string) ([]any, error)
}

func (i *Inline) validate(parent models.Schema) error {
	if !models.ValidIdentifier(i.Name) {
		return errors.New("admin: inline needs an identifier")
	}
	if err := i.Schema.Validate(); err != nil {
		return err
	}
	fk, ok := i.Schema.Field(i.FKName)
	if !ok || fk.Relation == nil || fk.Relation.Target != parent.Key() || fk.Kind != models.ForeignKey {
		return errors.New("admin: inline needs a foreign key to its parent")
	}
	if len(parent.PKFields()) != 1 || len(fk.Relation.TargetFields) > 1 {
		return errors.New("admin: composite inline foreign key mapping requires an adapter")
	}
	if i.Maximum == 0 {
		i.Maximum = 1000
	}
	if i.Minimum < 0 || i.Extra < 0 || i.Maximum < i.Minimum || i.Extra > i.Maximum || i.Maximum > 1000 {
		return errors.New("admin: invalid inline bounds")
	}
	if len(i.Fields) == 0 {
		for _, field := range i.Schema.Fields {
			if field.IsEditable() && field.Name != i.FKName {
				i.Fields = append(i.Fields, field.Name)
			}
		}
	}
	for _, name := range i.Fields {
		if name == i.FKName {
			return errors.New("admin: parent foreign key cannot be an editable inline field")
		}
		if _, ok := i.Schema.Field(name); !ok {
			return errors.New("admin: unknown inline field")
		}
	}
	if i.Label == "" {
		i.Label = i.Schema.LabelPlural
		if i.Label == "" {
			i.Label = i.Schema.Name
		}
	}
	return nil
}

type inlineState struct {
	config      Inline
	store       ScopedStore
	objects     []Object
	forms       []*forms.ModelForm
	set         *forms.FormSet
	initial     int
	canAdd      bool
	parentValue any
}

func (s *Site) loadInlines(ctx context.Context, r *http.Request, p auth.Principal, options ModelAdmin, parent Object, bound bool) ([]*inlineState, error) {
	states := []*inlineState{}
	for _, config := range options.Inlines {
		state := &inlineState{config: config}
		store, err := s.config.Store.Scope(ctx, p, s.config.Name, config.Schema)
		if err != nil || store == nil {
			return nil, auth.ErrPermissionDenied
		}
		state.store = store
		state.canAdd = s.config.Policy.Authorize(ctx, p, "add", auth.Resource{App: config.Schema.AppLabel, Model: config.Schema.Name}) == nil
		fk, _ := config.Schema.Field(config.FKName)
		parentField := parent.Record.Schema().PKFields()[0].Name
		if len(fk.Relation.TargetFields) > 0 {
			parentField = fk.Relation.TargetFields[0]
		}
		state.parentValue, err = parent.Record.Get(parentField)
		if err != nil {
			return nil, err
		}
		existing := []Object{}
		if parent.Record.State().Persisted {
			page, err := store.List(ctx, ListQuery{Filters: map[string]string{config.FKName: fmt.Sprint(state.parentValue)}, Limit: config.Maximum + 1})
			if err != nil {
				return nil, err
			}
			if len(page.Objects) > config.Maximum {
				return nil, errors.New("admin: inline rows exceed configured maximum")
			}
			existing = page.Objects
		}
		state.initial = len(existing)
		byID := map[string]Object{}
		initial := []map[string]any{}
		ids := []string{}
		for _, object := range existing {
			actual, err := object.Record.Get(config.FKName)
			if err != nil || !reflect.DeepEqual(actual, state.parentValue) {
				return nil, auth.ErrPermissionDenied
			}
			if err = s.config.Policy.Authorize(ctx, p, "view", auth.Resource{App: config.Schema.AppLabel, Model: config.Schema.Name, ID: object.ID, Object: object.Record}); err != nil {
				return nil, err
			}
			values := map[string]any{}
			for _, name := range config.Fields {
				value, err := object.Record.Get(name)
				if err == nil {
					values[name] = value
				}
			}
			initial = append(initial, values)
			ids = append(ids, object.ID)
			byID[object.ID] = object
		}
		fields := []forms.Field{}
		for _, name := range config.Fields {
			metadata, _ := config.Schema.Field(name)
			if !metadata.IsEditable() || slices.Contains(config.Exclude, name) || slices.Contains(config.Readonly, name) {
				continue
			}
			field, err := forms.FieldFromModel(metadata)
			if override, ok := config.FormOverrides[name]; ok {
				field = override
				field.Name = name
				err = nil
			}
			if err != nil {
				return nil, err
			}
			if metadata.Relation != nil {
				meta := metadata
				field.Resolve = func(ctx context.Context, ids []string) ([]any, error) {
					if config.ResolveRelation == nil {
						return nil, auth.ErrPermissionDenied
					}
					return config.ResolveRelation(ctx, meta, ids)
				}
			}
			fields = append(fields, field)
		}
		total := state.initial
		if state.canAdd {
			total = min(config.Maximum, total+config.Extra)
		}
		if bound {
			set, err := forms.BindFormSet(ctx, fields, r.PostForm, forms.FormSetOptions{Prefix: config.Name, Initial: initial, ExistingIDs: ids, IdentityField: "_id", Minimum: config.Minimum, Maximum: config.Maximum, CanDelete: config.CanDelete, CanDeleteRow: func(ctx context.Context, id string) error {
				return s.config.Policy.Authorize(ctx, p, "delete", auth.Resource{App: config.Schema.AppLabel, Model: config.Schema.Name, ID: id})
			}})
			if err != nil {
				return nil, err
			}
			state.set = set
			total = len(set.Forms)
			invalidManagement := false
			for _, problem := range set.Errors {
				if problem.Code == "management" || problem.Code == "identity" {
					invalidManagement = true
				}
			}
			if invalidManagement {
				states = append(states, state)
				continue
			}
		}
		for index := 0; index < total; index++ {
			prefix := fmt.Sprintf("%s-%d", config.Name, index)
			var object Object
			if index < state.initial {
				if bound {
					id := r.PostForm.Get(prefix + "-_id")
					object, err = store.Get(ctx, id, true)
					if err != nil {
						return nil, err
					}
					if _, ok := byID[id]; !ok {
						return nil, auth.ErrPermissionDenied
					}
					actual, err := object.Record.Get(config.FKName)
					if err != nil || !reflect.DeepEqual(actual, state.parentValue) {
						return nil, auth.ErrPermissionDenied
					}
				} else {
					object = existing[index]
				}
			} else {
				if !state.canAdd {
					return nil, auth.ErrPermissionDenied
				}
				object, err = store.New(ctx)
				if err != nil {
					return nil, err
				}
				if err = object.Record.Set(config.FKName, state.parentValue); err != nil {
					return nil, err
				}
			}
			opts := []forms.Option{forms.WithPrefix(prefix)}
			bindRow := bound
			if bound && index >= state.initial && !slices.Contains(state.set.Ordered, index) && !slices.Contains(state.set.Deleted, index) {
				bindRow = false
			}
			if bindRow {
				opts = append(opts, forms.WithData(r.PostForm))
			}
			form, err := forms.NewModelForm(ctx, object.Record, forms.ModelFormOptions{Fields: config.Fields, Exclude: config.Exclude, Readonly: config.Readonly, Overrides: config.FormOverrides, Checker: config.ConstraintChecker, ResolveRelation: config.ResolveRelation}, opts...)
			if err != nil {
				return nil, err
			}
			state.objects = append(state.objects, object)
			state.forms = append(state.forms, form)
			if bound && index < state.initial {
				if err = s.checkToken(r.PostForm.Get(prefix+"-_edit_token"), p, ModelAdmin{Schema: config.Schema}, object, "inline:"+parent.ID); err != nil {
					return nil, err
				}
			}
			if bound && !slices.Contains(state.set.Deleted, index) && slices.Contains(state.set.Ordered, index) {
				action := "add"
				if index < state.initial {
					action = "change"
				}
				if err = s.config.Policy.Authorize(ctx, p, action, auth.Resource{App: config.Schema.AppLabel, Model: config.Schema.Name, ID: object.ID, Object: object.Record}); err != nil {
					return nil, err
				}
				if !form.IsValid() {
					state.set.Errors = append(state.set.Errors, forms.Error{Code: "invalid", Message: "Correct the errors in the inline rows."})
				}
			}
		}
		states = append(states, state)
	}
	return states, nil
}
func validInlines(states []*inlineState) bool {
	for _, state := range states {
		if state.set == nil || !state.set.IsValid() {
			return false
		}
	}
	return true
}
func (s *Site) saveInlines(ctx context.Context, p auth.Principal, parent Object, states []*inlineState) error {
	for _, state := range states {
		parentField := parent.Record.Schema().PKFields()[0].Name
		fk, _ := state.config.Schema.Field(state.config.FKName)
		if len(fk.Relation.TargetFields) > 0 {
			parentField = fk.Relation.TargetFields[0]
		}
		parentValue, err := parent.Record.Get(parentField)
		if err != nil {
			return err
		}
		for _, index := range state.set.Ordered {
			object := state.objects[index]
			if err = object.Record.Set(state.config.FKName, parentValue); err != nil {
				return err
			}
			action := "change"
			if index >= state.initial {
				action = "add"
			}
			saved, err := state.store.Save(ctx, object)
			if err != nil {
				return err
			}
			if err = state.store.Audit(ctx, LogEntry{ActorID: p.ID, Site: s.config.Name, Model: state.config.Schema.Key(), ObjectID: saved.ID, ObjectLabel: saved.Label, Action: action, At: time.Now().UTC()}); err != nil {
				return err
			}
		}
		for _, index := range state.set.Deleted {
			if index >= state.initial {
				continue
			}
			object := state.objects[index]
			collector, ok := state.store.(DeleteCollector)
			if !ok {
				return errors.New("admin: inline deletion collector required")
			}
			graph, err := collector.CollectDeletion(ctx, object)
			if err != nil {
				return err
			}
			if len(graph.Protected) > 0 {
				return ErrConflict
			}
			if err = s.executeDeletion(ctx, p, state.store, object); err != nil {
				return err
			}
			if err = state.store.Audit(ctx, LogEntry{ActorID: p.ID, Site: s.config.Name, Model: state.config.Schema.Key(), ObjectID: object.ID, ObjectLabel: object.Label, Action: "delete", At: time.Now().UTC()}); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Site) renderInlines(ctx context.Context, p auth.Principal, parent Object, states []*inlineState) (template.HTML, error) {
	groups := []any{}
	for _, state := range states {
		rows := []any{}
		for index, form := range state.forms {
			object := state.objects[index]
			html, err := form.Render("div")
			if err != nil {
				return "", err
			}
			prefix := state.config.Name + "-" + strconv.Itoa(index)
			token, err := s.token(p, ModelAdmin{Schema: state.config.Schema}, object, "inline:"+parent.ID)
			if err != nil {
				return "", err
			}
			canDelete := state.config.CanDelete && index < state.initial && s.config.Policy.Authorize(ctx, p, "delete", auth.Resource{App: state.config.Schema.AppLabel, Model: state.config.Schema.Name, ID: object.ID, Object: object.Record}) == nil
			rows = append(rows, templates.Context{"prefix": prefix, "id": object.ID, "token": token, "form": html, "label": object.Label, "can_delete": canDelete})
		}
		errs := forms.ErrorList{}
		if state.set != nil {
			errs = state.set.Errors
		}
		groups = append(groups, templates.Context{"name": state.config.Name, "label": state.config.Label, "initial": state.initial, "total": len(state.forms), "rows": rows, "errors": errs})
	}
	output, err := s.engine.Render(ctx, "inlines.html", templates.Context{"inlines": groups})
	return template.HTML(output), err
}
