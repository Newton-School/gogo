package testing

import (
	"context"
	"os"
	stdtesting "testing"

	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/migrations/operations"
)

func TestSQLiteTestDatabaseLifecycleTransactionsFixturesMigrationsAndReset(t *stdtesting.T) {
	ctx := context.Background()
	database, err := NewSQLiteTestDatabase(ctx)
	if err != nil {
		t.Fatalf("NewSQLiteTestDatabase() error = %v", err)
	}
	path := database.Path
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sqlite database file missing: %v", err)
	}
	defer database.Close()

	if database.Database.Driver != "sqlite" || database.Database.Dialect.Name() != "sqlite" {
		t.Fatalf("database metadata = %#v", database.Database)
	}

	if err := database.ApplyMigrations(ctx, []migrations.Migration{{
		AppLabel: "blog",
		Name:     "0001_initial",
		Operations: []migrations.Operation{
			operations.RunSQL{SQL: `CREATE TABLE blog_post (id INTEGER PRIMARY KEY, title TEXT NOT NULL)`},
		},
	}}); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}

	if err := database.LoadFixtures(ctx, []FixtureRow{{
		Table:  "blog_post",
		Values: map[string]any{"id": 1, "title": "First"},
	}}); err != nil {
		t.Fatalf("LoadFixtures() error = %v", err)
	}
	if count := countRows(t, database, "blog_post"); count != 1 {
		t.Fatalf("fixture row count = %d, want 1", count)
	}

	if err := database.RollbackTransaction(ctx, func(ctx context.Context, tx Transaction) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO blog_post(id, title) VALUES (2, 'Rolled back')`)
		return err
	}); err != nil {
		t.Fatalf("RollbackTransaction() error = %v", err)
	}
	if count := countRows(t, database, "blog_post"); count != 1 {
		t.Fatalf("row count after rollback transaction = %d, want 1", count)
	}

	if err := database.Transaction(ctx, func(ctx context.Context, tx Transaction) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO blog_post(id, title) VALUES (3, 'Committed')`)
		return err
	}); err != nil {
		t.Fatalf("Transaction() error = %v", err)
	}
	if count := countRows(t, database, "blog_post"); count != 2 {
		t.Fatalf("row count after committed transaction = %d, want 2", count)
	}

	if err := database.Reset(ctx); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if exists, err := database.TableExists(ctx, "blog_post"); err != nil || exists {
		t.Fatalf("TableExists(blog_post) after reset = %v, %v", exists, err)
	}

	if err := database.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("sqlite database file still exists after Close: %v", err)
	}
}

func TestPostgresTestDatabaseFromDSN(t *stdtesting.T) {
	dsn := os.Getenv("GOGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GOGO_TEST_POSTGRES_DSN not set")
	}
	database, err := NewPostgresTestDatabase(context.Background(), dsn)
	if err != nil {
		t.Fatalf("NewPostgresTestDatabase() error = %v", err)
	}
	defer database.Close()
	if database.Database.Dialect.Name() != "postgres" {
		t.Fatalf("postgres dialect = %s", database.Database.Dialect.Name())
	}
}

func countRows(t *stdtesting.T, database *TestDatabase, table string) int {
	t.Helper()
	var count int
	if err := database.SQLDB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}
