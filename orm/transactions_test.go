package orm

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/orm/dialects/postgres"
	sqlitedialect "github.com/Newton-School/gogo/orm/dialects/sqlite"
	_ "modernc.org/sqlite"
)

func TestAtomicCommitRollbackAndOnCommit(t *testing.T) {
	ctx := context.Background()
	database := openTransactionTestDB(t)
	defer database.Close()
	manager := NewTransactionManager(database)

	committed := false
	err := manager.Atomic(ctx, func(ctx context.Context, tx *Transaction) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO items(name) VALUES (?)`, "commit"); err != nil {
			return err
		}
		tx.OnCommit(func() { committed = true })
		return nil
	})
	if err != nil {
		t.Fatalf("Atomic(commit) error = %v", err)
	}
	if countRows(t, database, "commit") != 1 || !committed {
		t.Fatalf("commit row/callback mismatch")
	}

	failure := errors.New("rollback")
	err = manager.Atomic(ctx, func(ctx context.Context, tx *Transaction) error {
		_, _ = tx.ExecContext(ctx, `INSERT INTO items(name) VALUES (?)`, "rollback")
		tx.OnCommit(func() { t.Fatalf("rollback callback executed") })
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("Atomic(rollback) error = %v", err)
	}
	if countRows(t, database, "rollback") != 0 {
		t.Fatalf("rollback row was committed")
	}
}

func TestAtomicNestedSavepointRollback(t *testing.T) {
	ctx := context.Background()
	database := openTransactionTestDB(t)
	defer database.Close()
	manager := NewTransactionManager(database)

	err := manager.Atomic(ctx, func(ctx context.Context, tx *Transaction) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO items(name) VALUES (?)`, "outer"); err != nil {
			return err
		}
		nestedErr := manager.Atomic(ctx, func(ctx context.Context, tx *Transaction) error {
			_, _ = tx.ExecContext(ctx, `INSERT INTO items(name) VALUES (?)`, "inner")
			return errors.New("inner failed")
		})
		if nestedErr == nil {
			t.Fatalf("nested transaction did not fail")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("outer Atomic() error = %v", err)
	}
	if countRows(t, database, "outer") != 1 || countRows(t, database, "inner") != 0 {
		t.Fatalf("nested savepoint did not preserve only outer row")
	}
}

func TestAtomicRollbackOnPanic(t *testing.T) {
	ctx := context.Background()
	database := openTransactionTestDB(t)
	defer database.Close()
	manager := NewTransactionManager(database)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatalf("panic was not rethrown")
			}
		}()
		_ = manager.Atomic(ctx, func(ctx context.Context, tx *Transaction) error {
			_, _ = tx.ExecContext(ctx, `INSERT INTO items(name) VALUES (?)`, "panic")
			panic("boom")
		})
	}()
	if countRows(t, database, "panic") != 0 {
		t.Fatalf("panic row was committed")
	}
}

func TestAtomicOptionsAndLockSQL(t *testing.T) {
	ctx := context.Background()
	database := openTransactionTestDB(t)
	defer database.Close()
	manager := NewTransactionManager(database)

	err := manager.Atomic(ctx, func(context.Context, *Transaction) error {
		return nil
	}, WithIsolation(sql.LevelSerializable), ReadOnly())
	if err != nil {
		t.Fatalf("Atomic(options) error = %v", err)
	}

	sql, err := NewQuerySet(testCompilerModel(), NewCompiler(postgres.New())).
		SelectForUpdate(LockState{ForUpdate: true, NoWait: true}).
		Iterator()
	if err != nil {
		t.Fatalf("SelectForUpdate Iterator() error = %v", err)
	}
	if !strings.HasSuffix(sql.SQL, "FOR UPDATE NOWAIT") {
		t.Fatalf("lock SQL = %q", sql.SQL)
	}
}

func openTransactionTestDB(t *testing.T) *Database {
	t.Helper()
	database, err := OpenDatabase(context.Background(), DatabaseConfig{
		Name:    DefaultDatabase,
		Driver:  "sqlite",
		DSN:     ":memory:",
		Dialect: sqlitedialect.New(),
	})
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	if _, err := database.SQLDB().Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return database
}

func countRows(t *testing.T, database *Database, name string) int {
	t.Helper()
	var count int
	if err := database.SQLDB().QueryRow(`SELECT COUNT(*) FROM items WHERE name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}
