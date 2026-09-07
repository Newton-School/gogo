package api

import (
	"context"
	"errors"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type deleteTarget struct {
	schema   models.Schema
	key      Values
	snapshot string
}

func deletionDescriptor(schema models.Schema) error {
	if schema.Unmanaged || schema.Proxy || schema.Abstract || schema.Parent != "" {
		return errors.New("api: deletion graph requires concrete managed models")
	}
	for _, field := range schema.Fields {
		if field.IsStored() && !deletionBuiltinField(field) {
			return errors.New("api: deletion effect fences require built-in field codecs")
		}
	}
	return nil
}

func deletionBuiltinField(field models.Field) bool {
	for depth := 0; depth < 64; depth++ {
		if field.Codec != nil || field.Kind == models.Custom {
			return false
		}
		if field.Element == nil {
			return true
		}
		field = *field.Element
	}
	return false
}

func (s *Resource) checkDeleteGraph(ctx context.Context, plan orm.DeletionPlan, options DeleteOptions) (targets []deleteTarget, err error) {
	err = orm.CheckDeletionPlan(ctx, plan, func(ctx context.Context, view orm.DeletionPlan) error {
		p := auth.FromContext(ctx)
		authorize := func(action string, record models.Record) error {
			schema := record.Schema()
			if err := deletionDescriptor(schema); err != nil {
				return err
			}
			key, err := recordIdentity(record)
			if err != nil {
				return err
			}
			return s.config.Policy.Authorize(ctx, p, action, auth.Resource{App: schema.AppLabel, Model: schema.Name, ID: key, Object: record})
		}
		for _, record := range view.Objects {
			if err := authorize("delete", record); err != nil {
				return err
			}
		}
		for _, update := range view.Updates {
			if err := authorize("change", update.Record); err != nil {
				return err
			}
			if update.Value == nil {
				continue
			}
			field, ok := update.Record.Schema().Field(update.Field)
			if !ok || field.Relation == nil {
				return errors.New("api: invalid deletion relation update")
			}
			schema, ok := s.config.Store.Registry.Get(field.Relation.Target)
			if !ok {
				return errors.New("api: deletion relation target is not registered")
			}
			if err := deletionDescriptor(schema); err != nil {
				return err
			}
			fields := field.Relation.TargetFields
			if len(fields) == 0 {
				for _, key := range schema.PKFields() {
					fields = append(fields, key.Name)
				}
			}
			if len(fields) != 1 {
				return errors.New("api: deletion update target requires one unique field")
			}
			scope, err := s.config.Scope(ctx, p, schema.Clone())
			if err != nil {
				return err
			}
			query := deletionQuery(s.config.Store, schema).Filter(scope, orm.Q(fields[0], update.Value)).SelectForUpdate(false, false)
			target, err := query.Get(ctx)
			if err != nil {
				return err
			}
			if err := readOnlyRecord(target, func() error { return authorize("view", target) }); err != nil {
				return err
			}
			key, err := recordIdentity(target)
			if err != nil {
				return err
			}
			key, err = receiptObject(key)
			if err != nil {
				return err
			}
			stamp, err := recordSnapshot(target)
			if err != nil {
				return err
			}
			targets = append(targets, deleteTarget{schema: schema, key: key, snapshot: stamp})
		}
		for _, join := range view.JoinRemovals {
			if !s.config.Store.Registry.IsAutomatic(join.Record.Schema().Key()) {
				return errors.New("api: invalid automatic deletion join")
			}
			if err := deletionDescriptor(join.Record.Schema()); err != nil {
				return err
			}
			// Synthetic intermediaries derive authority from their deletion
			// endpoint. They have no application tenant column or model grant.
			if err := authorize("delete", join.Endpoint); err != nil {
				return err
			}
		}
		return options.ValidateDelete(ctx, p, view)
	})
	return targets, err
}

func deletionQuery(store *orm.Store, schema models.Schema) orm.Query[*models.MapRecord] {
	return orm.For(store, func() *models.MapRecord { row, _ := models.NewRecord(schema); return row }).OrderBy()
}

func deletionKeyQuery(store *orm.Store, schema models.Schema, key Values) (orm.Query[*models.MapRecord], error) {
	query := deletionQuery(store, schema)
	if len(key) != len(schema.PKFields()) {
		return query, errors.New("api: invalid deletion effect identity")
	}
	for _, field := range schema.PKFields() {
		value, exists := key[field.Name]
		if !exists || value == nil {
			return query, errors.New("api: invalid deletion effect identity")
		}
		query = query.Filter(orm.Q(field.Name, value))
	}
	return query, nil
}

// Every callback-bearing scope is prepared before these exact final reads.
// Deleted rows and automatic joins use private unscoped existence checks: a
// recreated out-of-scope row must not be misreported as successful deletion.
// No row, identity or count from those checks is returned to the client.
func (s *Resource) fenceDeletion(ctx context.Context, plan orm.DeletionPlan, targets []deleteTarget) error {
	scopes := map[string]db.Predicate{}
	prepare := func(schema models.Schema) error {
		if _, exists := scopes[schema.Key()]; exists {
			return nil
		}
		scope, err := s.config.Scope(ctx, auth.FromContext(ctx), schema.Clone())
		if err != nil {
			return err
		}
		scopes[schema.Key()] = scope
		return nil
	}
	for _, update := range plan.Updates {
		if err := prepare(update.Record.Schema()); err != nil {
			return err
		}
	}
	for _, target := range targets {
		if err := prepare(target.schema); err != nil {
			return err
		}
	}
	conflict := func() error { return mediaError(409, "DELETE_CONFLICT", "Deletion effects changed before commit") }
	checkAbsent := func(record models.Record) error {
		schema := record.Schema()
		key, err := recordIdentity(record)
		if err != nil {
			return err
		}
		query, err := deletionKeyQuery(s.config.Store, schema, key)
		if err != nil {
			return err
		}
		fields := []string{}
		for _, field := range schema.PKFields() {
			fields = append(fields, field.Name)
		}
		exists, err := query.Only(fields...).Exists(ctx)
		if err != nil {
			return err
		}
		if exists {
			return conflict()
		}
		return nil
	}
	for _, record := range plan.Objects {
		if err := checkAbsent(record); err != nil {
			return err
		}
	}
	for _, join := range plan.JoinRemovals {
		if err := checkAbsent(join.Record); err != nil {
			return err
		}
	}
	for _, update := range plan.Updates {
		schema := update.Record.Schema()
		key, err := recordIdentity(update.Record)
		if err != nil {
			return err
		}
		query, err := deletionKeyQuery(s.config.Store, schema, key)
		if err != nil {
			return err
		}
		// Compare with the same database field semantics used by the update.
		// JSON equality would incorrectly reject e.g. a string-valued numeric
		// default after the database stores its canonical integer value.
		_, err = query.Filter(scopes[schema.Key()], orm.Q(update.Field, update.Value)).SelectForUpdate(false, false).Get(ctx)
		if errors.Is(err, orm.ErrNotFound) {
			return conflict()
		}
		if err != nil {
			return err
		}
	}
	for _, target := range targets {
		query, err := deletionKeyQuery(s.config.Store, target.schema, target.key)
		if err != nil {
			return err
		}
		row, err := query.Filter(scopes[target.schema.Key()]).SelectForUpdate(false, false).Get(ctx)
		if errors.Is(err, orm.ErrNotFound) {
			return conflict()
		}
		if err != nil {
			return err
		}
		actual, err := recordSnapshot(row)
		if err != nil {
			return err
		}
		if actual != target.snapshot {
			return conflict()
		}
	}
	return ctx.Err()
}
