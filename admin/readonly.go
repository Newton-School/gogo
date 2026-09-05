package admin

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
)

func (s *Site) readonlyValues(ctx context.Context, p auth.Principal, options ModelAdmin, object Object, store ScopedStore, readonly []string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values := map[string]any{}
	names := append([]string(nil), readonly...)
	for _, name := range options.Fields {
		field, _ := options.Schema.Field(name)
		if !field.IsEditable() && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	relations := []string{}
	for _, name := range names {
		if _, exists := values[name]; exists {
			continue
		}
		field, ok := options.Schema.Field(name)
		if !ok {
			return nil, errors.New("admin: unknown readonly field")
		}
		if field.Kind == models.ManyToMany {
			values[name] = ""
			if field.Relation == nil {
				return nil, errors.New("admin: invalid readonly relation")
			}
			target, ok := s.models[field.Relation.Target]
			if !ok {
				continue
			}
			err := s.allowed(ctx, p, "view", target, Object{})
			if canceled := ctx.Err(); canceled != nil {
				return nil, canceled
			}
			if err != nil {
				if errors.Is(err, auth.ErrPermissionDenied) {
					continue
				}
				return nil, err
			}
			relations = append(relations, name)
		} else {
			value, err := object.Record.Get(name)
			if err != nil {
				return nil, err
			}
			values[name] = value
		}
	}
	if len(relations) == 0 || !object.Record.State().Persisted {
		return values, nil
	}
	reader, ok := store.(RelationReader)
	if !ok {
		return nil, errors.New("admin: scoped readonly relation reader required")
	}
	rows, err := reader.ReadRelations(ctx, object, relations)
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	if err != nil {
		return nil, err
	}
	for _, name := range relations {
		field, _ := options.Schema.Field(name)
		target := s.models[field.Relation.Target]
		if _, ok := rows[name]; !ok {
			return nil, errors.New("admin: incomplete readonly relation snapshot")
		}
		if len(rows[name]) > 1000 {
			return nil, errors.New("admin: readonly relationship display exceeds bound")
		}
		labels := []string{}
		for _, related := range rows[name] {
			if related.Record == nil || related.Record.Schema().Key() != target.Schema.Key() || related.ID == "" {
				return nil, errors.New("admin: invalid readonly relation record")
			}
			err := s.allowed(ctx, p, "view", target, related)
			if canceled := ctx.Err(); canceled != nil {
				return nil, canceled
			}
			if err != nil {
				if errors.Is(err, auth.ErrPermissionDenied) {
					continue
				}
				return nil, err
			}
			if options.ResolveRelation != nil {
				id, err := relationChoiceID(field, related)
				if err != nil {
					return nil, err
				}
				resolved, err := options.ResolveRelation(ctx, field, []string{id})
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
					continue
				}
			}
			labels = append(labels, related.Label)
		}
		sort.Strings(labels)
		values[name] = strings.Join(labels, ", ")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return values, nil
}
