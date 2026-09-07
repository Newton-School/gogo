// Package migrations applies explicit, checksummed model/data migration graphs.
// Application startup never runs migrations implicitly.
package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"regexp"
	"sort"
	"strings"
)

type Operation struct {
	Kind               string
	Schema             models.Schema
	Field, OldField    models.Field
	Index              models.Index
	IndexOrder         []string           `json:",omitempty"`
	OldIndexOrder      []string           `json:",omitempty"`
	Constraint         *models.Constraint `json:",omitempty"`
	ConstraintOrder    []string           `json:",omitempty"`
	OldConstraintOrder []string           `json:",omitempty"`
	Name, OldName      string
	SQL, ReverseSQL    string
	Args, ReverseArgs  []any
	CodeID             string
	Forward, Backward  func(context.Context, db.Executor) error `json:"-"`
}

func CreateModel(schema models.Schema) Operation {
	return Operation{Kind: "create_model", Schema: schema.Clone()}
}
func DeleteModel(schema models.Schema) Operation {
	return Operation{Kind: "delete_model", Schema: schema.Clone()}
}
func AddField(schema models.Schema, field models.Field) Operation {
	return Operation{Kind: "add_field", Schema: schema.Clone(), Field: field}
}
func RemoveField(schema models.Schema, field models.Field) Operation {
	return Operation{Kind: "remove_field", Schema: schema.Clone(), Field: field}
}
func AlterField(schema models.Schema, old, new models.Field) Operation {
	return Operation{Kind: "alter_field", Schema: schema.Clone(), OldField: old, Field: new}
}
func RenameField(schema models.Schema, old, new string) Operation {
	return Operation{Kind: "rename_field", Schema: schema.Clone(), OldName: old, Name: new}
}
func AddIndex(schema models.Schema, index models.Index) Operation {
	return Operation{Kind: "add_index", Schema: schema.Clone(), Index: cloneIndex(index)}
}
func RemoveIndex(schema models.Schema, index models.Index) Operation {
	return Operation{Kind: "remove_index", Schema: schema.Clone(), Index: cloneIndex(index)}
}
func AddConstraint(schema models.Schema, constraint models.Constraint) Operation {
	copy := cloneConstraint(constraint)
	return Operation{Kind: "add_constraint", Schema: schema.Clone(), Constraint: &copy}
}
func RemoveConstraint(schema models.Schema, constraint models.Constraint) Operation {
	copy := cloneConstraint(constraint)
	return Operation{Kind: "remove_constraint", Schema: schema.Clone(), Constraint: &copy}
}
func RunSQL(sql string, args []any, reverse string, reverseArgs []any) Operation {
	return Operation{Kind: "sql", SQL: sql, Args: append([]any(nil), args...), ReverseSQL: reverse, ReverseArgs: append([]any(nil), reverseArgs...)}
}
func RunData(id string, forward, backward func(context.Context, db.Executor) error) Operation {
	return Operation{Kind: "data", CodeID: id, Forward: forward, Backward: backward}
}

type Migration struct {
	App, Name    string
	Dependencies []string
	Operations   []Operation
	NonAtomic    bool
	Replaces     []string
}

func (m Migration) Key() string { return m.App + "." + m.Name }
func (m Migration) Checksum() (string, error) {
	for _, o := range m.Operations {
		if o.Kind == "data" && (o.CodeID == "" || o.Forward == nil) {
			return "", errors.New("migrations: data operation requires stable CodeID and forward callback")
		}
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

type Executor struct {
	Backend      db.Backend
	Editor       db.SchemaEditor
	Migrations   []Migration
	AfterMigrate func(context.Context, Migration) error
}
type Applied struct{ Key, Checksum string }

func (e *Executor) withHistoricalSchemas(target string) (*Executor, error) {
	copy := *e
	if resolver, ok := e.Editor.(db.SchemaResolverEditor); ok {
		schemas, err := e.State(target)
		if err != nil {
			return nil, err
		}
		copy.Editor, err = resolver.WithSchemas(schemas)
		if err != nil {
			return nil, err
		}
	}
	return &copy, nil
}

var migrationName = regexp.MustCompile(`^[A-Za-z0-9_]{1,128}$`)

func (e *Executor) Plan(target string) ([]Migration, error) {
	byID := map[string]Migration{}
	for _, m := range e.Migrations {
		if !models.ValidIdentifier(m.App) || !migrationName.MatchString(m.Name) {
			return nil, errors.New("migrations: invalid migration identifier")
		}
		if _, ok := byID[m.Key()]; ok {
			return nil, errors.New("migrations: duplicate migration")
		}
		if _, err := m.Checksum(); err != nil {
			return nil, err
		}
		byID[m.Key()] = m
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	order := []Migration{}
	var visit func(string) error
	visit = func(key string) error {
		if visiting[key] {
			return fmt.Errorf("migrations: dependency cycle at %s", key)
		}
		if visited[key] {
			return nil
		}
		m, ok := byID[key]
		if !ok {
			return fmt.Errorf("migrations: missing dependency %s", key)
		}
		visiting[key] = true
		for _, dependency := range m.Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[key] = false
		visited[key] = true
		order = append(order, m)
		return nil
	}
	if target != "" {
		if err := visit(target); err != nil {
			return nil, err
		}
	} else {
		keys := []string{}
		for key := range byID {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := visit(key); err != nil {
				return nil, err
			}
		}
	}
	return order, nil
}
func (e *Executor) History(ctx context.Context) ([]Applied, error) {
	if e.Backend == nil {
		return nil, errors.New("migrations: backend is required")
	}
	if provider, ok := e.Backend.(interface{ Introspector() db.Introspector }); ok {
		tables, err := provider.Introspector().Tables(ctx, db.ExecutorFor(ctx, e.Backend))
		if err != nil {
			return nil, err
		}
		found := false
		for _, table := range tables {
			if table == "gogo_migrations" {
				found = true
				break
			}
		}
		if !found {
			return []Applied{}, nil
		}
	}
	rows, err := db.ExecutorFor(ctx, e.Backend).Query(ctx, "SELECT app, name, checksum FROM gogo_migrations ORDER BY applied_at, app, name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Applied{}
	for rows.Next() {
		var app, name, sum string
		if err := rows.Scan(&app, &name, &sum); err != nil {
			return nil, err
		}
		result = append(result, Applied{Key: app + "." + name, Checksum: sum})
	}
	return result, rows.Err()
}
func (e *Executor) initialize(ctx context.Context) error {
	_, err := e.Backend.Exec(ctx, "CREATE TABLE IF NOT EXISTS gogo_migrations (app varchar(128) NOT NULL, name varchar(128) NOT NULL, checksum varchar(64) NOT NULL, applied_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (app,name))")
	return err
}
func (e *Executor) Apply(ctx context.Context, target string) (err error) {
	if e.Backend == nil || e.Editor == nil {
		return errors.New("migrations: backend and schema editor are required")
	}
	plan, err := e.Plan(target)
	if err != nil {
		return err
	}
	engines := map[string]*Executor{}
	for _, migration := range plan {
		if err := e.validateIndexOperations(migration, false); err != nil {
			return err
		}
		resolved, err := e.withHistoricalSchemas(migration.Key())
		if err != nil {
			return err
		}
		if err := resolved.validateIndexOperations(migration, false); err != nil {
			return err
		}
		engines[migration.Key()] = resolved
	}
	locker, ok := e.Backend.(db.MigrationLocker)
	if !ok {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Migration locking is required"}
	}
	unlock, err := locker.LockMigrations(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if err := e.initialize(ctx); err != nil {
		return err
	}
	history, err := e.History(ctx)
	if err != nil {
		return err
	}
	allPlan, err := e.Plan("")
	if err != nil {
		return err
	}
	known := map[string]Migration{}
	for _, m := range allPlan {
		known[m.Key()] = m
	}
	applied := map[string]string{}
	for _, entry := range history {
		m, ok := known[entry.Key]
		if !ok {
			return fmt.Errorf("migrations: applied migration %s has no source", entry.Key)
		}
		sum, _ := m.Checksum()
		if entry.Checksum != sum {
			return fmt.Errorf("migrations: checksum mismatch for %s", entry.Key)
		}
		applied[entry.Key] = sum
	}
	for _, entry := range history {
		for _, dep := range known[entry.Key].Dependencies {
			if _, ok := applied[dep]; !ok {
				return fmt.Errorf("migrations: applied dependency missing: %s", dep)
			}
		}
	}
	for _, m := range plan {
		if _, ok := applied[m.Key()]; ok {
			continue
		}
		migrationEngine := engines[m.Key()]
		sum, _ := m.Checksum()
		run := func(txCtx context.Context) error {
			executor := db.ExecutorFor(txCtx, e.Backend)
			for _, operation := range m.Operations {
				if err := migrationEngine.run(txCtx, executor, operation, false); err != nil {
					return err
				}
			}
			if editor, ok := migrationEngine.Editor.(db.DeferredSchemaEditor); ok {
				if err := editor.FlushDeferred(txCtx, executor); err != nil {
					return err
				}
			}
			_, err := executor.Exec(txCtx, "INSERT INTO gogo_migrations (app,name,checksum) VALUES ("+e.Backend.Dialect().Placeholder(1)+","+e.Backend.Dialect().Placeholder(2)+","+e.Backend.Dialect().Placeholder(3)+")", m.App, m.Name, sum)
			return err
		}
		if m.NonAtomic {
			err = run(ctx)
		} else {
			err = db.Atomic(ctx, e.Backend, db.AtomicOptions{}, run)
		}
		if err != nil {
			return fmt.Errorf("migrations: %s: %w", m.Key(), err)
		}
		applied[m.Key()] = sum
		if e.AfterMigrate != nil {
			if err = e.AfterMigrate(ctx, m); err != nil {
				return &db.CommittedCallbackError{Errors: []error{err}}
			}
		}
	}
	return nil
}
func (e *Executor) run(ctx context.Context, executor db.Executor, o Operation, reverse bool) error {
	if reverse {
		switch o.Kind {
		case "create_model":
			return e.Editor.DeleteModel(ctx, executor, o.Schema)
		case "add_field":
			return e.Editor.RemoveField(ctx, executor, o.Schema, o.Field)
		case "rename_field":
			schema, err := renameFieldState(o.Schema, o.OldName, o.Name)
			if err != nil {
				return err
			}
			return e.Editor.RenameField(ctx, executor, schema, o.Name, o.OldName)
		case "alter_field":
			schema := fieldStateAfter(o.Schema, o.OldField.Name, o.Field)
			return e.Editor.AlterField(ctx, executor, schema, o.Field, o.OldField)
		case "add_index":
			return e.removeModelIndex(ctx, executor, o.Schema, o.Index)
		case "remove_index":
			return e.Editor.AddIndex(ctx, executor, o.Schema, o.Index)
		case "index_order":
			return nil
		case "add_constraint", "remove_constraint":
			return e.runConstraint(ctx, executor, o, o.Kind == "add_constraint")
		case "constraint_order":
			return nil
		case "sql":
			if o.ReverseSQL == "" {
				return errors.New("migrations: irreversible SQL")
			}
			_, err := executor.Exec(ctx, o.ReverseSQL, o.ReverseArgs...)
			return err
		case "data":
			if o.Backward == nil {
				return errors.New("migrations: irreversible data operation")
			}
			return o.Backward(ctx, executor)
		default:
			return errors.New("migrations: destructive operation is irreversible")
		}
	}
	switch o.Kind {
	case "create_model":
		return e.Editor.CreateModel(ctx, executor, o.Schema)
	case "delete_model":
		return e.Editor.DeleteModel(ctx, executor, o.Schema)
	case "add_field":
		return e.Editor.AddField(ctx, executor, o.Schema, o.Field)
	case "remove_field":
		return e.Editor.RemoveField(ctx, executor, o.Schema, o.Field)
	case "alter_field":
		return e.Editor.AlterField(ctx, executor, o.Schema, o.OldField, o.Field)
	case "rename_field":
		return e.Editor.RenameField(ctx, executor, o.Schema, o.OldName, o.Name)
	case "add_index":
		return e.Editor.AddIndex(ctx, executor, o.Schema, o.Index)
	case "remove_index":
		return e.removeModelIndex(ctx, executor, o.Schema, o.Index)
	case "index_order":
		return nil
	case "add_constraint", "remove_constraint":
		return e.runConstraint(ctx, executor, o, o.Kind == "remove_constraint")
	case "constraint_order":
		return nil
	case "sql":
		if strings.TrimSpace(o.SQL) == "" {
			return errors.New("migrations: empty SQL")
		}
		_, err := executor.Exec(ctx, o.SQL, o.Args...)
		return err
	case "data":
		if o.Forward == nil {
			return errors.New("migrations: data function is nil")
		}
		return o.Forward(ctx, executor)
	}
	return fmt.Errorf("migrations: unknown operation %s", o.Kind)
}

func (e *Executor) removeModelIndex(ctx context.Context, executor db.Executor, schema models.Schema, index models.Index) error {
	editor, ok := e.Editor.(db.IndexLifecycleEditor)
	if !ok {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Historical index removal requires a definition-aware schema editor"}
	}
	return editor.RemoveModelIndex(ctx, executor, schema, index)
}
