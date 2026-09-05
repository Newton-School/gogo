package orm

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"strings"
)

type RelationChange struct {
	Action         string
	Source         models.Record
	Field          models.Field
	Added, Removed []models.Record
}
type RelationReceiver func(context.Context, RelationChange) error
type RelationManager struct {
	Store  *Store
	Source models.Record
	Name   string
	Scope  func(context.Context, models.Schema) (db.Predicate, error)
	// Authorize is read-only and may run again after BeforeChange callbacks,
	// against freshly scoped endpoints. It must be safe to call repeatedly.
	Authorize                 RelationReceiver
	BeforeChange, AfterChange []RelationReceiver
	MaxObjects                int
	// ThroughDefaults supplies creation-only values for explicit intermediaries.
	// DefaultFactory values are evaluated once per relationship operation.
	ThroughDefaults Defaults
}
type relationBinding struct {
	field   models.Field
	target  models.Schema
	reverse bool
	through *throughBinding
}

func (m RelationManager) resolve() (relationBinding, error) {
	return m.resolveName(false)
}

// Query paths and instance accessors have distinct reverse namespaces. Never
// accept an accessor as a fallback for an explicitly different query name.
func (m RelationManager) resolveQuery() (relationBinding, error) {
	return m.resolveName(true)
}

func reverseAccessorName(schema models.Schema, field models.Field) string {
	if field.Relation.RelatedName != "" {
		return field.Relation.RelatedName
	}
	if field.Kind == models.OneToOne {
		return strings.ToLower(schema.Name)
	}
	return strings.ToLower(schema.Name) + "_set"
}

func (m RelationManager) resolveName(query bool) (relationBinding, error) {
	if m.Store == nil || m.Store.Backend == nil || m.Store.Registry == nil || m.Source == nil {
		return relationBinding{}, errors.New("orm: relation manager requires backend, source and complete registry")
	}
	if m.Source.State().Database != "" && m.Source.State().Database != m.Store.Backend.Alias() {
		return relationBinding{}, errors.New("orm: cross-database relation rejected")
	}
	if field, ok := m.Source.Schema().Field(m.Name); ok {
		if field.Relation == nil {
			return relationBinding{}, errors.New("orm: stored field is not a relation")
		}
		target, ok := m.Store.Registry.Get(field.Relation.Target)
		if !ok {
			return relationBinding{}, errors.New("orm: unknown relation target")
		}
		binding := relationBinding{field: field, target: target}
		if field.Kind == models.ManyToMany {
			return m.bindThrough(binding, m.Source.Schema())
		}
		return binding, nil
	}
	var found *relationBinding
	for _, schema := range m.Store.Registry.All() {
		for _, field := range schema.Fields {
			if field.Relation == nil || field.Relation.Target != m.Source.Schema().Key() {
				continue
			}
			if field.Kind == models.ManyToMany && schema.Key() == field.Relation.Target && (field.Relation.Symmetrical == nil || *field.Relation.Symmetrical) {
				continue
			}
			hidden := strings.HasSuffix(field.Relation.RelatedName, "+")
			if hidden && (!query || field.Relation.RelatedQueryName == "") {
				continue
			}
			name := reverseAccessorName(schema, field)
			if query {
				switch {
				case field.Relation.RelatedQueryName != "":
					name = field.Relation.RelatedQueryName
				case field.Relation.RelatedName != "":
					name = field.Relation.RelatedName
				default:
					name = strings.ToLower(schema.Name)
				}
			}
			if name == m.Name {
				if found != nil {
					return relationBinding{}, errors.New("orm: ambiguous reverse relation")
				}
				binding := relationBinding{field: field, target: schema, reverse: true}
				if field.Kind == models.ManyToMany {
					var err error
					binding, err = m.bindThrough(binding, schema)
					if err != nil {
						return relationBinding{}, err
					}
				}
				found = &binding
			}
		}
	}
	if found == nil {
		return relationBinding{}, errors.New("orm: relation does not exist")
	}
	return *found, nil
}
func (m RelationManager) limit() int {
	if m.MaxObjects > 0 {
		return m.MaxObjects
	}
	return defaultDeleteLimit
}
func (m RelationManager) reader() DeleteCollector {
	return DeleteCollector{Store: m.Store, Scope: m.Scope, MaxObjects: m.limit()}
}
func (m RelationManager) source(ctx context.Context, lock bool) (models.Record, error) {
	where, err := recordPK(m.Source)
	if err != nil {
		return nil, err
	}
	rows, err := m.reader().records(ctx, m.Source.Schema(), where, lock, 2)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, ErrNotFound
	}
	return rows[0], nil
}
func relationTargetField(source models.Schema, field models.Field) (models.Field, error) {
	names := field.Relation.TargetFields
	if len(names) == 0 {
		for _, pk := range source.PKFields() {
			names = append(names, pk.Name)
		}
	}
	if len(names) != 1 {
		return models.Field{}, errors.New("orm: scalar relation requires one target field")
	}
	target, ok := source.Field(names[0])
	if !ok {
		return target, errors.New("orm: relation target field missing")
	}
	return target, nil
}
func (m RelationManager) related(ctx context.Context, binding relationBinding, source models.Record, lock bool) ([]models.Record, error) {
	if binding.through != nil {
		rows, _, err := m.manyRelated(ctx, binding, source, lock)
		return rows, err
	}
	var predicate db.Predicate
	if binding.reverse {
		key, err := relationTargetField(source.Schema(), binding.field)
		if err != nil {
			return nil, err
		}
		value, err := source.Get(key.Name)
		if err != nil {
			return nil, err
		}
		predicate = Q(binding.field.Name, value)
	} else {
		value, err := source.Get(binding.field.Name)
		if err != nil {
			return nil, err
		}
		if value == nil {
			return []models.Record{}, nil
		}
		key, err := relationTargetField(binding.target, binding.field)
		if err != nil {
			return nil, err
		}
		predicate = Q(key.Name, value)
	}
	rows, err := m.reader().records(ctx, binding.target, predicate, lock, m.limit()+1)
	if err != nil {
		return nil, err
	}
	if len(rows) > m.limit() {
		return nil, errors.New("orm: relation exceeds configured bound")
	}
	return rows, nil
}
func (m RelationManager) All(ctx context.Context) ([]models.Record, error) {
	binding, err := m.resolve()
	if err != nil {
		return nil, err
	}
	source, err := m.source(ctx, false)
	if err != nil {
		return nil, err
	}
	return m.related(ctx, binding, source, false)
}
func (m RelationManager) One(ctx context.Context) (models.Record, error) {
	binding, err := m.resolve()
	if err != nil {
		return nil, err
	}
	rows, err := m.All(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		if !binding.reverse && binding.field.Null {
			return nil, nil
		}
		return nil, ErrNotFound
	}
	if len(rows) != 1 {
		return nil, ErrMultipleObjects
	}
	return rows[0], nil
}
func (m RelationManager) Add(ctx context.Context, targets ...models.Record) error {
	return m.change(ctx, "add", targets)
}
func (m RelationManager) Remove(ctx context.Context, targets ...models.Record) error {
	return m.change(ctx, "remove", targets)
}
func (m RelationManager) Clear(ctx context.Context) error { return m.change(ctx, "clear", nil) }
func (m RelationManager) Set(ctx context.Context, targets ...models.Record) error {
	return m.change(ctx, "set", targets)
}

func (m RelationManager) change(ctx context.Context, action string, targets []models.Record) error {
	binding, err := m.resolve()
	if err != nil {
		return err
	}
	if len(targets) > m.limit() {
		return errors.New("orm: relation input exceeds configured bound")
	}
	if binding.through != nil {
		return m.changeMany(ctx, binding, action, targets)
	}
	if !binding.reverse && (action == "add" || action == "remove" || len(targets) > 1) {
		return errors.New("orm: scalar forward relation supports Set or Clear")
	}
	if (action == "remove" || action == "clear" || !binding.reverse && len(targets) == 0) && !binding.field.Null {
		return errors.New("orm: nonnullable relation cannot be removed")
	}
	err = db.Atomic(ctx, m.Store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		source, err := m.source(ctx, true)
		if err != nil {
			return err
		}
		current, err := m.related(ctx, binding, source, true)
		if err != nil {
			return err
		}
		requested := []models.Record{}
		requestedIDs := map[string]bool{}
		provided := map[string]models.Record{}
		for _, target := range targets {
			if err := ctx.Err(); err != nil {
				return err
			}
			if target == nil || target.Schema().Key() != binding.target.Key() {
				return errors.New("orm: relation target model mismatch")
			}
			if target.State().Database != "" && target.State().Database != m.Store.Backend.Alias() {
				return errors.New("orm: cross-database relation rejected")
			}
			where, err := recordPK(target)
			if err != nil {
				return err
			}
			rows, err := m.reader().records(ctx, binding.target, where, true, 2)
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				return ErrNotFound
			}
			key, err := recordIdentity(rows[0])
			if err != nil {
				return err
			}
			if !requestedIDs[key] {
				requestedIDs[key] = true
				provided[key] = target
				requested = append(requested, rows[0])
			}
		}
		change := RelationChange{Action: action, Source: source, Field: binding.field}
		if !binding.reverse {
			change.Added = requested
			change.Removed = current
		} else {
			currentIDs := map[string]bool{}
			for _, record := range current {
				key, _ := recordIdentity(record)
				currentIDs[key] = true
				if action == "clear" || action == "remove" && requestedIDs[key] || action == "set" && binding.field.Null && !requestedIDs[key] {
					change.Removed = append(change.Removed, record)
				}
			}
			if action == "remove" {
				for key := range requestedIDs {
					if !currentIDs[key] {
						return errors.New("orm: object is not part of this relationship")
					}
				}
			}
			if action == "add" || action == "set" {
				for _, record := range requested {
					key, _ := recordIdentity(record)
					if !currentIDs[key] {
						change.Added = append(change.Added, record)
					}
				}
			}
		}
		change, err = m.checkChange(ctx, binding, change)
		if err != nil {
			return err
		}
		source = change.Source
		if !binding.reverse {
			requested = change.Added
		}
		if binding.reverse {
			key, err := relationTargetField(source.Schema(), binding.field)
			if err != nil {
				return err
			}
			value, err := source.Get(key.Name)
			if err != nil {
				return err
			}
			for _, record := range change.Removed {
				identity, _ := recordIdentity(record)
				if original := provided[identity]; original != nil {
					if err := original.Set(binding.field.Name, nil); err != nil {
						return err
					}
				}
				if err := m.reader().mutate(ctx, record, binding.field.Name, nil); err != nil {
					return err
				}
			}
			for _, record := range change.Added {
				identity, _ := recordIdentity(record)
				if original := provided[identity]; original != nil {
					if err := original.Set(binding.field.Name, value); err != nil {
						return err
					}
				}
				if err := m.reader().mutate(ctx, record, binding.field.Name, value); err != nil {
					return err
				}
			}
		} else {
			var value any
			if len(requested) == 1 {
				key, err := relationTargetField(binding.target, binding.field)
				if err != nil {
					return err
				}
				value, err = requested[0].Get(key.Name)
				if err != nil {
					return err
				}
			}
			if err := m.Source.Set(binding.field.Name, value); err != nil {
				return err
			}
			if err := m.reader().mutate(ctx, source, binding.field.Name, value); err != nil {
				return err
			}
		}
		for _, receiver := range m.AfterChange {
			if err := receiver(ctx, change); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		m.Source.State().Related = nil
		for _, target := range targets {
			target.State().Related = nil
		}
	}
	return err
}
