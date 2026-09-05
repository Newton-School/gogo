package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
)

func manyFields(options ModelAdmin, readonly []string) []string {
	names := []string{}
	for _, name := range options.Fields {
		field, _ := options.Schema.Field(name)
		if field.Kind == models.ManyToMany && field.IsEditable() && !slices.Contains(readonly, name) && !slices.Contains(options.Exclude, name) {
			names = append(names, name)
		}
	}
	return names
}

func (s *Site) relationChoices(ctx context.Context, p auth.Principal, options ModelAdmin, name string) ([]forms.Choice, error) {
	field, _ := options.Schema.Field(name)
	target, ok := s.models[field.Relation.Target]
	if !ok || s.allowed(ctx, p, "view", target, Object{}) != nil {
		return nil, auth.ErrPermissionDenied
	}
	store, err := s.config.Store.Scope(ctx, p, s.config.Name, target.Schema)
	if err != nil || store == nil {
		return nil, auth.ErrPermissionDenied
	}
	ordering := append([]string(nil), target.Ordering...)
	for _, key := range target.Schema.PKFields() {
		if !slices.Contains(ordering, key.Name) && !slices.Contains(ordering, "-"+key.Name) {
			ordering = append(ordering, key.Name)
		}
	}
	page, err := store.List(ctx, ListQuery{Ordering: ordering, Limit: 1001})
	if err != nil {
		return nil, err
	}
	if len(page.Objects) > 1000 {
		return nil, errors.New("admin: relation choices exceed 1000; configure AutocompleteFields")
	}
	choices := []forms.Choice{}
	for _, object := range page.Objects {
		if object.Record == nil || object.Record.Schema().Key() != target.Schema.Key() {
			return nil, errors.New("admin: invalid scoped relation choice")
		}
		if s.allowed(ctx, p, "view", target, object) != nil {
			continue
		}
		id, err := relationChoiceID(field, object)
		if err != nil {
			return nil, err
		}
		resolved, err := options.ResolveRelation(ctx, field, []string{id})
		if err != nil || len(resolved) != 1 || !sameChoiceIdentity(id, resolved[0], true) {
			continue
		}
		choices = append(choices, forms.Choice{Value: id, Label: object.Label})
	}
	return choices, nil
}

func (s *Site) formRelations(ctx context.Context, p auth.Principal, options ModelAdmin, object Object, store ScopedStore, readonly []string) (map[string][]any, error) {
	names := manyFields(options, readonly)
	if len(names) == 0 {
		return nil, nil
	}
	loader, ok := store.(RelationStore)
	if !ok {
		return nil, errors.New("admin: scoped many-to-many form store required")
	}
	initial, err := loader.InitialRelations(ctx, object, names)
	if err != nil {
		return nil, err
	}
	visible := map[string][]any{}
	for _, name := range names {
		field, _ := options.Schema.Field(name)
		target, ok := s.models[field.Relation.Target]
		if !ok || s.allowed(ctx, p, "view", target, Object{}) != nil || len(target.Schema.PKFields()) != 1 {
			return nil, auth.ErrPermissionDenied
		}
		targetStore, err := s.config.Store.Scope(ctx, p, s.config.Name, target.Schema)
		if err != nil || targetStore == nil {
			return nil, auth.ErrPermissionDenied
		}
		visible[name] = []any{}
		if len(initial[name]) > 10000 {
			return nil, errors.New("admin: relation selection exceeds bound")
		}
		for _, value := range initial[name] {
			key, err := models.NewRecord(target.Schema)
			if err != nil {
				return nil, err
			}
			if err = key.Set(target.Schema.PKFields()[0].Name, value); err != nil {
				return nil, err
			}
			stub, err := objectFromRecord(key)
			if err != nil {
				return nil, err
			}
			current, err := targetStore.Get(ctx, stub.ID, false)
			if err != nil {
				return nil, auth.ErrPermissionDenied
			}
			if s.allowed(ctx, p, "view", target, current) != nil {
				continue
			}
			if options.ResolveRelation == nil {
				return nil, errors.New("admin: scoped relation resolver required")
			}
			resolved, err := options.ResolveRelation(ctx, field, []string{fmt.Sprint(value)})
			if err != nil || len(resolved) != 1 || !sameChoiceIdentity(value, resolved[0], false) {
				continue
			}
			visible[name] = append(visible[name], value)
		}
	}
	return visible, nil
}

func relationVersion(object Object, initial map[string][]any) (Object, error) {
	values, err := relationSnapshot(initial)
	if err != nil {
		return Object{}, err
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return Object{}, err
	}
	object.Version = fmt.Sprintf("%x", sha256.Sum256(encoded))
	return object, nil
}

func relationSnapshot(initial map[string][]any) (map[string][]string, error) {
	values := map[string][]string{}
	for name, items := range initial {
		values[name] = []string{}
		for _, value := range items {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			values[name] = append(values[name], string(encoded))
		}
		sort.Strings(values[name])
	}
	return values, nil
}

func addRelationSnapshot(snapshot map[string]any, options ModelAdmin, initial map[string][]any) error {
	values, err := relationSnapshot(initial)
	if err != nil {
		return err
	}
	for name, ids := range values {
		if !sensitiveField(options, name) {
			snapshot[name] = ids
		}
	}
	return nil
}

func (s *Site) saveFormRelations(ctx context.Context, p auth.Principal, options ModelAdmin, store ScopedStore, object Object, action string, form *forms.ModelForm) error {
	values, err := form.CleanedRelations()
	if err != nil {
		return err
	}
	if len(values) == 0 {
		return nil
	}
	saver, ok := store.(RelationStore)
	if !ok {
		return errors.New("admin: scoped many-to-many form store required")
	}
	if err := form.CheckRelations(ctx); err != nil {
		return err
	}
	// Preserve invisible existing links using fresh server-owned identities.
	// They never enter form initials, conflict tokens, choices or audit diffs.
	// Posted IDs are checked separately, even if they already exist as hidden
	// links, so guessing one cannot change the response into a success oracle.
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	current, err := saver.InitialRelations(ctx, object, names)
	if err != nil {
		return err
	}
	for _, name := range names {
		field, _ := options.Schema.Field(name)
		target, ok := s.models[field.Relation.Target]
		if !ok || len(target.Schema.PKFields()) != 1 {
			return auth.ErrPermissionDenied
		}
		targetStore, err := s.config.Store.Scope(ctx, p, s.config.Name, target.Schema)
		if err != nil || targetStore == nil {
			return auth.ErrPermissionDenied
		}
		eligible := func(value any) (bool, error) {
			record, err := models.NewRecord(target.Schema)
			if err != nil {
				return false, err
			}
			if err = record.Set(target.Schema.PKFields()[0].Name, value); err != nil {
				return false, nil
			}
			stub, err := objectFromRecord(record)
			if err != nil {
				return false, err
			}
			row, err := targetStore.Get(ctx, stub.ID, true)
			if errors.Is(err, ErrNotFound) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if s.allowed(ctx, p, "view", target, row) != nil {
				return false, nil
			}
			resolved, err := options.ResolveRelation(ctx, field, []string{fmt.Sprint(value)})
			return err == nil && len(resolved) == 1 && sameChoiceIdentity(value, resolved[0], false), nil
		}
		for _, value := range values[name] {
			allowed, err := eligible(value)
			if err != nil {
				return err
			}
			if !allowed {
				form.AddError(name, forms.Error{Code: "invalid_choice", Message: "Select valid choices."})
				return errInvalidForm
			}
		}
		for _, value := range current[name] {
			allowed, err := eligible(value)
			if err != nil {
				return err
			}
			if !allowed {
				values[name] = append(values[name], value)
			}
		}
	}
	return saver.SaveRelations(ctx, object, values, func(ctx context.Context, change RelationChange) error {
		if change.Source.Record == nil || change.Source.Record.Schema().Key() != options.Schema.Key() || change.Source.ID != object.ID {
			return auth.ErrPermissionDenied
		}
		if err := s.allowed(ctx, p, action, options, change.Source); err != nil {
			return err
		}
		field, ok := options.Schema.Field(change.Field)
		if !ok || field.Relation == nil || field.Kind != models.ManyToMany {
			return auth.ErrPermissionDenied
		}
		target, ok := s.models[field.Relation.Target]
		if !ok {
			return auth.ErrPermissionDenied
		}
		for _, rows := range [][]Object{change.Added, change.Removed} {
			for _, row := range rows {
				if row.Record == nil || row.Record.Schema().Key() != target.Schema.Key() {
					return auth.ErrPermissionDenied
				}
				if err := s.allowed(ctx, p, "view", target, row); err != nil {
					return err
				}
				id, err := relationChoiceID(field, row)
				if err != nil {
					return err
				}
				resolved, err := options.ResolveRelation(ctx, field, []string{id})
				if err != nil || len(resolved) != 1 || !sameChoiceIdentity(id, resolved[0], true) {
					return auth.ErrPermissionDenied
				}
			}
		}
		return nil
	})
}

func sameChoiceIdentity(expected, resolved any, textual bool) bool {
	if textual {
		return fmt.Sprint(expected) == fmt.Sprint(resolved)
	}
	left, err := json.Marshal(expected)
	if err != nil {
		return false
	}
	right, err := json.Marshal(resolved)
	return err == nil && string(left) == string(right)
}
