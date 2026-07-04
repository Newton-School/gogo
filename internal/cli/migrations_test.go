package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	authmigrations "github.com/cybersaksham/gogo/auth/migrations"
	"github.com/cybersaksham/gogo/migrations"
	"github.com/cybersaksham/gogo/migrations/operations"
	"github.com/cybersaksham/gogo/models"
	sqlitedialect "github.com/cybersaksham/gogo/orm/dialects/sqlite"

	_ "modernc.org/sqlite"
)

func TestMigrationCommandsRunWithFlags(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	migrationsDir := filepath.Join(dir, "blog", "migrations")
	if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	writeTextFile(t, filepath.Join(migrationsDir, "0001_initial.go"), "package migrations\n")
	writeTextFile(t, filepath.Join(migrationsDir, "0002_post.go"), "package migrations\n")
	root := NewRoot()
	cases := [][]string{
		{"makemigrations", "--app", "blog", "--name", "initial", "--empty", "--dry-run"},
		{"migrate", "--database", "default", "--plan", "--fake"},
		{"showmigrations", "--app", "blog", "--verbosity", "2"},
		{"sqlmigrate", "blog", "0001_initial", "--database", "default"},
		{"squashmigrations", "blog", "0001_initial", "0002_post", "--noinput"},
		{"migrate", "--prune"},
		{"optimizemigration", "blog", "0001_initial"},
	}
	for _, args := range cases {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if err := root.Execute(context.Background(), args, &stdout, &stderr); err != nil {
			t.Fatalf("Execute(%v) error = %v", args, err)
		}
		if stdout.Len() == 0 {
			t.Fatalf("Execute(%v) produced no output", args)
		}
	}
}

func TestMakeMigrationsWritesFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	root := NewRoot()
	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"makemigrations", "--app", "blog", "--name", "initial", "--empty"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("makemigrations error = %v", err)
	}
	path := filepath.Join(dir, "blog", "migrations", "0001_initial.go")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected migration file %s: %v", path, err)
	}
	if !strings.Contains(stdout.String(), "created blog.0001_initial") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestProjectStateFromModelsInfersRelationKindFromTargetPrimaryKeyWhenAppScoped(t *testing.T) {
	state, err := projectStateFromModels([]models.Metadata{
		{
			AppLabel:  "accounts",
			ModelName: "User",
			TableName: "accounts_user",
			Fields: []models.FieldMeta{
				{Name: "id", Column: "id", Kind: "uuid", PrimaryKey: true},
			},
		},
		{
			AppLabel:  "docs",
			ModelName: "Document",
			TableName: "docs_document",
			Fields: []models.FieldMeta{
				{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
				{Name: "owner", Column: "owner_id", RelationTarget: "accounts.User", DeleteBehavior: "cascade"},
			},
		},
	}, "docs")
	if err != nil {
		t.Fatalf("projectStateFromModels() error = %v", err)
	}
	if _, exists := state.Models["accounts.User"]; exists {
		t.Fatalf("app-scoped state included target model: %#v", state.Models)
	}
	field := state.Models["docs.Document"].Fields[1]
	if field.Kind != "uuid" {
		t.Fatalf("relation field kind = %q, want uuid", field.Kind)
	}
}

func TestMakeMigrationsDiscoversGeneratedAppsFromProjectRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "apps", "blog", "migrations"), 0o755); err != nil {
		t.Fatalf("mkdir blog migrations: %v", err)
	}
	t.Chdir(dir)

	root := NewRoot()
	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"makemigrations"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("makemigrations error = %v", err)
	}
	path := filepath.Join(dir, "apps", "blog", "migrations", "0001_initial.go")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected generated app migration %s: %v", path, err)
	}
	if !strings.Contains(string(contents), `"blog"`) || !strings.Contains(string(contents), `\"type\":\"CreateModel\"`) || !strings.Contains(string(contents), `blog_item`) {
		t.Fatalf("migration contents did not describe blog item:\n%s", contents)
	}
	if !strings.Contains(stdout.String(), "created blog.0001_initial") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestShowAndSQLMigrateUseGeneratedAppOutput(t *testing.T) {
	dir := t.TempDir()
	migrationsDir := filepath.Join(dir, "apps", "blog", "migrations")
	if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	writeTextFile(t, filepath.Join(migrationsDir, "0001_initial.go"), "package migrations\n")
	t.Chdir(dir)

	root := NewRoot()
	var showOut bytes.Buffer
	if err := root.Execute(context.Background(), []string{"showmigrations"}, &showOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("showmigrations error = %v", err)
	}
	if !strings.Contains(showOut.String(), "[ ] blog.0001_initial") {
		t.Fatalf("showmigrations stdout = %q", showOut.String())
	}

	var sqlOut bytes.Buffer
	if err := root.Execute(context.Background(), []string{"sqlmigrate", "blog", "0001_initial"}, &sqlOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("sqlmigrate error = %v", err)
	}
	if !strings.Contains(sqlOut.String(), `CREATE TABLE IF NOT EXISTS "blog_item"`) {
		t.Fatalf("sqlmigrate stdout = %q", sqlOut.String())
	}
}

func TestSQLMigrateUsesGeneratedOperationContent(t *testing.T) {
	dir := t.TempDir()
	migrationsDir := filepath.Join(dir, "apps", "blog", "migrations")
	if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	writeGeneratedMigration(t, migrationsDir, migrations.Migration{
		AppLabel:   "blog",
		Name:       "0001_initial",
		Operations: []migrations.Operation{migrations.ManifestOperation{NameValue: "EmptyMigration"}},
	})
	t.Chdir(dir)

	var stdout bytes.Buffer
	if err := NewRoot().Execute(context.Background(), []string{"sqlmigrate", "blog", "0001_initial"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("sqlmigrate error = %v", err)
	}
	if strings.Contains(stdout.String(), "CREATE TABLE") || !strings.Contains(stdout.String(), "No SQL operations") {
		t.Fatalf("sqlmigrate stdout = %q", stdout.String())
	}
}

func TestSQLMigrateRendersCustomTableNamesWithSharedRenderer(t *testing.T) {
	dir := t.TempDir()
	migrationsDir := filepath.Join(dir, "apps", "sales", "migrations")
	writeGeneratedMigration(t, migrationsDir, migrations.Migration{
		AppLabel: "sales",
		Name:     "0001_initial",
		Atomic:   true,
		Operations: []migrations.Operation{
			operations.CreateModel{Model: migrations.ModelState{
				AppLabel:  "sales",
				Name:      "Order",
				TableName: "orders",
				Fields: []migrations.FieldState{
					{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
					{Name: "status", Column: "status", Kind: "text", Null: true},
				},
			}},
			operations.AddIndex{AppLabel: "sales", ModelName: "Order", TableName: "orders", Index: migrations.IndexState{Name: "idx_orders_status", Fields: []string{"status"}}},
			operations.AddConstraint{AppLabel: "sales", ModelName: "Order", TableName: "orders", Constraint: migrations.ConstraintState{Name: "uniq_orders_status", Type: "unique", Fields: []string{"status"}}},
		},
	})
	t.Chdir(dir)

	var stdout bytes.Buffer
	if err := NewRoot().Execute(context.Background(), []string{"sqlmigrate", "sales", "0001_initial"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("sqlmigrate error = %v", err)
	}
	output := stdout.String()
	for _, want := range []string{`CREATE TABLE "orders"`, `CREATE INDEX "idx_orders_status" ON "orders" ("status")`, `-- SQLite rebuild required to add constraint "uniq_orders_status" on "orders"`} {
		if !strings.Contains(output, want) {
			t.Fatalf("sqlmigrate output missing %q:\n%s", want, output)
		}
	}
}

func TestBuiltInAuthMigrationIsDiscoveredAndAppliedByDefault(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.sqlite3")
	writeMigrationEnv(t, dir, dbPath)
	t.Chdir(dir)

	root := NewRoot()
	var showOut bytes.Buffer
	if err := root.Execute(context.Background(), []string{"showmigrations", "--app", "auth"}, &showOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("showmigrations auth error = %v", err)
	}
	if !strings.Contains(showOut.String(), "[ ] auth.0001_initial") {
		t.Fatalf("showmigrations auth stdout = %q", showOut.String())
	}

	var sqlOut bytes.Buffer
	if err := root.Execute(context.Background(), []string{"sqlmigrate", "auth", "0001_initial"}, &sqlOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("sqlmigrate auth error = %v", err)
	}
	if !strings.Contains(sqlOut.String(), "CREATE TABLE auth_user") {
		t.Fatalf("sqlmigrate auth stdout = %q", sqlOut.String())
	}

	var migrateOut bytes.Buffer
	if err := root.Execute(context.Background(), []string{"migrate"}, &migrateOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("migrate error = %v", err)
	}
	if !strings.Contains(migrateOut.String(), "applied auth.0001_initial") {
		t.Fatalf("migrate stdout = %q", migrateOut.String())
	}
	for _, table := range []string{"gogo_content_type", "auth_permission", "auth_group", "auth_user"} {
		assertSQLiteTableExists(t, dbPath, table)
	}
	assertMigrationRecorded(t, dbPath, "auth", "0001_initial")
}

func TestMigrateFakeInitialRecordsExistingBuiltInAuthSchema(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	dbPath := filepath.Join(dir, "db.sqlite3")
	writeMigrationEnv(t, dir, dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range sqlStatementsForMigration(authmigrations.Initial(), sqlitedialect.New()) {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare existing auth schema: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	var stdout bytes.Buffer
	if err := NewRoot().Execute(context.Background(), []string{"migrate", "--app", "auth", "--fake-initial"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("migrate --fake-initial error = %v", err)
	}
	if !strings.Contains(stdout.String(), "applied auth.0001_initial") {
		t.Fatalf("migrate stdout = %q", stdout.String())
	}
	assertMigrationRecorded(t, dbPath, "auth", migrations.InitialMigrationName())
}

func TestMigrateAppliesGeneratedAppMigrationsAndShowMigrationsUsesRecorder(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.sqlite3")
	writeMigrationEnv(t, dir, dbPath)
	migrationsDir := filepath.Join(dir, "apps", "blog", "migrations")
	writeGeneratedMigration(t, migrationsDir, migrations.Migration{
		AppLabel: "blog",
		Name:     "0001_initial",
		Atomic:   true,
		Operations: []migrations.Operation{
			migrations.ManifestOperation{NameValue: "CreateModel:blog.Item"},
		},
	})
	t.Chdir(dir)

	root := NewRoot()
	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"migrate"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("migrate error = %v", err)
	}
	if !strings.Contains(stdout.String(), "applied blog.0001_initial") {
		t.Fatalf("migrate stdout = %q", stdout.String())
	}
	assertSQLiteTableExists(t, dbPath, "blog_item")
	assertMigrationRecorded(t, dbPath, "blog", "0001_initial")

	var showOut bytes.Buffer
	if err := root.Execute(context.Background(), []string{"showmigrations"}, &showOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("showmigrations error = %v", err)
	}
	if !strings.Contains(showOut.String(), "[X] blog.0001_initial") {
		t.Fatalf("showmigrations stdout = %q", showOut.String())
	}
}

func TestMigrateUsesGeneratedOperationContent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.sqlite3")
	writeMigrationEnv(t, dir, dbPath)
	migrationsDir := filepath.Join(dir, "apps", "blog", "migrations")
	if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	writeGeneratedMigration(t, migrationsDir, migrations.Migration{
		AppLabel:   "blog",
		Name:       "0001_initial",
		Operations: []migrations.Operation{migrations.ManifestOperation{NameValue: "EmptyMigration"}},
	})
	t.Chdir(dir)

	var stdout bytes.Buffer
	if err := NewRoot().Execute(context.Background(), []string{"migrate"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("migrate error = %v", err)
	}
	assertMigrationRecorded(t, dbPath, "blog", "0001_initial")
	assertSQLiteTableMissing(t, dbPath, "blog_item")
}

func TestMigratePlanListsPendingMigrations(t *testing.T) {
	dir := t.TempDir()
	writeMigrationEnv(t, dir, filepath.Join(dir, "db.sqlite3"))
	migrationsDir := filepath.Join(dir, "apps", "blog", "migrations")
	writeGeneratedMigration(t, migrationsDir, migrations.Migration{
		AppLabel:   "blog",
		Name:       "0001_initial",
		Atomic:     true,
		Operations: []migrations.Operation{migrations.ManifestOperation{NameValue: "CreateModel:blog.Item"}},
	})
	t.Chdir(dir)

	var stdout bytes.Buffer
	if err := NewRoot().Execute(context.Background(), []string{"migrate", "--plan"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("migrate --plan error = %v", err)
	}
	if !strings.Contains(stdout.String(), "apply blog.0001_initial") {
		t.Fatalf("migrate --plan stdout = %q", stdout.String())
	}
}

func TestMakeMigrationsCheckDryRunSkipsExistingMigration(t *testing.T) {
	dir := t.TempDir()
	migrationsDir := filepath.Join(dir, "apps", "blog", "migrations")
	writeGeneratedMigration(t, migrationsDir, migrations.Migration{
		AppLabel:   "blog",
		Name:       "0001_initial",
		Atomic:     true,
		Operations: []migrations.Operation{migrations.ManifestOperation{NameValue: "CreateModel:blog.Item"}},
	})
	t.Chdir(dir)

	var stdout bytes.Buffer
	if err := NewRoot().Execute(context.Background(), []string{"makemigrations", "--app", "blog", "--name", "initial", "--check", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("makemigrations --check --dry-run error = %v", err)
	}
	if !strings.Contains(stdout.String(), "no changes detected") || strings.Contains(stdout.String(), "would create") {
		t.Fatalf("makemigrations stdout = %q", stdout.String())
	}
}

func TestMakeMigrationsCheckFailsWhenMigrationWouldBeCreated(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "apps", "blog", "migrations"), 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	t.Chdir(dir)

	var stdout bytes.Buffer
	err := NewRoot().Execute(context.Background(), []string{"makemigrations", "--app", "blog", "--check", "--dry-run"}, &stdout, &bytes.Buffer{})
	if !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("makemigrations error = %v, want ErrCommandFailed", err)
	}
	if !strings.Contains(stdout.String(), "would create blog.0001_initial") {
		t.Fatalf("makemigrations stdout = %q", stdout.String())
	}
}

func TestMakeMigrationsUsesProjectModelMetadata(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	meta := models.Metadata{
		AppLabel:  "sales",
		ModelName: "Order",
		TableName: "orders",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "status", Column: "status", Kind: "text", Null: true},
		},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: []models.Metadata{meta}, ProjectMigrations: []migrations.Migration{}})

	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"makemigrations", "--app", "sales"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("makemigrations error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "sales", "migrations", "0001_initial.go"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	for _, want := range []string{`\"type\":\"CreateModel\"`, `\"table_name\":\"orders\"`, `\"name\":\"status\"`} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("migration missing %q:\n%s", want, contents)
		}
	}
	if strings.Contains(string(contents), `blog_item`) || !strings.Contains(stdout.String(), "created sales.0001_initial") {
		t.Fatalf("unexpected migration/stdout:\n%s\n%s", contents, stdout.String())
	}
}

func TestMakeMigrationsReportsNoChangesFromProjectMigrationHistory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	meta := models.Metadata{
		AppLabel:  "sales",
		ModelName: "Order",
		TableName: "orders",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "status", Column: "status", Kind: "text", Null: true},
		},
	}
	initial := migrations.Migration{
		AppLabel: "sales",
		Name:     migrations.InitialMigrationName(),
		Atomic:   true,
		Operations: []migrations.Operation{
			operations.CreateModel{Model: migrations.ModelState{
				AppLabel:  "sales",
				Name:      "Order",
				TableName: "orders",
				Fields: []migrations.FieldState{
					{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
					{Name: "status", Column: "status", Kind: "text", Null: true},
				},
			}},
		},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: []models.Metadata{meta}, ProjectMigrations: []migrations.Migration{initial}})

	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"makemigrations", "--app", "sales", "--check", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("makemigrations check error = %v", err)
	}
	if !strings.Contains(stdout.String(), "no changes detected") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestMakeMigrationsDetectsAddFieldOnCustomTable(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	initial := migrations.Migration{
		AppLabel: "sales",
		Name:     migrations.InitialMigrationName(),
		Atomic:   true,
		Operations: []migrations.Operation{
			operations.CreateModel{Model: migrations.ModelState{
				AppLabel:  "sales",
				Name:      "Order",
				TableName: "orders",
				Fields:    []migrations.FieldState{{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true}},
			}},
		},
	}
	meta := models.Metadata{
		AppLabel:  "sales",
		ModelName: "Order",
		TableName: "orders",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "status", Column: "status", Kind: "text", Null: true},
		},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: []models.Metadata{meta}, ProjectMigrations: []migrations.Migration{initial}})

	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"makemigrations", "--app", "sales", "--name", "add_status"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("makemigrations error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "sales", "migrations", "0002_add_status.go"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	for _, want := range []string{`\"type\":\"AddField\"`, `\"table_name\":\"orders\"`, `\"name\":\"status\"`} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("migration missing %q:\n%s", want, contents)
		}
	}
	stdout.Reset()
	if err := root.Execute(context.Background(), []string{"sqlmigrate", "sales", "0002_add_status"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("sqlmigrate new migration error = %v", err)
	}
	if !strings.Contains(stdout.String(), `ALTER TABLE "orders" ADD COLUMN "status" text`) {
		t.Fatalf("sqlmigrate new migration stdout = %q", stdout.String())
	}
}

func TestMakeMigrationsRendersDatabaseDefaultsInSQL(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	statusDefault := models.DefaultValue("draft")
	uuidDefault := models.DefaultSQL("gen_random_uuid()")
	meta := models.Metadata{
		AppLabel:  "sales",
		ModelName: "Order",
		TableName: "orders",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "uuid", PrimaryKey: true, DBDefault: uuidDefault},
			{Name: "status", Column: "status", Kind: "text", DBDefault: statusDefault},
		},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: []models.Metadata{meta}, ProjectMigrations: []migrations.Migration{}})

	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"makemigrations", "--app", "sales"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("makemigrations error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "sales", "migrations", "0001_initial.go"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	for _, want := range []string{`\"db_default\":{\"kind\":\"expression\",\"sql\":\"gen_random_uuid()\"}`, `\"db_default\":{\"kind\":\"literal\",\"value\":\"draft\"}`} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("migration missing %q:\n%s", want, contents)
		}
	}

	stdout.Reset()
	if err := root.Execute(context.Background(), []string{"sqlmigrate", "sales", "0001_initial"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("sqlmigrate error = %v", err)
	}
	for _, want := range []string{`"id" uuid DEFAULT gen_random_uuid()`, `"status" text DEFAULT 'draft' NOT NULL`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("sqlmigrate missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestMakeMigrationsRendersAlterFieldAttributeChangesInSQL(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	oldDefault := models.DefaultValue("draft")
	newDefault := models.DefaultValue("published")
	initial := migrations.Migration{
		AppLabel: "sales",
		Name:     migrations.InitialMigrationName(),
		Atomic:   true,
		Operations: []migrations.Operation{
			operations.CreateModel{Model: migrations.ModelState{
				AppLabel:  "sales",
				Name:      "Order",
				TableName: "orders",
				Fields: []migrations.FieldState{
					{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
					{Name: "status", Column: "status", Kind: "text", Null: true, DBDefault: &oldDefault, DBCollation: "C"},
				},
			}},
		},
	}
	meta := models.Metadata{
		AppLabel:  "sales",
		ModelName: "Order",
		TableName: "orders",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "status", Column: "status", Kind: "varchar(32)", DBDefault: newDefault, DBCollation: "en_US"},
		},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: []models.Metadata{meta}, ProjectMigrations: []migrations.Migration{initial}})

	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"makemigrations", "--app", "sales", "--name", "alter_status"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("makemigrations error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "sales", "migrations", "0002_alter_status.go"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if !strings.Contains(string(contents), `\"type\":\"AlterField\"`) {
		t.Fatalf("migration missing AlterField:\n%s", contents)
	}

	stdout.Reset()
	if err := root.Execute(context.Background(), []string{"sqlmigrate", "sales", "0002_alter_status"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("sqlmigrate error = %v", err)
	}
	for _, want := range []string{
		`ALTER TABLE "orders" ALTER COLUMN "status" TYPE varchar(32);`,
		`ALTER TABLE "orders" ALTER COLUMN "status" SET DEFAULT 'published';`,
		`ALTER TABLE "orders" ALTER COLUMN "status" SET NOT NULL;`,
		`ALTER TABLE "orders" ALTER COLUMN "status" TYPE varchar(32) COLLATE "en_US";`,
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("sqlmigrate missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestMakeMigrationsRejectsUnsafeNonNullAddField(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	initial := migrations.Migration{
		AppLabel: "sales",
		Name:     migrations.InitialMigrationName(),
		Atomic:   true,
		Operations: []migrations.Operation{
			operations.CreateModel{Model: migrations.ModelState{AppLabel: "sales", Name: "Order", TableName: "orders", Fields: []migrations.FieldState{{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true}}}},
		},
	}
	meta := models.Metadata{
		AppLabel:  "sales",
		ModelName: "Order",
		TableName: "orders",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "status", Column: "status", Kind: "text"},
		},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: []models.Metadata{meta}, ProjectMigrations: []migrations.Migration{initial}})

	err := root.Execute(context.Background(), []string{"makemigrations", "--app", "sales"}, &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, migrations.ErrUnsafeMigration) {
		t.Fatalf("makemigrations error = %v, want ErrUnsafeMigration", err)
	}
}

func TestMakeMigrationsDetectsFieldIndexWithoutAlterField(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	initial := migrations.Migration{
		AppLabel: "sales",
		Name:     migrations.InitialMigrationName(),
		Atomic:   true,
		Operations: []migrations.Operation{
			operations.CreateModel{Model: migrations.ModelState{
				AppLabel:  "sales",
				Name:      "Order",
				TableName: "orders",
				Fields: []migrations.FieldState{
					{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
					{Name: "status", Column: "status", Kind: "text", Null: true},
				},
			}},
		},
	}
	meta := models.Metadata{
		AppLabel:  "sales",
		ModelName: "Order",
		TableName: "orders",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "status", Column: "status", Kind: "text", Null: true, DBIndex: true},
		},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: []models.Metadata{meta}, ProjectMigrations: []migrations.Migration{initial}})

	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"makemigrations", "--app", "sales", "--name", "index_status"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("makemigrations error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "sales", "migrations", "0002_index_status.go"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if !strings.Contains(string(contents), `\"type\":\"AddIndex\"`) || strings.Contains(string(contents), `\"type\":\"AlterField\"`) {
		t.Fatalf("unexpected migration operations:\n%s", contents)
	}
	stdout.Reset()
	if err := root.Execute(context.Background(), []string{"sqlmigrate", "sales", "0002_index_status"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("sqlmigrate error = %v", err)
	}
	if !strings.Contains(stdout.String(), `CREATE INDEX`) || !strings.Contains(stdout.String(), `"status"`) {
		t.Fatalf("sqlmigrate missing index SQL:\n%s", stdout.String())
	}
}

func TestMakeMigrationsDetectsDefaultOnlyAlterField(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	oldDefault := models.DefaultValue("draft")
	newDefault := models.DefaultValue("published")
	initial := migrations.Migration{
		AppLabel: "sales",
		Name:     migrations.InitialMigrationName(),
		Atomic:   true,
		Operations: []migrations.Operation{
			operations.CreateModel{Model: migrations.ModelState{
				AppLabel:  "sales",
				Name:      "Order",
				TableName: "orders",
				Fields: []migrations.FieldState{
					{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
					{Name: "status", Column: "status", Kind: "text", Null: true, DBDefault: &oldDefault},
				},
			}},
		},
	}
	meta := models.Metadata{
		AppLabel:  "sales",
		ModelName: "Order",
		TableName: "orders",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "status", Column: "status", Kind: "text", Null: true, DBDefault: newDefault},
		},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: []models.Metadata{meta}, ProjectMigrations: []migrations.Migration{initial}})

	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"makemigrations", "--app", "sales", "--name", "default_status"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("makemigrations error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "sales", "migrations", "0002_default_status.go"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if !strings.Contains(string(contents), `\"type\":\"AlterField\"`) {
		t.Fatalf("migration missing AlterField:\n%s", contents)
	}
	stdout.Reset()
	if err := root.Execute(context.Background(), []string{"sqlmigrate", "sales", "0002_default_status"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("sqlmigrate error = %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, `ALTER TABLE "orders" ALTER COLUMN "status" SET DEFAULT 'published';`) || strings.Contains(output, ` TYPE `) {
		t.Fatalf("unexpected default-only sqlmigrate output:\n%s", output)
	}
}

func TestMakeMigrationsDetectsFieldUniqueWithoutAlterField(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	initial := migrations.Migration{
		AppLabel: "sales",
		Name:     migrations.InitialMigrationName(),
		Atomic:   true,
		Operations: []migrations.Operation{
			operations.CreateModel{Model: migrations.ModelState{
				AppLabel:  "sales",
				Name:      "Order",
				TableName: "orders",
				Fields: []migrations.FieldState{
					{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
					{Name: "status", Column: "status", Kind: "text", Null: true},
				},
			}},
		},
	}
	meta := models.Metadata{
		AppLabel:  "sales",
		ModelName: "Order",
		TableName: "orders",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "status", Column: "status", Kind: "text", Null: true, Unique: true},
		},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: []models.Metadata{meta}, ProjectMigrations: []migrations.Migration{initial}})

	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"makemigrations", "--app", "sales", "--name", "unique_status"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("makemigrations error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "sales", "migrations", "0002_unique_status.go"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if !strings.Contains(string(contents), `\"type\":\"AddConstraint\"`) || strings.Contains(string(contents), `\"type\":\"AlterField\"`) {
		t.Fatalf("unexpected migration operations:\n%s", contents)
	}
}

func TestMakeMigrationsDetectsTableIndexAndConstraintChanges(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	initial := migrations.Migration{
		AppLabel: "sales",
		Name:     migrations.InitialMigrationName(),
		Atomic:   true,
		Operations: []migrations.Operation{
			operations.CreateModel{Model: migrations.ModelState{
				AppLabel:  "sales",
				Name:      "Order",
				TableName: "sales_order",
				Fields: []migrations.FieldState{
					{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
					{Name: "status", Column: "status", Kind: "text", Null: true},
				},
			}},
		},
	}
	meta := models.Metadata{
		AppLabel:  "sales",
		ModelName: "Order",
		TableName: "orders",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "status", Column: "status", Kind: "text", Null: true},
		},
		Indexes:     []models.Index{{Name: "idx_orders_status", Fields: []models.IndexField{models.Asc("status")}}},
		Constraints: []models.Constraint{{Name: "uniq_orders_status", Type: models.ConstraintUnique, Fields: []models.IndexField{models.Asc("status")}}},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: []models.Metadata{meta}, ProjectMigrations: []migrations.Migration{initial}})

	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"makemigrations", "--app", "sales", "--name", "table_indexes"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("makemigrations error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "sales", "migrations", "0002_table_indexes.go"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	for _, want := range []string{`\"type\":\"AlterModelTable\"`, `\"old_table\":\"sales_order\"`, `\"new_table\":\"orders\"`, `\"type\":\"AddIndex\"`, `\"type\":\"AddConstraint\"`} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("migration missing %q:\n%s", want, contents)
		}
	}
}

func TestMakeMigrationsEmitsUniqueIndexesAndRelationForeignKeys(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	initial := migrations.Migration{
		AppLabel: "blog",
		Name:     migrations.InitialMigrationName(),
		Atomic:   true,
		Operations: []migrations.Operation{
			operations.CreateModel{Model: migrations.ModelState{
				AppLabel:  "blog",
				Name:      "Post",
				TableName: "blog_post",
				Fields: []migrations.FieldState{
					{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
					{Name: "author", Column: "author_id", Kind: "bigint"},
					{Name: "title", Column: "title", Kind: "text"},
					{Name: "deleted_at", Column: "deleted_at", Kind: "timestamp", Null: true},
				},
			}},
		},
	}
	projectModels := []models.Metadata{
		{
			AppLabel:  "accounts",
			ModelName: "User",
			TableName: "accounts_user",
			Fields: []models.FieldMeta{
				{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			},
		},
		{
			AppLabel:  "blog",
			ModelName: "Post",
			TableName: "blog_post",
			Fields: []models.FieldMeta{
				{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
				{Name: "author", Column: "author_id", Kind: "bigint", RelationTarget: "accounts.User", DeleteBehavior: "cascade"},
				{Name: "title", Column: "title", Kind: "text"},
				{Name: "deleted_at", Column: "deleted_at", Kind: "timestamp", Null: true},
			},
			Constraints: []models.Constraint{
				models.UniqueExpression("uniq_blog_post_lower_title", "LOWER(title)").WithCondition("deleted_at IS NULL"),
			},
		},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: projectModels, ProjectMigrations: []migrations.Migration{initial}})

	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"makemigrations", "--app", "blog", "--name", "schema_ownership"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("makemigrations error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "blog", "migrations", "0002_schema_ownership.go"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	for _, want := range []string{`\"type\":\"AddIndex\"`, `\"unique\":true`, `\"expressions\":[\"LOWER(title)\"]`, `\"condition_sql\":\"deleted_at IS NULL\"`, `\"type\":\"AddConstraint\"`, `\"type\":\"foreign_key\"`, `\"references_table\":\"accounts_user\"`, `\"on_delete\":\"CASCADE\"`} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("migration missing %q:\n%s", want, contents)
		}
	}
}

func TestSquashMigrationsWritesReplacementMigrationFile(t *testing.T) {
	dir := t.TempDir()
	migrationsDir := filepath.Join(dir, "apps", "blog", "migrations")
	writeGeneratedMigration(t, migrationsDir, migrations.Migration{
		AppLabel:   "blog",
		Name:       "0001_initial",
		Atomic:     true,
		Operations: []migrations.Operation{migrations.ManifestOperation{NameValue: "CreateModel:blog.Item"}},
	})
	writeGeneratedMigration(t, migrationsDir, migrations.Migration{
		AppLabel:   "blog",
		Name:       "0002_post",
		Atomic:     true,
		Operations: []migrations.Operation{migrations.ManifestOperation{NameValue: "AddField:blog.Item.slug"}},
	})
	t.Chdir(dir)

	root := NewRoot()
	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"squashmigrations", "blog", "0001_initial", "0002_post", "--noinput"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("squashmigrations error = %v", err)
	}
	path := filepath.Join(migrationsDir, "0001_squashed_0002_post.go")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected squashed migration file %s: %v", path, err)
	}
	if !strings.Contains(string(contents), `Replaces: []gogomigrations.Dependency`) || !strings.Contains(string(contents), `Name: "0002_post"`) {
		t.Fatalf("squashed migration missing replacement metadata:\n%s", contents)
	}
	if !strings.Contains(stdout.String(), "created squashed migration blog.0001_squashed_0002_post replacing 2 migration(s)") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	writeTextFile(t, filepath.Join(dir, "go.mod"), "module generated-client\n\ngo 1.26.4\n\ntoolchain go1.26.4\n\nrequire github.com/cybersaksham/gogo v0.0.0\n")
	repoRoot := cliTestRepoRoot(t)
	runCLICommand(t, dir, "go", "mod", "edit", "-replace", "github.com/cybersaksham/gogo="+filepath.ToSlash(repoRoot))
	runCLICommand(t, dir, "go", "mod", "tidy")
	runCLICommand(t, dir, "go", "test", "./apps/blog/migrations")
}

func TestSquashedMigrationIsSatisfiedWhenReplacedMigrationsAreApplied(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.sqlite3")
	writeMigrationEnv(t, dir, dbPath)
	migrationsDir := filepath.Join(dir, "apps", "blog", "migrations")
	writeGeneratedMigration(t, migrationsDir, migrations.Migration{
		AppLabel:   "blog",
		Name:       "0001_initial",
		Atomic:     true,
		Operations: []migrations.Operation{migrations.ManifestOperation{NameValue: "CreateModel:blog.Item"}},
	})
	t.Chdir(dir)

	root := NewRoot()
	if err := root.Execute(context.Background(), []string{"migrate"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("initial migrate error = %v", err)
	}
	if err := root.Execute(context.Background(), []string{"squashmigrations", "blog", "0001_initial", "0001_initial", "--noinput"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("squashmigrations error = %v", err)
	}

	var showOut bytes.Buffer
	if err := root.Execute(context.Background(), []string{"showmigrations"}, &showOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("showmigrations error = %v", err)
	}
	if !strings.Contains(showOut.String(), "[X] blog.0001_squashed_0001_initial (replaces applied: 0001_initial)") {
		t.Fatalf("showmigrations stdout = %q", showOut.String())
	}

	var planOut bytes.Buffer
	if err := root.Execute(context.Background(), []string{"migrate", "--plan"}, &planOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("migrate --plan error = %v", err)
	}
	if !strings.Contains(planOut.String(), "no migrations to apply") || strings.Contains(planOut.String(), "0001_squashed_0001_initial") {
		t.Fatalf("migrate --plan stdout = %q", planOut.String())
	}
}

func TestOptimizeMigrationReportsNoopWhenNoSafeRewriteExists(t *testing.T) {
	dir := t.TempDir()
	migrationsDir := filepath.Join(dir, "apps", "blog", "migrations")
	if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	writeTextFile(t, filepath.Join(migrationsDir, "0001_initial.go"), "package migrations\n")
	t.Chdir(dir)

	root := NewRoot()
	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"optimizemigration", "blog", "0001_initial"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("optimizemigration error = %v", err)
	}
	if !strings.Contains(stdout.String(), "no optimizations needed for blog.0001_initial") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func writeMigrationEnv(t *testing.T, dir, dbPath string) {
	t.Helper()
	writeTextFile(t, filepath.Join(dir, ".env"), "GOGO_SECRET_KEY=migration-secret\nDATABASE_URL=sqlite://"+filepath.ToSlash(dbPath)+"\n")
}

func writeGeneratedMigration(t *testing.T, dir string, migration migrations.Migration) {
	t.Helper()
	if _, err := migrations.NewWriter(dir).Write(migration); err != nil {
		t.Fatalf("write migration %s: %v", migration.Identity(), err)
	}
}

func assertSQLiteTableExists(t *testing.T, dbPath, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
		t.Fatalf("expected sqlite table %s: %v", table, err)
	}
}

func assertSQLiteTableMissing(t *testing.T, dbPath, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var name string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	if err == nil {
		t.Fatalf("sqlite table %s exists", table)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("query sqlite table %s: %v", table, err)
	}
}

func assertMigrationRecorded(t *testing.T, dbPath, appLabel, name string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM gogo_migrations WHERE app = ? AND name = ?`, appLabel, name).Scan(&count); err != nil {
		t.Fatalf("query migration record: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration record count = %d, want 1", count)
	}
}

func runCLICommand(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed in %s: %v\n%s", name, strings.Join(args, " "), dir, err, output)
	}
}

func cliTestRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("resolve current test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
