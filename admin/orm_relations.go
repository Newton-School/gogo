package admin

import (
	"context"
	"errors"
	"sort"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func (s *ormScoped) relationManager(object Object, name string, editable bool) (orm.RelationManager, models.Schema, error) {
	field, ok := s.schema.Field(name)
	if !ok || field.Kind != models.ManyToMany || field.Relation == nil || editable && field.Relation.Through != "" || object.Record == nil || object.Record.Schema().Key() != s.schema.Key() {
		return orm.RelationManager{}, models.Schema{}, errors.New("admin: automatic declared many-to-many relation required")
	}
	registry := s.owner.config.Store.Registry
	if registry == nil {
		return orm.RelationManager{}, models.Schema{}, errors.New("admin: complete relation registry required")
	}
	target, ok := registry.Get(field.Relation.Target)
	if !ok || editable && len(target.PKFields()) != 1 {
		return orm.RelationManager{}, models.Schema{}, errors.New("admin: scalar relation target primary key required")
	}
	manager := orm.RelationManager{Store: s.owner.config.Store, Source: object.Record, Name: name, Scope: func(ctx context.Context, schema models.Schema) (db.Predicate, error) {
		scope, err := s.owner.config.QueryScope(ctx, s.principal, schema)
		if err != nil {
			return db.Predicate{}, err
		}
		if scope.Identity == "" {
			return db.Predicate{}, auth.ErrPermissionDenied
		}
		return scope.Predicate, nil
	}}
	return manager, target, nil
}

func (s *ormScoped) InitialRelations(ctx context.Context, object Object, names []string) (map[string][]any, error) {
	initial := map[string][]any{}
	for _, name := range names {
		manager, target, err := s.relationManager(object, name, true)
		if err != nil {
			return nil, err
		}
		initial[name] = []any{}
		if !object.Record.State().Persisted {
			continue
		}
		rows, err := manager.All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			value, err := row.Get(target.PKFields()[0].Name)
			if err != nil {
				return nil, err
			}
			initial[name] = append(initial[name], value)
		}
	}
	return initial, nil
}

func (s *ormScoped) SaveRelations(ctx context.Context, object Object, values map[string][]any, authorize func(context.Context, RelationChange) error) error {
	if !db.InTransaction(ctx, s.owner.config.Store.Backend.Alias()) || authorize == nil {
		return errors.New("admin: relation transaction and authorization required")
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		manager, target, err := s.relationManager(object, name, true)
		if err != nil {
			return err
		}
		if len(values[name]) > 10000 {
			return errors.New("admin: relation selection exceeds bound")
		}
		rows := []models.Record{}
		for _, value := range values[name] {
			key := target.PKFields()[0]
			cleaned, err := key.Clean(ctx, value)
			if err != nil {
				return err
			}
			row, err := models.NewRecord(target)
			if err != nil {
				return err
			}
			if err = row.Set(key.Name, cleaned); err != nil {
				return err
			}
			rows = append(rows, row)
		}
		manager.Authorize = func(ctx context.Context, change orm.RelationChange) error {
			if err := s.owner.config.ValidateWrite(ctx, s.principal, change.Source); err != nil {
				return err
			}
			source, err := objectFromRecord(change.Source)
			if err != nil {
				return err
			}
			effect := RelationChange{Field: name, Source: source}
			for _, pair := range []struct {
				records []models.Record
				target  *[]Object
			}{{change.Added, &effect.Added}, {change.Removed, &effect.Removed}} {
				for _, row := range pair.records {
					object, err := objectFromRecord(row)
					if err != nil {
						return err
					}
					*pair.target = append(*pair.target, object)
				}
			}
			return authorize(ctx, effect)
		}
		if err := manager.Set(ctx, rows...); err != nil {
			return err
		}
	}
	return nil
}

func (s *ormScoped) ReadRelations(ctx context.Context, object Object, names []string) (map[string][]Object, error) {
	result := map[string][]Object{}
	for _, name := range names {
		manager, _, err := s.relationManager(object, name, false)
		if err != nil {
			return nil, err
		}
		result[name] = []Object{}
		if !object.Record.State().Persisted {
			continue
		}
		manager.MaxObjects = 1000
		rows, err := manager.All(ctx)
		if err != nil {
			return nil, err
		}
		for _, record := range rows {
			object, err := objectFromRecord(record)
			if err != nil {
				return nil, err
			}
			result[name] = append(result[name], object)
		}
	}
	return result, nil
}
