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
	resolved, err := e.withHistoricalSchemas(key)
	if err != nil {
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
				for i := range schema.Fields {
					if schema.Fields[i].Name == operation.OldName {
						schema.Fields[i].Name = operation.Name
						schema.Fields[i].Column = operation.Name
					}
				}
				state[key] = schema
			case "add_index":
				schema.Indexes = append(schema.Indexes, operation.Index)
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
		result = append(result, state[key])
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
	operations := []Operation{}
	for _, key := range keys {
		schema := new[key]
		previous, exists := old[key]
		if !exists {
			operations = append(operations, CreateModel(schema))
			continue
		}
		oldFields := map[string]models.Field{}
		for _, field := range previous.Fields {
			oldFields[field.Name] = field
		}
		for _, field := range schema.Fields {
			oldField, exists := oldFields[field.Name]
			if !exists {
				renamed := ""
				for oldName, newName := range options.Renames {
					if strings.HasPrefix(oldName, key+".") && newName == field.Name {
						source := strings.TrimPrefix(oldName, key+".")
						if _, ok := oldFields[source]; ok {
							renamed = source
							break
						}
					}
				}
				if renamed != "" {
					operations = append(operations, RenameField(previous, renamed, field.Name))
					oldField = oldFields[renamed]
					delete(oldFields, renamed)
					oldField.Name = field.Name
					oldField.Column = field.Column
					if !fieldEquivalent(oldField, field) {
						operations = append(operations, AlterField(previous, oldField, field))
					}
					continue
				}
				if !field.Null && !field.HasDefault() && !field.IsAuto() {
					return nil, fmt.Errorf("migrations: nonnullable added field %s.%s requires default or staged backfill", key, field.Name)
				}
				operations = append(operations, AddField(previous, field))
				continue
			}
			delete(oldFields, field.Name)
			if !fieldEquivalent(oldField, field) {
				operations = append(operations, AlterField(previous, oldField, field))
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
	return operations, nil
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
	for _, m := range all {
		if sum, ok := applied[m.Key()]; ok {
			expected, _ := m.Checksum()
			if sum != expected {
				return fmt.Errorf("migrations: checksum mismatch %s", m.Key())
			}
		}
		if remove[m.Key()] {
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
		migrationEngine, err := e.withHistoricalSchemas(m.Key())
		if err != nil {
			return err
		}
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
