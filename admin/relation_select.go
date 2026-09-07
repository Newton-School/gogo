package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
)

type toOneSelectState struct {
	site      *Site
	principal auth.Principal
	options   ModelAdmin
	names     []string
	allowed   map[string]map[string]bool
	checked   map[string]toOneReference
	failure   error
}

type toOneReference struct{ ObjectID, ChoiceID, Version string }

func ordinaryToOneSelect(field forms.Field) bool {
	if field.Disabled || field.Kind != forms.ModelChoice {
		return false
	}
	if field.Widget == nil {
		return true
	}
	switch widget := field.Widget.(type) {
	case forms.InputWidget:
		return widget.Type == "select"
	case *forms.InputWidget:
		return widget != nil && widget.Type == "select"
	default:
		return false
	}
}

func (s *Site) toOneSelectOverrides(ctx context.Context, p auth.Principal, options ModelAdmin, readonly []string, overrides map[string]forms.Field) (map[string]forms.Field, *toOneSelectState, error) {
	state := &toOneSelectState{site: s, principal: p, options: options, allowed: map[string]map[string]bool{}, checked: map[string]toOneReference{}}
	for _, name := range options.Fields {
		metadata, _ := options.Schema.Field(name)
		if metadata.Kind != models.ForeignKey && metadata.Kind != models.OneToOne || !metadata.IsEditable() || slices.Contains(options.Exclude, name) || slices.Contains(readonly, name) || slices.Contains(options.AutocompleteFields, name) || slices.Contains(options.RawIDFields, name) {
			continue
		}
		field, err := forms.FieldFromModel(metadata)
		if override, exists := overrides[name]; exists {
			field, err = override.Clone(), nil
		}
		if err != nil {
			return nil, nil, err
		}
		if !ordinaryToOneSelect(field) {
			continue
		}
		if options.ResolveRelation == nil {
			return nil, nil, errors.New("admin: ordinary relationship selects require a scoped resolver")
		}
		if len(field.Choices) > 0 {
			state.allowed[name] = map[string]bool{}
			for _, choice := range field.Choices {
				if choice.Value != "" {
					state.allowed[name][choice.Value] = true
				}
			}
		}
		choices, err := state.choices(ctx, metadata)
		if err != nil {
			return nil, nil, err
		}
		// Always start with an explicit empty choice. A required relation must
		// not silently acquire the first eligible record when the form opens.
		field.Choices = append([]forms.Choice{{Value: "", Label: "---------"}}, choices...)
		if overrides == nil {
			overrides = map[string]forms.Field{}
		}
		overrides[name] = field
		state.names = append(state.names, name)
	}
	return overrides, state, nil
}

func (state *toOneSelectState) choices(ctx context.Context, field models.Field) ([]forms.Choice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, exists := state.site.models[field.Relation.Target]
	if !exists {
		return nil, errors.New("admin: relationship select target must be registered")
	}
	if err := state.site.allowed(ctx, state.principal, "view", target, Object{}); err != nil {
		return nil, err
	}
	store, err := state.site.config.Store.Scope(ctx, state.principal, state.site.config.Name, target.Schema)
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errors.New("admin: relationship scope unavailable")
	}
	ordering := slices.Clone(target.Ordering)
	for _, pk := range target.Schema.PKFields() {
		if !slices.Contains(ordering, pk.Name) && !slices.Contains(ordering, "-"+pk.Name) {
			ordering = append(ordering, pk.Name)
		}
	}
	page, err := store.List(ctx, ListQuery{Ordering: ordering, Limit: 1001})
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	if err != nil {
		return nil, err
	}
	if len(page.Objects) > 1000 || page.Count > 1000 {
		return nil, errors.New("admin: relationship select exceeds 1000 candidates; configure AutocompleteFields")
	}
	choices := []forms.Choice{}
	seen := map[string]bool{}
	for _, object := range page.Objects {
		if object.Record == nil || object.Record.Schema().Key() != target.Schema.Key() {
			return nil, errors.New("admin: invalid scoped relationship choice")
		}
		err := state.site.allowed(ctx, state.principal, "view", target, object)
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		if err != nil {
			if errors.Is(err, auth.ErrPermissionDenied) {
				continue
			}
			return nil, err
		}
		id, err := relationChoiceID(field, object)
		if err != nil || id == "" || seen[id] {
			return nil, errors.New("admin: invalid or duplicate relationship choice")
		}
		seen[id] = true
		if allowed, limited := state.allowed[field.Name]; limited && !allowed[id] {
			continue
		}
		resolved, err := state.options.ResolveRelation(ctx, field, []string{id})
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		if err != nil {
			if errors.Is(err, auth.ErrPermissionDenied) {
				continue
			}
			return nil, err
		}
		if len(resolved) != 1 || !sameChoiceIdentity(id, resolved[0], true) {
			return nil, auth.ErrPermissionDenied
		}
		choices = append(choices, forms.Choice{Value: id, Label: object.Label})
	}
	return choices, nil
}

func (state *toOneSelectState) resolve(ctx context.Context, field models.Field, ids []string) ([]any, error) {
	if !slices.Contains(state.names, field.Name) {
		if state.options.ResolveRelation == nil {
			return nil, forms.Error{Code: "invalid_choice", Message: "Select a valid choice."}
		}
		return state.options.ResolveRelation(ctx, field, ids)
	}
	if len(ids) != 1 || ids[0] == "" {
		return nil, forms.Error{Code: "invalid_choice", Message: "Select a valid choice."}
	}
	resolved, eligible, err := state.selected(ctx, field, ids[0])
	if err != nil {
		state.failure = err
		return nil, forms.Error{Code: "lookup_unavailable", Message: "Relationship choices are unavailable."}
	}
	if !eligible {
		return nil, forms.Error{Code: "invalid_choice", Message: "Select a valid choice."}
	}
	return resolved, nil
}

// POST checks one exact target, never rescanning every display candidate. The
// scoped Get lock lasts for the surrounding parent transaction; a second fresh
// scoped Get catches same-transaction effects of policy/resolver callbacks.
func (state *toOneSelectState) selected(ctx context.Context, field models.Field, id string) ([]any, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if allowed, limited := state.allowed[field.Name]; limited && !allowed[id] {
		return nil, false, nil
	}
	target, exists := state.site.models[field.Relation.Target]
	if !exists {
		return nil, false, errors.New("admin: relationship select target must be registered")
	}
	if err := state.site.allowed(ctx, state.principal, "view", target, Object{}); err != nil {
		return nil, false, err
	}
	scope, err := state.site.config.Store.Scope(ctx, state.principal, state.site.config.Name, target.Schema)
	if err != nil || scope == nil {
		if err == nil {
			err = errors.New("admin: relationship scope unavailable")
		}
		return nil, false, err
	}
	keys := slices.Clone(field.Relation.TargetFields)
	if len(keys) == 0 {
		for _, key := range target.Schema.PKFields() {
			keys = append(keys, key.Name)
		}
	}
	objectID := id
	if len(keys) == 1 {
		page, err := scope.List(ctx, ListQuery{Filters: map[string]string{keys[0]: id}, Limit: 2})
		if canceled := ctx.Err(); canceled != nil {
			return nil, false, canceled
		}
		if err != nil {
			return nil, false, err
		}
		if len(page.Objects) != 1 {
			return nil, false, nil
		}
		objectID = page.Objects[0].ID
	}
	row, err := scope.Get(ctx, objectID, true)
	if canceled := ctx.Err(); canceled != nil {
		return nil, false, canceled
	}
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	valid := func(object Object) bool {
		if object.ID != objectID || object.Record == nil || object.Record.Schema().Key() != target.Schema.Key() {
			return false
		}
		actual, err := relationChoiceID(field, object)
		return err == nil && actual == id
	}
	if !valid(row) {
		return nil, false, nil
	}
	authorized, err := objectFromRecord(row.Record)
	if err != nil {
		return nil, false, err
	}
	if err := state.site.allowed(ctx, state.principal, "view", target, row); err != nil {
		if errors.Is(err, auth.ErrPermissionDenied) {
			return nil, false, ctx.Err()
		}
		return nil, false, err
	}
	resolved, err := state.options.ResolveRelation(ctx, field, []string{id})
	if canceled := ctx.Err(); canceled != nil {
		return nil, false, canceled
	}
	if err != nil {
		if errors.Is(err, auth.ErrPermissionDenied) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if len(resolved) != 1 || !sameChoiceIdentity(id, resolved[0], true) {
		return nil, false, nil
	}
	scope, err = state.site.config.Store.Scope(ctx, state.principal, state.site.config.Name, target.Schema)
	if err != nil || scope == nil {
		if err == nil {
			err = errors.New("admin: relationship scope unavailable")
		}
		return nil, false, err
	}
	row, err = scope.Get(ctx, objectID, true)
	if canceled := ctx.Err(); canceled != nil {
		return nil, false, canceled
	}
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !valid(row) {
		return nil, false, nil
	}
	current, err := objectFromRecord(row.Record)
	if err != nil {
		return nil, false, err
	}
	if current.Version != authorized.Version {
		return nil, false, nil
	}
	state.checked[field.Name] = toOneReference{ObjectID: objectID, ChoiceID: id, Version: current.Version}
	return resolved, true, nil
}

type toOneSnapshot struct {
	ID, Encoded string
	Empty       bool
}

func (state *toOneSelectState) snapshot(record models.Record) (map[string]toOneSnapshot, error) {
	return snapshotToOneFields(record, state.names)
}

func snapshotToOneFields(record models.Record, names []string) (map[string]toOneSnapshot, error) {
	values := map[string]toOneSnapshot{}
	for _, name := range names {
		value, err := record.Get(name)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		values[name] = toOneSnapshot{ID: fmt.Sprint(value), Encoded: string(encoded), Empty: value == nil}
	}
	return values, nil
}

func (state *toOneSelectState) recheck(ctx context.Context, values map[string]toOneSnapshot) error {
	for _, name := range state.names {
		if values[name].Empty {
			continue
		}
		metadata, _ := state.options.Schema.Field(name)
		resolved, err := state.resolve(ctx, metadata, []string{values[name].ID})
		if state.failure != nil {
			return state.failure
		}
		if err != nil {
			return auth.ErrPermissionDenied
		}
		if len(resolved) != 1 {
			return auth.ErrPermissionDenied
		}
		encoded, err := json.Marshal(resolved[0])
		if err != nil || string(encoded) != values[name].Encoded {
			return auth.ErrPermissionDenied
		}
	}
	return ctx.Err()
}

// An existing out-of-scope relation is not an authorized history disclosure.
// Omit that field's change instead of publishing its hidden identity or claiming
// that the previously stored value was empty. Other changes remain auditable.
func (state *toOneSelectState) redactAudit(ctx context.Context, before, after map[string]any) error {
	for _, name := range state.names {
		value, exists := before[name]
		if !exists || value == nil {
			continue
		}
		field, _ := state.options.Schema.Field(name)
		_, eligible, err := state.selected(ctx, field, fmt.Sprint(value))
		if err != nil {
			return err
		}
		if !eligible {
			delete(before, name)
			delete(after, name)
		}
	}
	return ctx.Err()
}
