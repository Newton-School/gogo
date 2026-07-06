package auth

import (
	"context"
	"strings"
	"testing"

	authmigrations "github.com/Newton-School/gogo/auth/migrations"
	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/orm"
	sqlitedialect "github.com/Newton-School/gogo/orm/dialects/sqlite"
	_ "modernc.org/sqlite"
)

func TestInitialAuthMigrationDefinesTablesAndExecutes(t *testing.T) {
	ctx := context.Background()
	migration := authmigrations.Initial()
	if err := migration.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	sql := migrationSQL(migration)
	required := []string{
		"CREATE TABLE gogo_content_type",
		"CREATE TABLE auth_permission",
		"CREATE TABLE auth_group",
		"CREATE TABLE auth_group_permissions",
		"CREATE TABLE auth_user",
		"CREATE TABLE auth_user_groups",
		"CREATE TABLE auth_user_user_permissions",
		"CREATE TABLE gogo_session",
		"UNIQUE(content_type_id, codename)",
		"UNIQUE(group_id, permission_id)",
		"UNIQUE(user_id, group_id)",
		"UNIQUE(user_id, permission_id)",
		"CREATE INDEX gogo_session_expire_date_idx",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("migration SQL missing %q in:\n%s", fragment, sql)
		}
	}

	db, err := orm.OpenDatabase(ctx, orm.DatabaseConfig{
		Name:    orm.DefaultDatabase,
		Driver:  "sqlite",
		DSN:     ":memory:",
		Dialect: sqlitedialect.New(),
	})
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	defer db.Close()

	recorder := migrations.NewRecorder(db, "auth-test")
	editor := &recordingSchemaEditor{}
	executor := migrations.NewExecutor(recorder, editor)
	if err := executor.Apply(ctx, []migrations.Migration{migration}, migrations.ExecutorOptions{}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(editor.SQL) != len(migration.Operations) {
		t.Fatalf("executed SQL count = %d, want %d", len(editor.SQL), len(migration.Operations))
	}
	if applied, _ := recorder.IsApplied(ctx, migration.Dependency()); !applied {
		t.Fatalf("migration not recorded applied")
	}
}

func migrationSQL(migration migrations.Migration) string {
	editor := &recordingSchemaEditor{}
	for _, operation := range migration.Operations {
		_ = operation.DatabaseForwards(context.Background(), editor)
	}
	return strings.Join(editor.SQL, "\n")
}

type recordingSchemaEditor struct {
	SQL []string
}

func (e *recordingSchemaEditor) Execute(ctx context.Context, sql string, args ...any) error {
	_ = ctx
	_ = args
	e.SQL = append(e.SQL, sql)
	return nil
}
