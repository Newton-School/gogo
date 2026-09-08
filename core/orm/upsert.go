package orm

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// UniqueKey names a database-enforced unique constraint or unique index from
// Schema.Constraints/Indexes. Values must cover its exact, unconditional key.
type UniqueKey struct {
	Constraint string
	Values     map[string]any
}
type Defaults map[string]any
type DefaultFactory func(context.Context) (any, error)

func (q Query[T]) GetOrCreate(ctx context.Context, key UniqueKey, defaults Defaults) (T, bool, error) {
	if e := q.store.refuseRouting(); e != nil {
		var zero T
		return zero, false, e
	}
	return q.getOrCreate(ctx, key, defaults, nil, false)
}
func (q Query[T]) UpdateOrCreate(ctx context.Context, key UniqueKey, defaults Defaults, createDefaults ...Defaults) (T, bool, error) {
	if e := q.store.refuseRouting(); e != nil {
		var zero T
		return zero, false, e
	}
	if len(createDefaults) > 1 {
		var zero T
		return zero, false, errors.New("orm: at most one creation defaults map is allowed")
	}
	creation := defaults
	if len(createDefaults) == 1 {
		creation = createDefaults[0]
	}
	return q.getOrCreate(ctx, key, creation, defaults, true)
}
func (q Query[T]) getOrCreate(ctx context.Context, key UniqueKey, creation, updates Defaults, update bool) (T, bool, error) {
	var result T
	created := false
	if q.err != nil {
		return result, false, q.err
	}
	if q.selectAST.Limit != nil || q.selectAST.Offset != nil || q.selectAST.Distinct || len(q.selectAST.DistinctOn) > 0 {
		return result, false, errors.New("orm: create-or-update requires an unsliced ordinary query")
	}
	if err := checkUniqueKey(q.schema, key); err != nil {
		return result, false, err
	}
	query := q
	for _, name := range sortedValueKeys(key.Values) {
		query = query.Filter(Q(name, key.Values[name]))
	}
	err := db.Atomic(ctx, q.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		read := query
		if update {
			read = read.SelectForUpdate(false, false)
		}
		found, err := read.Get(ctx)
		if err == nil {
			result = found
			return q.updateExisting(ctx, result, updates, update)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		candidate := q.factory()
		record, err := models.Bind(candidate)
		if err != nil {
			return err
		}
		for _, name := range sortedValueKeys(key.Values) {
			if err := record.Set(name, key.Values[name]); err != nil {
				return err
			}
		}
		if _, err := prepareDefaults(ctx, record, creation, key.Values); err != nil {
			return err
		}
		// The inner Atomic is a PostgreSQL race savepoint, not a retry loop.
		// A failed INSERT must be rolled back before re-reading the winner.
		err = db.Atomic(ctx, q.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error { return q.store.Save(ctx, candidate, SaveOptions{ForceInsert: true}) })
		if err == nil {
			pk, err := recordPK(record)
			if err != nil {
				return err
			}
			allowed, err := q.Filter(pk).Exists(ctx)
			if err != nil {
				return err
			}
			if !allowed {
				return ErrNotFound
			}
			result = candidate
			created = true
			return nil
		}
		var integrity *db.Error
		if !errors.As(err, &integrity) || integrity.Code != db.UniqueViolation || integrity.Constraint != key.Constraint {
			return err
		}
		found, err = read.Get(ctx)
		if err != nil {
			return err
		}
		result = found
		return q.updateExisting(ctx, result, updates, update)
	})
	if err != nil {
		var zero T
		return zero, false, err
	}
	return result, created, nil
}
func (q Query[T]) updateExisting(ctx context.Context, model T, defaults Defaults, update bool) error {
	if !update {
		return nil
	}
	record, err := models.Bind(model)
	if err != nil {
		return err
	}
	fields, err := prepareDefaults(ctx, record, defaults, nil)
	if err != nil {
		return err
	}
	for _, field := range record.Schema().Fields {
		if field.AutoNow {
			present := false
			for _, name := range fields {
				present = present || name == field.Name
			}
			if !present {
				fields = append(fields, field.Name)
			}
		}
	}
	if err := q.store.Save(ctx, model, SaveOptions{UpdateFields: fields}); err != nil {
		return err
	}
	pk, err := recordPK(record)
	if err != nil {
		return err
	}
	allowed, err := q.Filter(pk).Exists(ctx)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotFound
	}
	return nil
}
func prepareDefaults(ctx context.Context, record models.Record, defaults Defaults, lookup map[string]any) ([]string, error) {
	fields := make([]string, 0, len(defaults))
	for _, name := range sortedValueKeys(defaults) {
		field, ok := record.Schema().Field(name)
		if !ok || !field.IsStored() || field.IsAuto() || field.Kind == models.Generated {
			return nil, errors.New("orm: invalid default field")
		}
		value := defaults[name]
		switch factory := value.(type) {
		case DefaultFactory:
			var err error
			value, err = factory(ctx)
			if err != nil {
				return nil, err
			}
		case func() any:
			value = factory()
		}
		if expected, ok := lookup[name]; ok && !reflect.DeepEqual(expected, value) {
			return nil, errors.New("orm: creation defaults cannot change the unique lookup key")
		}
		if err := record.Set(name, value); err != nil {
			return nil, err
		}
		fields = append(fields, name)
	}
	return fields, nil
}
func sortedValueKeys[M ~map[string]any](values M) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func checkUniqueKey(schema models.Schema, key UniqueKey) error {
	var fields []string
	nullsEqual := false
	for _, constraint := range schema.Constraints {
		if constraint.Name == key.Constraint && strings.EqualFold(constraint.Kind, "unique") && constraint.Condition == "" && !constraint.Deferrable {
			fields = constraint.Fields
			nullsEqual = constraint.NullsDistinct != nil && !*constraint.NullsDistinct
		}
	}
	for _, index := range schema.Indexes {
		if index.Name == key.Constraint && index.Unique && index.Condition == "" {
			fields = index.Fields
			nullsEqual = index.NullsDistinct != nil && !*index.NullsDistinct
		}
	}
	if len(fields) == 0 || len(fields) != len(key.Values) {
		return errors.New("orm: operation requires an exact named, unconditional, immediate unique database key")
	}
	for _, name := range fields {
		value, ok := key.Values[name]
		if !ok || value == nil && !nullsEqual {
			return errors.New("orm: unique lookup is incomplete or NULL is not uniquely constrained")
		}
	}
	return nil
}
