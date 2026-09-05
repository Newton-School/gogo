package orm

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/internal/sqlcompiler"
)

var ErrProtectedRelation = errors.New("orm: protected relationship prevents deletion")
var ErrDeleteLimit = errors.New("orm: deletion graph exceeds its object or work limit; narrow the scoped selection or configure an explicit collector limit")

const defaultDeleteLimit = 10000

type DeleteEvent struct{ Record models.Record }
type DeleteReceiver func(context.Context, DeleteEvent) error
type FieldUpdate struct {
	Record models.Record
	Field  string
	Value  any
}
type ProtectedRelation struct {
	Record models.Record
	Field  string
	Policy models.DeletePolicy
}
type DeletionPlan struct {
	Objects   []models.Record
	Updates   []FieldUpdate
	Protected []ProtectedRelation
}

// DeleteCollector uses the same scoped graph for preview and execution. Scope
// must carry application tenant/authorization constraints for every schema.
// Execution recollects inside Atomic; it never trusts a stale preview plan.
type DeleteCollector struct {
	Store *Store
	Scope func(context.Context, models.Schema) (db.Predicate, error)
	// Authorize checks the freshly locked execution graph, including updates.
	// It must not mutate the plan and may return any application policy error.
	Authorize  func(context.Context, DeletionPlan) error
	MaxObjects int
	MaxWork    int
}

func (c DeleteCollector) Collect(ctx context.Context, root models.Record) (DeletionPlan, error) {
	return c.collect(ctx, []models.Record{root}, false)
}

func (c DeleteCollector) collect(ctx context.Context, roots []models.Record, lock bool) (DeletionPlan, error) {
	plan := DeletionPlan{}
	if err := ctx.Err(); err != nil {
		return plan, err
	}
	if c.Store == nil || c.Store.Backend == nil || c.Store.Registry == nil {
		return plan, errors.New("orm: delete collector requires backend and complete model registry")
	}
	maximum := c.MaxObjects
	if maximum <= 0 {
		maximum = defaultDeleteLimit
	}
	workLimit := c.MaxWork
	if workLimit <= 0 {
		workLimit = maximum
		if maximum <= int(^uint(0)>>1)/32 {
			workLimit = maximum * 32
		}
	}
	work := 0
	tick := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		work++
		if work > workLimit {
			return ErrDeleteLimit
		}
		return nil
	}
	fetched := map[string]bool{}
	track := func(record models.Record) error {
		if err := tick(); err != nil {
			return err
		}
		key, err := recordIdentity(record)
		if err != nil {
			return err
		}
		if !fetched[key] {
			if len(fetched) >= maximum {
				return ErrDeleteLimit
			}
			fetched[key] = true
		}
		return nil
	}
	seen := map[string]bool{}
	var restrictions []ProtectedRelation
	schemas := c.Store.Registry.All()
	var visit func(models.Record) error
	visit = func(record models.Record) error {
		if err := tick(); err != nil {
			return err
		}
		key, err := recordIdentity(record)
		if err != nil {
			return err
		}
		if seen[key] {
			return nil
		}
		if len(seen) >= maximum {
			return ErrDeleteLimit
		}
		seen[key] = true
		for _, schema := range schemas {
			for _, field := range schema.Fields {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !field.IsStored() || field.Relation == nil || field.Relation.Target != record.Schema().Key() {
					continue
				}
				if field.Relation.NoConstraint {
					return errors.New("orm: unconstrained relations require an explicit application deletion policy")
				}
				if err := tick(); err != nil {
					return err
				}
				keys := field.Relation.TargetFields
				if len(keys) == 0 {
					for _, pk := range record.Schema().PKFields() {
						keys = append(keys, pk.Name)
					}
				}
				if len(keys) != 1 {
					return errors.New("orm: scalar relation requires one target field")
				}
				value, err := record.Get(keys[0])
				if err != nil {
					return err
				}
				children, err := c.records(ctx, schema, Q(field.Name, value), lock, maximum+1)
				if err != nil {
					return err
				}
				if len(children) > maximum {
					return ErrDeleteLimit
				}
				for _, child := range children {
					if err := track(child); err != nil {
						return err
					}
					switch field.Relation.OnDelete {
					case models.Cascade:
						if err := visit(child); err != nil {
							return err
						}
					case models.Protect:
						plan.Protected = append(plan.Protected, ProtectedRelation{child, field.Name, models.Protect})
					case models.Restrict:
						restrictions = append(restrictions, ProtectedRelation{child, field.Name, models.Restrict})
					case models.SetNull:
						if !field.Null {
							return errors.New("orm: SET_NULL requires nullable relation")
						}
						plan.Updates = append(plan.Updates, FieldUpdate{child, field.Name, nil})
					case models.SetDefault:
						value := field.Default
						if field.DefaultFunc != nil {
							value = field.DefaultFunc()
						}
						if value == nil && !field.Null {
							return errors.New("orm: SET_DEFAULT requires valid default")
						}
						if err := field.Validate(ctx, value); err != nil {
							return err
						}
						plan.Updates = append(plan.Updates, FieldUpdate{child, field.Name, value})
					case models.DoNothing:
						// Deferred database FK remains authoritative at commit.
					default:
						return errors.New("orm: relation requires an explicit OnDelete policy")
					}
				}
			}
		}
		plan.Objects = append(plan.Objects, record)
		return nil
	}
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		if root == nil {
			return plan, errors.New("orm: nil delete root")
		}
		if root.State().Database != "" && root.State().Database != c.Store.Backend.Alias() {
			return plan, errors.New("orm: cross-database deletion rejected")
		}
		predicate, err := recordPK(root)
		if err != nil {
			return plan, err
		}
		rows, err := c.records(ctx, root.Schema(), predicate, lock, 2)
		if err != nil {
			return plan, err
		}
		if len(rows) != 1 {
			return plan, ErrNotFound
		}
		if err := track(rows[0]); err != nil {
			return plan, err
		}
		if err := visit(rows[0]); err != nil {
			return plan, err
		}
	}
	for _, restriction := range restrictions {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		key, err := recordIdentity(restriction.Record)
		if err != nil {
			return plan, err
		}
		if !seen[key] {
			plan.Protected = append(plan.Protected, restriction)
		}
	}
	updates := plan.Updates[:0]
	for _, update := range plan.Updates {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		key, err := recordIdentity(update.Record)
		if err != nil {
			return plan, err
		}
		if !seen[key] {
			updates = append(updates, update)
		}
	}
	plan.Updates = updates
	return plan, nil
}

func (c DeleteCollector) records(ctx context.Context, schema models.Schema, where db.Predicate, lock bool, maximum int) ([]models.Record, error) {
	if c.Scope != nil {
		scope, err := c.Scope(ctx, schema)
		if err != nil {
			return nil, err
		}
		where = And(where, scope)
	}
	query := db.Select{Table: schema.DBTable(), Where: where, ForUpdate: lock, Limit: &maximum}
	for _, field := range schema.Fields {
		if field.IsStored() {
			query.Fields = append(query.Fields, field.Name)
		}
	}
	for _, key := range schema.PKFields() {
		query.Order = append(query.Order, db.Order{Field: key.Name})
	}
	statement, args, err := sqlcompiler.Select(c.Store.Backend.Dialect(), schema, query)
	if err != nil {
		return nil, err
	}
	rows, err := db.ExecutorFor(ctx, c.Store.Backend).Query(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []models.Record{}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		values := make([]any, len(query.Fields))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		record, err := models.NewRecord(schema)
		if err != nil {
			return nil, err
		}
		for i, name := range query.Fields {
			field, _ := schema.Field(name)
			value, err := decodeField(field, values[i])
			if err != nil {
				return nil, err
			}
			if err := record.Set(name, value); err != nil {
				return nil, err
			}
		}
		record.State().Persisted = true
		record.State().Database = c.Store.Backend.Alias()
		result = append(result, record)
	}
	return result, rows.Err()
}

func recordPK(record models.Record) (db.Predicate, error) {
	parts := []db.Predicate{}
	for _, pk := range record.Schema().PKFields() {
		value, err := record.Get(pk.Name)
		if err != nil {
			return db.Predicate{}, err
		}
		if value == nil {
			return db.Predicate{}, errors.New("orm: deletion requires a primary key")
		}
		parts = append(parts, Q(pk.Name, value))
	}
	if len(parts) == 0 {
		return db.Predicate{}, errors.New("orm: deletion requires a primary key")
	}
	return And(parts...), nil
}
func recordIdentity(record models.Record) (string, error) {
	values := []any{}
	for _, pk := range record.Schema().PKFields() {
		value, err := record.Get(pk.Name)
		if err != nil {
			return "", err
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return "", errors.New("orm: missing primary key")
	}
	encoded, err := json.Marshal(values)
	return record.Schema().Key() + ":" + string(encoded), err
}

func (c DeleteCollector) Execute(ctx context.Context, root models.Record) (map[string]int64, error) {
	return c.execute(ctx, []models.Record{root})
}
func (c DeleteCollector) execute(ctx context.Context, roots []models.Record) (map[string]int64, error) {
	counts := map[string]int64{}
	if c.Store == nil || c.Store.Backend == nil {
		return nil, errors.New("orm: backend required")
	}
	err := db.Atomic(ctx, c.Store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		plan, err := c.collect(ctx, roots, true)
		if err != nil {
			return err
		}
		if len(plan.Protected) > 0 {
			return ErrProtectedRelation
		}
		if c.Authorize != nil {
			if err := c.Authorize(ctx, plan); err != nil {
				return err
			}
		}
		for _, record := range plan.Objects {
			for _, receiver := range c.Store.BeforeDelete {
				if err := receiver(ctx, DeleteEvent{Record: record}); err != nil {
					return err
				}
			}
		}
		for _, update := range plan.Updates {
			if err := c.mutate(ctx, update.Record, update.Field, update.Value); err != nil {
				return err
			}
		}
		for _, record := range plan.Objects {
			if err := c.mutate(ctx, record, "", nil); err != nil {
				return err
			}
			counts[record.Schema().Key()]++
			for _, receiver := range c.Store.AfterDelete {
				if err := receiver(ctx, DeleteEvent{Record: record}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, root := range roots {
		root.State().Persisted = false
		root.State().Related = nil
		for _, pk := range root.Schema().PKFields() {
			value, err := root.Get(pk.Name)
			if err == nil && value != nil {
				_ = root.Set(pk.Name, reflect.Zero(reflect.TypeOf(value)).Interface())
			}
		}
	}
	return counts, nil
}

func (c DeleteCollector) mutate(ctx context.Context, record models.Record, fieldName string, value any) error {
	schema := record.Schema()
	predicate, err := recordPK(record)
	if err != nil {
		return err
	}
	if c.Scope != nil {
		scope, err := c.Scope(ctx, schema)
		if err != nil {
			return err
		}
		predicate = And(predicate, scope)
	}
	compiler := sqlcompiler.Compiler{Dialect: c.Store.Backend.Dialect(), Schema: schema}
	where, err := compiler.Predicate(predicate)
	if err != nil {
		return err
	}
	table, err := c.Store.Backend.Dialect().QuoteIdentifier(schema.DBTable())
	if err != nil {
		return err
	}
	statement := "DELETE FROM " + table + " WHERE " + where
	if fieldName != "" {
		field, ok := schema.Field(fieldName)
		if !ok {
			return errors.New("orm: invalid relation update field")
		}
		column, err := c.Store.Backend.Dialect().QuoteIdentifier(field.DBColumn())
		if err != nil {
			return err
		}
		encoded, err := encodeField(field, value)
		if err != nil {
			return err
		}
		compiler.Args = append(compiler.Args, encoded)
		statement = "UPDATE " + table + " SET " + column + "=" + c.Store.Backend.Dialect().Placeholder(len(compiler.Args)) + " WHERE " + where
	}
	result, err := db.ExecutorFor(ctx, c.Store.Backend).Exec(ctx, statement, compiler.Args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotUpdated
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, model models.Model) (map[string]int64, error) {
	record, err := models.Bind(model)
	if err != nil {
		return nil, err
	}
	return (DeleteCollector{Store: s}).Execute(ctx, record)
}

func (q Query[T]) Delete(ctx context.Context) (map[string]int64, error) {
	if q.err != nil {
		return nil, q.err
	}
	if q.selectAST.Limit != nil || q.selectAST.Offset != nil || len(q.selectAST.DistinctOn) > 0 {
		return nil, errors.New("orm: delete requires an unsliced query without DISTINCT ON")
	}
	var counts map[string]int64
	err := db.Atomic(ctx, q.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		modelsFound, err := q.Limit(defaultDeleteLimit+1).SelectForUpdate(false, false).All(ctx)
		if err != nil {
			return err
		}
		if len(modelsFound) > defaultDeleteLimit {
			return ErrDeleteLimit
		}
		records := make([]models.Record, 0, len(modelsFound))
		for _, model := range modelsFound {
			if err := ctx.Err(); err != nil {
				return err
			}
			record, err := models.Bind(model)
			if err != nil {
				return err
			}
			records = append(records, record)
		}
		// Consistent root order narrows deadlock opportunities for overlapping deletes.
		sort.Slice(records, func(i, j int) bool {
			a, _ := recordIdentity(records[i])
			b, _ := recordIdentity(records[j])
			return a < b
		})
		counts, err = (DeleteCollector{Store: q.store}).execute(ctx, records)
		return err
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}
