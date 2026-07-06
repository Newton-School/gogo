package orm

import (
	"context"
	"errors"
	"testing"

	sqlitedialect "github.com/Newton-School/gogo/orm/dialects/sqlite"
	_ "modernc.org/sqlite"
)

type rawItem struct {
	ID   int64
	Name string
}

func TestRawSelectMapsRowsAndBindsParameters(t *testing.T) {
	ctx := context.Background()
	database := openRawTestDB(t)
	defer database.Close()
	executor := NewRawExecutor(database)

	if _, err := executor.Exec(ctx, ParameterizedRaw(`INSERT INTO items(name) VALUES (?)`, "one")); err != nil {
		t.Fatalf("Exec(insert) error = %v", err)
	}
	items, err := RawSelect(ctx, executor, ParameterizedRaw(`SELECT id, name FROM items WHERE name = ?`, "one"), func(scanner RowScanner) (rawItem, error) {
		var item rawItem
		err := scanner.Scan(&item.ID, &item.Name)
		return item, err
	})
	if err != nil {
		t.Fatalf("RawSelect() error = %v", err)
	}
	if len(items) != 1 || items[0].Name != "one" {
		t.Fatalf("items = %#v", items)
	}
}

func TestRawExecRowsAffected(t *testing.T) {
	ctx := context.Background()
	database := openRawTestDB(t)
	defer database.Close()
	executor := NewRawExecutor(database)

	result, err := executor.Exec(ctx, ParameterizedRaw(`INSERT INTO items(name) VALUES (?)`, "two"))
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if result.RowsAffected != 1 {
		t.Fatalf("RowsAffected = %d", result.RowsAffected)
	}
}

func TestRawSQLRequiresUnsafeMarkerWithoutParameters(t *testing.T) {
	executor := NewRawExecutor(openRawTestDB(t))
	defer executor.Database.Close()

	if _, err := executor.Exec(context.Background(), RawQuery{SQL: `VACUUM`}); !errors.Is(err, ErrUnsafeRawSQL) {
		t.Fatalf("Exec(unmarked) error = %v, want ErrUnsafeRawSQL", err)
	}
	if _, err := executor.Exec(context.Background(), UnsafeRawQuery(`VACUUM`)); err != nil {
		t.Fatalf("Exec(unsafe) error = %v", err)
	}
}

func openRawTestDB(t *testing.T) *Database {
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
