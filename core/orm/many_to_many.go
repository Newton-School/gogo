package orm

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type throughBinding struct {
	schema         models.Schema
	source, target models.Field
	automatic      bool
	symmetrical    bool
}

func (m RelationManager) bindThrough(binding relationBinding, owner models.Schema) (relationBinding, error) {
	relation := binding.field.Relation
	target, ok := m.Store.Registry.Get(relation.Target)
	if !ok {
		return binding, errors.New("orm: many-to-many target missing")
	}
	symmetrical := owner.Key() == target.Key() && (relation.Symmetrical == nil || *relation.Symmetrical)
	var through models.Schema
	names := relation.ThroughFields
	automatic := relation.Through == ""
	if automatic {
		definition, err := models.ImplicitThrough(owner, binding.field, target)
		if err != nil {
			return binding, err
		}
		through, ok = m.Store.Registry.Get(definition.Key())
		if !ok || !m.Store.Registry.IsAutomatic(definition.Key()) {
			return binding, errors.New("orm: registry must be frozen before using automatic intermediaries")
		}
		names = []string{"source_id", "target_id"}
	} else {
		through, ok = m.Store.Registry.Get(relation.Through)
		if !ok {
			return binding, errors.New("orm: explicit intermediary missing")
		}
		if len(names) == 0 {
			if owner.Key() == target.Key() {
				for _, field := range through.Fields {
					if field.IsStored() && field.Relation != nil && field.Relation.Target == owner.Key() {
						names = append(names, field.Name)
					}
				}
				if len(names) != 2 {
					return binding, errors.New("orm: ambiguous self intermediary requires exact ThroughFields")
				}
			}
		}
		if len(names) == 0 {
			for _, endpoint := range []string{owner.Key(), target.Key()} {
				matches := []string{}
				for _, field := range through.Fields {
					if field.IsStored() && field.Relation != nil && field.Relation.Target == endpoint {
						matches = append(matches, field.Name)
					}
				}
				if len(matches) != 1 {
					return binding, errors.New("orm: ambiguous intermediary requires exact ThroughFields")
				}
				names = append(names, matches[0])
			}
		}
	}
	if len(names) != 2 || names[0] == names[1] {
		return binding, errors.New("orm: ThroughFields requires distinct source and target fields")
	}
	sourceField, sourceOK := through.Field(names[0])
	targetField, targetOK := through.Field(names[1])
	if !sourceOK || !targetOK || sourceField.Relation == nil || targetField.Relation == nil || sourceField.Relation.Target != owner.Key() || targetField.Relation.Target != target.Key() {
		return binding, errors.New("orm: intermediary endpoints do not match relation")
	}
	if _, err := relationTargetField(owner, sourceField); err != nil {
		return binding, err
	}
	if _, err := relationTargetField(target, targetField); err != nil {
		return binding, err
	}
	if binding.reverse {
		sourceField, targetField = targetField, sourceField
	}
	binding.through = &throughBinding{schema: through, source: sourceField, target: targetField, automatic: automatic, symmetrical: symmetrical}
	return binding, nil
}

func (m RelationManager) throughReader(binding relationBinding) DeleteCollector {
	reader := m.reader()
	if binding.through.automatic {
		reader.Scope = nil
	}
	return reader
}

func (m RelationManager) checkedThroughRecords(ctx context.Context, binding relationBinding, where db.Predicate, lock bool, maximum int) ([]models.Record, error) {
	rows, err := m.throughReader(binding).records(ctx, binding.through.schema, where, lock, maximum)
	if err != nil {
		return nil, err
	}
	if binding.through.symmetrical && !binding.through.automatic && m.Scope != nil {
		// A symmetric operation cannot apply only its visible half. These internal
		// exact-endpoint checks disclose no hidden row identity to the caller.
		reader := m.reader()
		reader.Scope = nil
		all, err := reader.records(ctx, binding.through.schema, where, lock, maximum)
		if err != nil {
			return nil, err
		}
		visible := map[string]bool{}
		for _, row := range rows {
			id, err := recordIdentity(row)
			if err != nil {
				return nil, err
			}
			visible[id] = true
		}
		for _, row := range all {
			id, err := recordIdentity(row)
			if err != nil {
				return nil, err
			}
			if !visible[id] {
				return nil, ErrNotFound
			}
		}
	}
	return rows, nil
}

// manyRelated always scopes both application endpoints. Automatic link rows are
// constrained by the exact source FK; explicit intermediary rows also use Scope.
func (m RelationManager) manyRelated(ctx context.Context, binding relationBinding, source models.Record, lock bool) ([]models.Record, []models.Record, error) {
	through := binding.through
	sourceKey, err := relationTargetField(source.Schema(), through.source)
	if err != nil {
		return nil, nil, err
	}
	sourceValue, err := source.Get(sourceKey.Name)
	if err != nil {
		return nil, nil, err
	}
	links, err := m.checkedThroughRecords(ctx, binding, Q(through.source.Name, sourceValue), lock, m.limit()+1)
	if err != nil {
		return nil, nil, err
	}
	if len(links) > m.limit() {
		return nil, nil, errors.New("orm: intermediary selection exceeds configured bound")
	}
	if len(links) == 0 {
		return []models.Record{}, links, nil
	}
	values := make([]any, 0, len(links))
	for _, link := range links {
		value, err := link.Get(through.target.Name)
		if err != nil {
			return nil, nil, err
		}
		values = append(values, value)
	}
	targetKey, err := relationTargetField(binding.target, through.target)
	if err != nil {
		return nil, nil, err
	}
	rows, err := m.reader().records(ctx, binding.target, Q(targetKey.Name+"__in", values), lock, m.limit()+1)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) > m.limit() {
		return nil, nil, errors.New("orm: relation target selection exceeds configured bound")
	}
	return rows, links, nil
}

func (m RelationManager) scopedTarget(ctx context.Context, schema models.Schema, record models.Record) (models.Record, error) {
	if record == nil || record.Schema().Key() != schema.Key() {
		return nil, errors.New("orm: relation target model mismatch")
	}
	if record.State().Database != "" && record.State().Database != m.Store.Backend.Alias() {
		return nil, errors.New("orm: cross-database relation rejected")
	}
	pk, err := recordPK(record)
	if err != nil {
		return nil, err
	}
	rows, err := m.reader().records(ctx, schema, pk, true, 2)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, ErrNotFound
	}
	return rows[0], nil
}

func (m RelationManager) changeMany(ctx context.Context, binding relationBinding, action string, targets []models.Record) error {
	err := db.Atomic(ctx, m.Store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		source, err := m.source(ctx, true)
		if err != nil {
			return err
		}
		current, links, err := m.manyRelated(ctx, binding, source, true)
		if err != nil {
			return err
		}
		requested := []models.Record{}
		requestedIDs := map[string]bool{}
		for _, target := range targets {
			if err := ctx.Err(); err != nil {
				return err
			}
			fresh, err := m.scopedTarget(ctx, binding.target, target)
			if err != nil {
				return err
			}
			id, err := recordIdentity(fresh)
			if err != nil {
				return err
			}
			if !requestedIDs[id] {
				requestedIDs[id] = true
				requested = append(requested, fresh)
			}
		}
		change := RelationChange{Action: action, Source: source, Field: binding.field}
		currentIDs := map[string]bool{}
		for _, record := range current {
			id, err := recordIdentity(record)
			if err != nil {
				return err
			}
			currentIDs[id] = true
			if action == "clear" || action == "remove" && requestedIDs[id] || action == "set" && !requestedIDs[id] {
				change.Removed = append(change.Removed, record)
			}
		}
		if action == "add" || action == "set" {
			for _, record := range requested {
				id, _ := recordIdentity(record)
				if !currentIDs[id] {
					change.Added = append(change.Added, record)
				}
			}
		}
		change, err = m.checkChange(ctx, binding, change)
		if err != nil {
			return err
		}
		source = change.Source
		through := binding.through
		sourceKey, err := relationTargetField(source.Schema(), through.source)
		if err != nil {
			return err
		}
		sourceValue, err := source.Get(sourceKey.Name)
		if err != nil {
			return err
		}
		targetKey, err := relationTargetField(binding.target, through.target)
		if err != nil {
			return err
		}
		removedValues := map[string]bool{}
		for _, record := range change.Removed {
			value, err := record.Get(targetKey.Name)
			if err != nil {
				return err
			}
			key, err := scalarKey(value)
			if err != nil {
				return err
			}
			removedValues[key] = true
		}
		for _, link := range links {
			value, err := link.Get(through.target.Name)
			if err != nil {
				return err
			}
			key, err := scalarKey(value)
			if err != nil {
				return err
			}
			if !removedValues[key] {
				continue
			}
			reader := m.throughReader(binding)
			previous := reader.Scope
			reader.Scope = func(ctx context.Context, schema models.Schema) (db.Predicate, error) {
				where := And(Q(through.source.Name, sourceValue), Q(through.target.Name, value))
				if previous != nil {
					scope, err := previous(ctx, schema)
					if err != nil {
						return db.Predicate{}, err
					}
					where = And(where, scope)
				}
				return where, nil
			}
			if err := reader.mutate(ctx, link, "", nil); err != nil {
				return err
			}
		}
		if through.symmetrical {
			mirrorWork := len(links)
			for _, target := range change.Removed {
				mirrorSource, err := target.Get(sourceKey.Name)
				if err != nil {
					return err
				}
				mirrorTarget, err := source.Get(targetKey.Name)
				if err != nil {
					return err
				}
				pair := And(Q(through.source.Name, mirrorSource), Q(through.target.Name, mirrorTarget))
				mirrors, err := m.checkedThroughRecords(ctx, binding, pair, true, m.limit()-mirrorWork+1)
				if err != nil {
					return err
				}
				mirrorWork += len(mirrors)
				if mirrorWork > m.limit() {
					return errors.New("orm: symmetric intermediary selection exceeds configured bound")
				}
				for _, mirror := range mirrors {
					reader := m.throughReader(binding)
					previous := reader.Scope
					reader.Scope = func(ctx context.Context, schema models.Schema) (db.Predicate, error) {
						if previous == nil {
							return pair, nil
						}
						scope, err := previous(ctx, schema)
						return And(pair, scope), err
					}
					if err := reader.mutate(ctx, mirror, "", nil); err != nil {
						return err
					}
				}
			}
		}
		if len(change.Added) > 0 {
			defaults := Defaults{}
			for _, name := range sortedValueKeys(m.ThroughDefaults) {
				field, ok := through.schema.Field(name)
				if !ok || !field.IsStored() || field.PrimaryKey || field.Kind == models.Generated || name == through.source.Name || name == through.target.Name {
					return errors.New("orm: invalid intermediary default field")
				}
				value := m.ThroughDefaults[name]
				if factory, ok := value.(DefaultFactory); ok {
					value, err = factory(ctx)
					if err != nil {
						return err
					}
				}
				defaults[name] = value
			}
			inserts := []*models.MapRecord{}
			insertPairs := map[string]bool{}
			for _, target := range change.Added {
				value, err := target.Get(targetKey.Name)
				if err != nil {
					return err
				}
				pairs := [][2]any{{sourceValue, value}}
				if through.symmetrical {
					mirrorSource, err := target.Get(sourceKey.Name)
					if err != nil {
						return err
					}
					mirrorTarget, err := source.Get(targetKey.Name)
					if err != nil {
						return err
					}
					pairs = append(pairs, [2]any{mirrorSource, mirrorTarget})
				}
				for _, pair := range pairs {
					key, err := scalarKey(pair)
					if err != nil {
						return err
					}
					if insertPairs[key] {
						continue
					}
					insertPairs[key] = true
					if len(inserts) >= m.limit() {
						return errors.New("orm: intermediary insert exceeds configured bound")
					}
					if through.symmetrical && !through.automatic {
						existing, err := m.checkedThroughRecords(ctx, binding, And(Q(through.source.Name, pair[0]), Q(through.target.Name, pair[1])), true, m.limit()+1)
						if err != nil {
							return err
						}
						if len(existing) > 0 {
							continue
						}
					}
					row, err := prepareThroughRow(ctx, through, pair, defaults)
					if err != nil {
						return err
					}
					inserts = append(inserts, row)
				}
			}
			outcomes, err := BulkCreate(ctx, m.Store, inserts, BulkOptions{IgnoreConflicts: through.automatic})
			if err != nil {
				return err
			}
			if !through.automatic {
				for _, outcome := range outcomes {
					where, err := recordPK(outcome.Model)
					if err != nil {
						return err
					}
					rows, err := m.throughReader(binding).records(ctx, through.schema, where, true, 2)
					if err != nil {
						return err
					}
					if len(rows) != 1 {
						return ErrNotFound
					}
				}
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

func scalarKey(value any) (string, error) {
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func prepareThroughRow(ctx context.Context, through *throughBinding, pair [2]any, defaults Defaults) (*models.MapRecord, error) {
	row, err := models.NewRecord(through.schema)
	if err != nil {
		return nil, err
	}
	for name, value := range defaults {
		if err := row.Set(name, value); err != nil {
			return nil, err
		}
	}
	if err := row.Set(through.source.Name, pair[0]); err != nil {
		return nil, err
	}
	if err := row.Set(through.target.Name, pair[1]); err != nil {
		return nil, err
	}
	if err := models.ApplyDefaults(row); err != nil {
		return nil, err
	}
	for _, field := range through.schema.Fields {
		if !field.IsStored() || field.IsAuto() || field.Kind == models.Generated || field.AutoNow || field.AutoNowAdd || field.DBDefault != "" {
			continue
		}
		value, err := row.Get(field.Name)
		if err != nil {
			return nil, err
		}
		if err := field.Validate(ctx, value); err != nil {
			return nil, err
		}
	}
	return row, nil
}
