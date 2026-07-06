package migrations

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/models"
)

func TestExecutorApplyRollbackAndFake(t *testing.T) {
	ctx := context.Background()
	db := openRecorderDB(t)
	defer db.Close()
	recorder := NewRecorder(db, "executor")
	editor := &FakeSchemaEditor{}
	executor := NewExecutor(recorder, editor)
	migration := testMigration("blog", "0001_initial")
	migration.Operations = []Operation{FakeOperation{NameValue: "op", ReversibleValue: true}}

	if err := executor.Apply(ctx, []Migration{migration}, ExecutorOptions{}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if ok, _ := recorder.IsApplied(ctx, migration.Dependency()); !ok {
		t.Fatalf("migration not recorded applied")
	}
	if err := executor.Rollback(ctx, migration, ExecutorOptions{}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if ok, _ := recorder.IsApplied(ctx, migration.Dependency()); ok {
		t.Fatalf("migration not recorded unapplied")
	}

	if err := executor.Apply(ctx, []Migration{migration}, ExecutorOptions{Fake: true}); err != nil {
		t.Fatalf("fake Apply() error = %v", err)
	}
	if len(editor.SQL) != 2 {
		t.Fatalf("fake apply executed SQL: %#v", editor.SQL)
	}
	if err := executor.Rollback(ctx, migration, ExecutorOptions{Fake: true}); err != nil {
		t.Fatalf("fake Rollback() error = %v", err)
	}
}

func TestExecutorFailureAndIrreversibleReverse(t *testing.T) {
	ctx := context.Background()
	db := openRecorderDB(t)
	defer db.Close()
	recorder := NewRecorder(db, "executor")
	executor := NewExecutor(recorder, &FakeSchemaEditor{})
	failing := testMigration("blog", "0002_fail")
	failing.Atomic = true
	failing.Operations = []Operation{FailingOperation{}}
	if err := executor.Apply(ctx, []Migration{failing}, ExecutorOptions{}); err == nil {
		t.Fatalf("failing migration returned nil")
	}
	if ok, _ := recorder.IsApplied(ctx, failing.Dependency()); ok {
		t.Fatalf("failing migration was recorded")
	}

	irreversible := testMigration("blog", "0003_irreversible")
	irreversible.Operations = []Operation{FakeOperation{NameValue: "irreversible", ReversibleValue: false}}
	if err := executor.Rollback(ctx, irreversible, ExecutorOptions{}); !errors.Is(err, ErrIrreversibleOperation) {
		t.Fatalf("Rollback irreversible error = %v", err)
	}
}

func TestExecutorApplyRequiresMigrationLock(t *testing.T) {
	ctx := context.Background()
	db := openRecorderDB(t)
	defer db.Close()
	recorder := NewRecorder(db, "executor")
	release, err := recorder.AcquireLock(ctx)
	if err != nil {
		t.Fatalf("AcquireLock() error = %v", err)
	}
	defer func() {
		if err := release(ctx); err != nil {
			t.Fatalf("release() error = %v", err)
		}
	}()

	editor := &FakeSchemaEditor{}
	executor := NewExecutor(recorder, editor)
	migration := testMigration("blog", "0001_initial")
	migration.Operations = []Operation{FakeOperation{NameValue: "op", ReversibleValue: true}}
	if err := executor.Apply(ctx, []Migration{migration}, ExecutorOptions{}); !errors.Is(err, ErrMigrationLocked) {
		t.Fatalf("Apply() error = %v, want ErrMigrationLocked", err)
	}
	if len(editor.SQL) != 0 {
		t.Fatalf("locked Apply() executed SQL: %#v", editor.SQL)
	}
}

func TestExecutorFakeInitialRecordsWhenInitialTablesExist(t *testing.T) {
	ctx := context.Background()
	db := openRecorderDB(t)
	defer db.Close()
	recorder := NewRecorder(db, "executor")
	editor := &tableAwareSchemaEditor{tables: map[string]bool{"blog_post": true}}
	executor := NewExecutor(recorder, editor)
	migration := testMigration("blog", "0001_initial")
	migration.Operations = []Operation{initialTableOperation{table: "blog_post"}}

	if err := executor.Apply(ctx, []Migration{migration}, ExecutorOptions{FakeInitial: true}); err != nil {
		t.Fatalf("Apply(fake-initial) error = %v", err)
	}
	if len(editor.SQL) != 0 {
		t.Fatalf("fake-initial executed SQL: %#v", editor.SQL)
	}
	if ok, _ := recorder.IsApplied(ctx, migration.Dependency()); !ok {
		t.Fatal("fake-initial migration was not recorded")
	}
}

func TestExecutorFakeInitialRequiresMatchingSchemaShape(t *testing.T) {
	ctx := context.Background()
	db := openRecorderDB(t)
	defer db.Close()
	recorder := NewRecorder(db, "executor")
	migration := testMigration("blog", "0001_initial")
	migration.Operations = []Operation{initialSchemaOperation{
		table:   "blog_post",
		columns: []ColumnSchema{{Name: "id", PrimaryKey: true}, {Name: "title"}},
	}}

	mismatch := &shapeAwareSchemaEditor{columns: map[string][]ColumnSchema{
		"blog_post": {{Name: "id", PrimaryKey: true}},
	}}
	if err := NewExecutor(recorder, mismatch).Apply(ctx, []Migration{migration}, ExecutorOptions{FakeInitial: true}); err != nil {
		t.Fatalf("Apply(fake-initial mismatch) error = %v", err)
	}
	if len(mismatch.SQL) != 1 {
		t.Fatalf("shape mismatch should execute migration SQL, got %#v", mismatch.SQL)
	}
	if err := NewExecutor(recorder, mismatch).Rollback(ctx, migration, ExecutorOptions{Fake: true}); err != nil {
		t.Fatalf("cleanup rollback error = %v", err)
	}

	match := &shapeAwareSchemaEditor{columns: map[string][]ColumnSchema{
		"blog_post": {{Name: "id", PrimaryKey: true}, {Name: "title"}},
	}}
	if err := NewExecutor(recorder, match).Apply(ctx, []Migration{migration}, ExecutorOptions{FakeInitial: true}); err != nil {
		t.Fatalf("Apply(fake-initial match) error = %v", err)
	}
	if len(match.SQL) != 0 {
		t.Fatalf("shape match should fake initial without SQL, got %#v", match.SQL)
	}
}

func TestExecutorFakeInitialRejectsTypeAndDefaultMismatch(t *testing.T) {
	ctx := context.Background()
	db := openRecorderDB(t)
	defer db.Close()
	recorder := NewRecorder(db, "executor")
	defaultValue := databaseDefaultPtr(models.DefaultValue("draft"))
	migration := testMigration("blog", "0001_initial")
	migration.Operations = []Operation{initialSchemaOperation{
		table: "blog_post",
		columns: []ColumnSchema{
			{Name: "id", Kind: "bigint", NormalizedKind: "bigint", PrimaryKey: true},
			{Name: "title", Kind: "text", NormalizedKind: "text", Default: defaultValue},
		},
	}}

	editor := &shapeAwareSchemaEditor{columns: map[string][]ColumnSchema{
		"blog_post": {
			{Name: "id", Kind: "bigint", NormalizedKind: "bigint", PrimaryKey: true},
			{Name: "title", Kind: "integer", NormalizedKind: "integer"},
		},
	}}
	if err := NewExecutor(recorder, editor).Apply(ctx, []Migration{migration}, ExecutorOptions{FakeInitial: true}); err != nil {
		t.Fatalf("Apply(fake-initial mismatch) error = %v", err)
	}
	if len(editor.SQL) != 1 {
		t.Fatalf("type/default mismatch should execute migration SQL, got %#v", editor.SQL)
	}
}

func TestExecutorFakeInitialFailsWhenInitialSchemaCannotBeVerified(t *testing.T) {
	ctx := context.Background()
	db := openRecorderDB(t)
	defer db.Close()
	recorder := NewRecorder(db, "executor")
	migration := testMigration("blog", "0001_initial")
	migration.Operations = []Operation{initialSchemaOperation{
		table:   "blog_post",
		columns: []ColumnSchema{{Name: "id", PrimaryKey: true}},
	}}
	editor := &FakeSchemaEditor{}

	err := NewExecutor(recorder, editor).Apply(ctx, []Migration{migration}, ExecutorOptions{FakeInitial: true})
	if !errors.Is(err, ErrFakeInitialVerification) {
		t.Fatalf("Apply(fake-initial unverifiable) error = %v, want ErrFakeInitialVerification", err)
	}
	if len(editor.SQL) != 0 {
		t.Fatalf("unverifiable fake-initial executed SQL: %#v", editor.SQL)
	}
	if ok, _ := recorder.IsApplied(ctx, migration.Dependency()); ok {
		t.Fatal("unverifiable fake-initial migration was recorded")
	}
}

func TestMigrationChecksumIncludesCanonicalContent(t *testing.T) {
	base := testMigration("blog", "0001_initial")
	base.Atomic = true
	base.Dependencies = []Dependency{{AppLabel: "auth", Name: "0001_initial"}}
	base.RunBefore = []Dependency{{AppLabel: "comments", Name: "0001_initial"}}
	base.Operations = []Operation{ManifestOperation{Spec: OperationSpec{Type: "RunSQL", SQL: "SELECT 1"}}}

	changedSQL := base
	changedSQL.Operations = []Operation{ManifestOperation{Spec: OperationSpec{Type: "RunSQL", SQL: "SELECT 2"}}}
	if migrationChecksum(base) == migrationChecksum(changedSQL) {
		t.Fatal("checksum did not include operation content")
	}

	changedAtomic := base
	changedAtomic.Atomic = false
	if migrationChecksum(base) == migrationChecksum(changedAtomic) {
		t.Fatal("checksum did not include atomic flag")
	}

	changedRunBefore := base
	changedRunBefore.RunBefore = nil
	if migrationChecksum(base) == migrationChecksum(changedRunBefore) {
		t.Fatal("checksum did not include run-before dependencies")
	}
}

type FailingOperation struct{}

func (FailingOperation) Name() string                      { return "FailingOperation" }
func (FailingOperation) StateForwards(*ProjectState) error { return nil }
func (FailingOperation) DatabaseForwards(context.Context, SchemaEditor) error {
	return errors.New("failed")
}
func (FailingOperation) DatabaseBackwards(context.Context, SchemaEditor) error { return nil }
func (FailingOperation) Describe() string                                      { return "failing" }
func (FailingOperation) Reversible() bool                                      { return true }
func (FailingOperation) ReferencesModel(string, string) bool                   { return false }
func (FailingOperation) ReferencesField(string, string, string) bool           { return false }

type initialTableOperation struct {
	table string
}

func (o initialTableOperation) Name() string                      { return "InitialTableOperation" }
func (o initialTableOperation) StateForwards(*ProjectState) error { return nil }
func (o initialTableOperation) DatabaseForwards(ctx context.Context, editor SchemaEditor) error {
	return editor.Execute(ctx, "CREATE TABLE "+o.table+" ()")
}
func (o initialTableOperation) DatabaseBackwards(context.Context, SchemaEditor) error {
	return nil
}
func (o initialTableOperation) Describe() string                            { return "initial table" }
func (o initialTableOperation) Reversible() bool                            { return true }
func (o initialTableOperation) ReferencesModel(string, string) bool         { return false }
func (o initialTableOperation) ReferencesField(string, string, string) bool { return false }
func (o initialTableOperation) InitialTables() []string                     { return []string{o.table} }

type initialSchemaOperation struct {
	table   string
	columns []ColumnSchema
}

func (o initialSchemaOperation) Name() string                      { return "InitialSchemaOperation" }
func (o initialSchemaOperation) StateForwards(*ProjectState) error { return nil }
func (o initialSchemaOperation) DatabaseForwards(ctx context.Context, editor SchemaEditor) error {
	return editor.Execute(ctx, "CREATE TABLE "+o.table+" ()")
}
func (o initialSchemaOperation) DatabaseBackwards(context.Context, SchemaEditor) error {
	return nil
}
func (o initialSchemaOperation) Describe() string                            { return "initial schema" }
func (o initialSchemaOperation) Reversible() bool                            { return true }
func (o initialSchemaOperation) ReferencesModel(string, string) bool         { return false }
func (o initialSchemaOperation) ReferencesField(string, string, string) bool { return false }
func (o initialSchemaOperation) InitialSchema() []TableSchema {
	return []TableSchema{{Name: o.table, Columns: append([]ColumnSchema(nil), o.columns...)}}
}

type tableAwareSchemaEditor struct {
	FakeSchemaEditor
	tables map[string]bool
}

func (e *tableAwareSchemaEditor) TableExists(_ context.Context, table string) (bool, error) {
	return e.tables[table], nil
}

type shapeAwareSchemaEditor struct {
	FakeSchemaEditor
	columns map[string][]ColumnSchema
}

func (e *shapeAwareSchemaEditor) TableColumns(_ context.Context, table string) ([]ColumnSchema, error) {
	return append([]ColumnSchema(nil), e.columns[table]...), nil
}
