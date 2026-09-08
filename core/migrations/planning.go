package migrations

import (
	"context"
	"errors"
	"fmt"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"reflect"
	"sort"
	"strings"
)

type Statement struct {
	SQL     string
	Args    []any
	Comment string
}
type recordingExecutor struct{ statements []Statement }
type recordedResult struct{}

func (recordedResult) RowsAffected() (int64, error) { return 0, nil }
func (r *recordingExecutor) Exec(_ context.Context, statement string, args ...any) (db.Result, error) {
	r.statements = append(r.statements, Statement{SQL: statement, Args: append([]any(nil), args...)})
	return recordedResult{}, nil
}
func (r *recordingExecutor) Query(context.Context, string, ...any) (db.Rows, error) {
	return nil, errors.New("migrations: preview cannot execute database reads")
}

// SQL previews a single migration; data callbacks are described, never invoked.
func (e *Executor) SQL(ctx context.Context, key string, reverse bool) ([]Statement, error) {
	var migration *Migration
	for i := range e.Migrations {
		if e.Migrations[i].Key() == key {
			migration = &e.Migrations[i]
			break
		}
	}
	if migration == nil {
		return nil, fmt.Errorf("migrations: unknown migration %s", key)
	}
	if e.Editor == nil {
		return nil, errors.New("migrations: schema editor is required for SQL rendering")
	}
	if err := e.validateIndexOperations(*migration, reverse); err != nil {
		return nil, err
	}
	resolved, err := e.withHistoricalSchemas(key, reverse)
	if err != nil {
		return nil, err
	}
	if err := resolved.validateIndexOperations(*migration, reverse); err != nil {
		return nil, err
	}
	recorder := &recordingExecutor{}
	operations := append([]Operation(nil), migration.Operations...)
	if reverse {
		for i, j := 0, len(operations)-1; i < j; i, j = i+1, j-1 {
			operations[i], operations[j] = operations[j], operations[i]
		}
	}
	for _, operation := range operations {
		if operation.Kind == "data" {
			recorder.statements = append(recorder.statements, Statement{Comment: "Run data callback " + operation.CodeID})
			continue
		}
		if err := resolved.run(ctx, recorder, operation, reverse); err != nil {
			return nil, err
		}
	}
	if editor, ok := resolved.Editor.(db.DeferredSchemaEditor); ok {
		if err := editor.FlushDeferred(ctx, recorder); err != nil {
			return nil, err
		}
	}
	return recorder.statements, nil
}
func (e *Executor) State(target string) ([]models.Schema, error) {
	plan, err := e.Plan(target)
	if err != nil {
		return nil, err
	}
	return schemaState(plan)
}

func schemaState(plan []Migration) ([]models.Schema, error) {
	state := map[string]models.Schema{}
	for _, migration := range plan {
		for _, operation := range migration.Operations {
			key := operation.Schema.Key()
			schema := state[key]
			switch operation.Kind {
			case "create_model":
				state[key] = operation.Schema.Clone()
			case "delete_model":
				delete(state, key)
			case "add_field":
				schema.Fields = append(schema.Fields, operation.Field)
				state[key] = schema
			case "remove_field":
				fields := []models.Field{}
				for _, f := range schema.Fields {
					if f.Name != operation.Field.Name {
						fields = append(fields, f)
					}
				}
				schema.Fields = fields
				state[key] = schema
			case "alter_field":
				for i := range schema.Fields {
					if schema.Fields[i].Name == operation.OldField.Name {
						schema.Fields[i] = operation.Field
					}
				}
				state[key] = schema
			case "rename_field":
				var err error
				schema, err = renameFieldState(schema, operation.OldName, operation.Name)
				if err != nil {
					return nil, err
				}
				state[key] = schema
				for otherKey, other := range state {
					if otherKey == key {
						continue
					}
					other = other.Clone()
					for i := range other.Fields {
						f := &other.Fields[i]
						renameRelationFieldReferences(f.Relation, key, operation.OldName, operation.Name)
					}
					state[otherKey] = other
				}
			case "add_index", "remove_index", "index_order":
				var err error
				schema, err = applyIndexState(schema, operation)
				if err != nil {
					return nil, err
				}
				state[key] = schema
			case "add_constraint", "remove_constraint", "constraint_order":
				var err error
				schema, err = applyConstraintState(schema, operation)
				if err != nil {
					return nil, err
				}
				state[key] = schema
			}
		}
	}
	keys := []string{}
	for key := range state {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := []models.Schema{}
	for _, key := range keys {
		result = append(result, state[key].Clone())
	}
	return result, nil
}

// Detect refuses destructive/ambiguous changes unless the caller supplies an
// explicit allowlist; it never guesses whether remove+add means rename.
type DetectOptions struct {
	AllowRemoveModels, AllowRemoveFields map[string]bool
	Renames                              map[string]string
}

func Detect(before, after []models.Schema, options DetectOptions) ([]Operation, error) {
	old, new := map[string]models.Schema{}, map[string]models.Schema{}
	for _, schema := range before {
		old[schema.Key()] = schema
	}
	for _, schema := range after {
		if err := schema.Validate(); err != nil {
			return nil, err
		}
		new[schema.Key()] = schema
	}
	keys := []string{}
	for key := range new {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	operations, err := detectFieldRenames(old, new, options.Renames)
	if err != nil {
		return nil, err
	}
	historical := make(map[string]models.Schema, len(old))
	for key, schema := range old {
		historical[key] = schema
	}
	for _, key := range keys {
		schema := new[key]
		previous, exists := old[key]
		if !exists {
			operations = append(operations, CreateModel(schema))
			continue
		}
		if err := supportedModelStateChange(previous, schema); err != nil {
			return nil, err
		}
		removeIndexes, addIndexes, indexOrder, err := detectIndexes(previous, schema)
		if err != nil {
			return nil, err
		}
		removeConstraints, addConstraints, constraintOrder, err := detectConstraints(previous, schema)
		if err != nil {
			return nil, err
		}
		operations = append(operations, removeIndexes...)
		for _, operation := range removeIndexes {
			previous, err = applyIndexState(previous, operation)
			if err != nil {
				return nil, err
			}
		}
		operations = append(operations, removeConstraints...)
		for _, operation := range removeConstraints {
			previous, err = applyConstraintState(previous, operation)
			if err != nil {
				return nil, err
			}
		}
		oldFields := map[string]models.Field{}
		for _, field := range previous.Fields {
			oldFields[field.Name] = field
		}
		for _, field := range schema.Fields {
			oldField, exists := oldFields[field.Name]
			if !exists {
				if !field.Null && !field.HasDefault() && !field.IsAuto() {
					return nil, fmt.Errorf("migrations: nonnullable added field %s.%s requires default or staged backfill", key, field.Name)
				}
				operations = append(operations, AddField(previous, field))
				previous = fieldStateAfter(previous, field.Name, field)
				continue
			}
			delete(oldFields, field.Name)
			if !fieldEquivalent(oldField, field) {
				operations = append(operations, AlterField(previous, oldField, field))
				previous = fieldStateAfter(previous, oldField.Name, field)
			}
		}
		removed := []string{}
		for name := range oldFields {
			removed = append(removed, name)
		}
		sort.Strings(removed)
		for _, name := range removed {
			if !options.AllowRemoveFields[key+"."+name] {
				return nil, fmt.Errorf("migrations: removing %s.%s requires explicit intent", key, name)
			}
			operations = append(operations, RemoveField(previous, oldFields[name]))
			remaining := make([]models.Field, 0, len(previous.Fields)-1)
			for _, field := range previous.Fields {
				if field.Name != name {
					remaining = append(remaining, field)
				}
			}
			previous.Fields = remaining
		}
		operations = append(operations, addIndexes...)
		operations = append(operations, addConstraints...)
		if indexOrder != nil {
			operations = append(operations, *indexOrder)
		}
		if constraintOrder != nil {
			operations = append(operations, *constraintOrder)
		}
		delete(old, key)
	}
	removed := []string{}
	for key := range old {
		removed = append(removed, key)
	}
	sort.Strings(removed)
	for _, key := range removed {
		if !options.AllowRemoveModels[key] {
			return nil, fmt.Errorf("migrations: removing %s requires explicit intent", key)
		}
		operations = append(operations, DeleteModel(old[key]))
	}
	return orderDetectedOperations(operations, historical, new)
}
func fieldEquivalent(a, b models.Field) bool {
	a.Validators = nil
	b.Validators = nil
	a.DefaultFunc = nil
	b.DefaultFunc = nil
	a.Codec = nil
	b.Codec = nil
	return reflect.DeepEqual(a, b)
}

// Reverse keeps target and its dependencies, undoing later migrations in the
// same app and applied downstream dependents. Use app.zero for no app migrations.
func (e *Executor) Reverse(ctx context.Context, target string) (err error) {
	app, _, ok := strings.Cut(target, ".")
	if !ok {
		return errors.New("migrations: reverse target must be app.name or app.zero")
	}
	all, err := e.Plan("")
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	if !strings.HasSuffix(target, ".zero") {
		plan, err := e.Plan(target)
		if err != nil {
			return err
		}
		for _, m := range plan {
			keep[m.Key()] = true
		}
	}
	remove := map[string]bool{}
	for _, m := range all {
		if m.App == app && !keep[m.Key()] {
			remove[m.Key()] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, m := range all {
			if remove[m.Key()] {
				continue
			}
			for _, dep := range m.Dependencies {
				if remove[dep] {
					remove[m.Key()] = true
					changed = true
					break
				}
			}
		}
	}
	locker, ok := e.Backend.(db.MigrationLocker)
	if !ok {
		return errors.New("migrations: locking required")
	}
	unlock, err := locker.LockMigrations(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	history, err := e.History(ctx)
	if err != nil {
		return err
	}
	applied := map[string]string{}
	for _, h := range history {
		applied[h.Key] = h.Checksum
	}
	engines := map[string]*Executor{}
	for _, m := range all {
		if sum, ok := applied[m.Key()]; ok {
			expected, _ := m.Checksum()
			if sum != expected {
				return fmt.Errorf("migrations: checksum mismatch %s", m.Key())
			}
		}
		if remove[m.Key()] {
			if applied[m.Key()] != "" {
				if err := e.validateIndexOperations(m, true); err != nil {
					return err
				}
				resolved, err := e.withHistoricalSchemas(m.Key(), true)
				if err != nil {
					return err
				}
				if err := resolved.validateIndexOperations(m, true); err != nil {
					return err
				}
				engines[m.Key()] = resolved
			}
			for _, o := range m.Operations {
				if o.Kind == "delete_model" || o.Kind == "remove_field" || (o.Kind == "sql" && o.ReverseSQL == "") || (o.Kind == "data" && o.Backward == nil) {
					return fmt.Errorf("migrations: irreversible operation in %s", m.Key())
				}
			}
		}
	}
	for i := len(all) - 1; i >= 0; i-- {
		m := all[i]
		if !remove[m.Key()] || applied[m.Key()] == "" {
			continue
		}
		migrationEngine := engines[m.Key()]
		run := func(txCtx context.Context) error {
			executor := db.ExecutorFor(txCtx, e.Backend)
			for j := len(m.Operations) - 1; j >= 0; j-- {
				if err := migrationEngine.run(txCtx, executor, m.Operations[j], true); err != nil {
					return err
				}
			}
			if editor, ok := migrationEngine.Editor.(db.DeferredSchemaEditor); ok {
				if err := editor.FlushDeferred(txCtx, executor); err != nil {
					return err
				}
			}
			_, err := executor.Exec(txCtx, "DELETE FROM gogo_migrations WHERE app="+e.Backend.Dialect().Placeholder(1)+" AND name="+e.Backend.Dialect().Placeholder(2), m.App, m.Name)
			return err
		}
		if m.NonAtomic {
			err = run(ctx)
		} else {
			err = db.Atomic(ctx, e.Backend, db.AtomicOptions{}, run)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
