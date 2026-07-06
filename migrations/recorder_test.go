package migrations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/orm"
	postgresdialect "github.com/Newton-School/gogo/orm/dialects/postgres"
	sqlitedialect "github.com/Newton-School/gogo/orm/dialects/sqlite"
	_ "modernc.org/sqlite"
)

func TestRecorderAppliedHistory(t *testing.T) {
	ctx := context.Background()
	database := openRecorderDB(t)
	defer database.Close()
	recorder := NewRecorder(database, "test-executor")
	if err := recorder.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema() error = %v", err)
	}
	migration := testMigration("blog", "0001_initial")
	if ok, err := recorder.IsApplied(ctx, migration.Dependency()); err != nil || ok {
		t.Fatalf("IsApplied(before) = (%v, %v)", ok, err)
	}
	if err := recorder.RecordApplied(ctx, migration, "abc123"); err != nil {
		t.Fatalf("RecordApplied() error = %v", err)
	}
	if ok, err := recorder.IsApplied(ctx, migration.Dependency()); err != nil || !ok {
		t.Fatalf("IsApplied(after) = (%v, %v)", ok, err)
	}
	applied, err := recorder.Applied(ctx)
	if err != nil {
		t.Fatalf("Applied() error = %v", err)
	}
	if len(applied) != 1 || applied[0].Checksum != "abc123" || applied[0].ExecutorVersion != "test-executor" {
		t.Fatalf("applied = %#v", applied)
	}
	if err := recorder.CheckConsistency(ctx, []Migration{migration}); err != nil {
		t.Fatalf("CheckConsistency() error = %v", err)
	}
	if err := recorder.RecordUnapplied(ctx, migration.Dependency()); err != nil {
		t.Fatalf("RecordUnapplied() error = %v", err)
	}
	if ok, _ := recorder.IsApplied(ctx, migration.Dependency()); ok {
		t.Fatalf("migration still applied")
	}
}

func TestRecorderAcquireLockBlocksConcurrentMigration(t *testing.T) {
	ctx := context.Background()
	database := openRecorderDB(t)
	defer database.Close()
	recorder := NewRecorder(database, "test-executor")

	release, err := recorder.AcquireLock(ctx)
	if err != nil {
		t.Fatalf("AcquireLock() error = %v", err)
	}
	if _, err := recorder.AcquireLock(ctx); !errors.Is(err, ErrMigrationLocked) {
		t.Fatalf("second AcquireLock() error = %v, want ErrMigrationLocked", err)
	}
	if err := release(ctx); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	release, err = recorder.AcquireLock(ctx)
	if err != nil {
		t.Fatalf("AcquireLock() after release error = %v", err)
	}
	if err := release(ctx); err != nil {
		t.Fatalf("second release() error = %v", err)
	}
}

func TestRecorderStatementsUseDialectPlaceholdersAndUpsert(t *testing.T) {
	postgresRecorder := NewRecorder(&orm.Database{Dialect: postgresdialect.New()}, "test-executor")
	postgresSQL := postgresRecorder.statements()
	for _, statement := range []string{postgresSQL.IsApplied, postgresSQL.RecordApplied, postgresSQL.RecordUnapplied} {
		if strings.Contains(statement, "?") {
			t.Fatalf("postgres recorder SQL contains SQLite placeholder: %s", statement)
		}
	}
	if !strings.Contains(postgresSQL.IsApplied, "app = $1 AND name = $2") {
		t.Fatalf("postgres IsApplied SQL = %q, want $ placeholders", postgresSQL.IsApplied)
	}
	if !strings.Contains(postgresSQL.RecordApplied, "VALUES ($1, $2, $3, $4, $5)") {
		t.Fatalf("postgres RecordApplied SQL = %q, want $ placeholders", postgresSQL.RecordApplied)
	}
	if strings.Contains(postgresSQL.RecordApplied, "INSERT OR REPLACE") || !strings.Contains(postgresSQL.RecordApplied, "ON CONFLICT(app, name) DO UPDATE SET") {
		t.Fatalf("postgres RecordApplied SQL = %q, want ON CONFLICT upsert", postgresSQL.RecordApplied)
	}

	sqliteRecorder := NewRecorder(&orm.Database{Dialect: sqlitedialect.New()}, "test-executor")
	sqliteSQL := sqliteRecorder.statements()
	if !strings.Contains(sqliteSQL.IsApplied, "app = ? AND name = ?") {
		t.Fatalf("sqlite IsApplied SQL = %q, want ? placeholders", sqliteSQL.IsApplied)
	}
	if strings.Contains(sqliteSQL.RecordApplied, "INSERT OR REPLACE") || !strings.Contains(sqliteSQL.RecordApplied, "ON CONFLICT(app, name) DO UPDATE SET") {
		t.Fatalf("sqlite RecordApplied SQL = %q, want ON CONFLICT upsert", sqliteSQL.RecordApplied)
	}
}

func openRecorderDB(t *testing.T) *orm.Database {
	t.Helper()
	database, err := orm.OpenDatabase(context.Background(), orm.DatabaseConfig{
		Name:    orm.DefaultDatabase,
		Driver:  "sqlite",
		DSN:     ":memory:",
		Dialect: sqlitedialect.New(),
	})
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	return database
}
