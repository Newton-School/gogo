package testing

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/orm"
	postgresdialect "github.com/Newton-School/gogo/orm/dialects/postgres"
	sqlitedialect "github.com/Newton-School/gogo/orm/dialects/sqlite"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type TestDatabase struct {
	Database      *orm.Database
	Path          string
	removeOnClose bool
}

type Transaction interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type FixtureRow struct {
	Table  string
	Values map[string]any
}

type SQLSchemaEditor struct {
	DB *sql.DB
}

func (e SQLSchemaEditor) Execute(ctx context.Context, statement string, args ...any) error {
	_, err := e.DB.ExecContext(ctx, statement, args...)
	return err
}

func NewSQLiteTestDatabase(ctx context.Context) (*TestDatabase, error) {
	file, err := os.CreateTemp("", "gogo-test-*.sqlite3")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	database, err := orm.OpenDatabase(ctx, orm.DatabaseConfig{
		Name:    orm.DefaultDatabase,
		Driver:  "sqlite",
		DSN:     path,
		Dialect: sqlitedialect.New(),
	})
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return &TestDatabase{Database: database, Path: path, removeOnClose: true}, nil
}

func NewPostgresTestDatabase(ctx context.Context, dsn string) (*TestDatabase, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("postgres test DSN is required")
	}
	database, err := orm.OpenDatabase(ctx, orm.DatabaseConfig{
		Name:    orm.DefaultDatabase,
		Driver:  "pgx",
		DSN:     dsn,
		Dialect: postgresdialect.New(),
	})
	if err != nil {
		return nil, err
	}
	return &TestDatabase{Database: database}, nil
}

func (d *TestDatabase) SQLDB() *sql.DB {
	if d == nil || d.Database == nil {
		return nil
	}
	return d.Database.SQLDB()
}

func (d *TestDatabase) Close() error {
	if d == nil || d.Database == nil {
		return nil
	}
	err := d.Database.Close()
	if d.removeOnClose && d.Path != "" {
		if removeErr := os.Remove(d.Path); removeErr != nil && !os.IsNotExist(removeErr) && err == nil {
			err = removeErr
		}
	}
	return err
}

func (d *TestDatabase) Transaction(ctx context.Context, fn func(context.Context, Transaction) error) (err error) {
	tx, err := d.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback()
			panic(recovered)
		}
	}()
	if err := fn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (d *TestDatabase) RollbackTransaction(ctx context.Context, fn func(context.Context, Transaction) error) (err error) {
	tx, err := d.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback()
			panic(recovered)
		}
	}()
	if err := fn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Rollback()
}

func (d *TestDatabase) LoadFixtures(ctx context.Context, rows []FixtureRow) error {
	for _, row := range rows {
		if len(row.Values) == 0 {
			return fmt.Errorf("fixture row for %s has no values", row.Table)
		}
		columns := sortedKeys(row.Values)
		placeholders := make([]string, len(columns))
		values := make([]any, len(columns))
		for i, column := range columns {
			placeholders[i] = d.Database.Dialect.Placeholder(i + 1)
			values[i] = row.Values[column]
		}
		statement := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", d.quote(row.Table), d.quoteList(columns), strings.Join(placeholders, ", "))
		if _, err := d.SQLDB().ExecContext(ctx, statement, values...); err != nil {
			return err
		}
	}
	return nil
}

func (d *TestDatabase) ApplyMigrations(ctx context.Context, migrationList []migrations.Migration) error {
	executor := migrations.NewExecutor(
		migrations.NewRecorder(d.Database, "test"),
		SQLSchemaEditor{DB: d.SQLDB()},
	)
	return executor.Apply(ctx, migrationList, migrations.ExecutorOptions{})
}

func (d *TestDatabase) Reset(ctx context.Context) error {
	tables, err := d.tables(ctx)
	if err != nil {
		return err
	}
	for _, table := range tables {
		statement := "DROP TABLE " + d.quote(table)
		if d.Database.Dialect.Name() == "postgres" {
			statement += " CASCADE"
		}
		if _, err := d.SQLDB().ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (d *TestDatabase) TableExists(ctx context.Context, table string) (bool, error) {
	switch d.Database.Dialect.Name() {
	case "sqlite":
		var name string
		err := d.SQLDB().QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err == sql.ErrNoRows {
			return false, nil
		}
		return err == nil, err
	case "postgres":
		var name string
		err := d.SQLDB().QueryRowContext(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename = $1`, table).Scan(&name)
		if err == sql.ErrNoRows {
			return false, nil
		}
		return err == nil, err
	default:
		return false, fmt.Errorf("unsupported test database dialect %s", d.Database.Dialect.Name())
	}
}

func (d *TestDatabase) tables(ctx context.Context) ([]string, error) {
	var query string
	switch d.Database.Dialect.Name() {
	case "sqlite":
		query = `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`
	case "postgres":
		query = `SELECT tablename FROM pg_tables WHERE schemaname = 'public'`
	default:
		return nil, fmt.Errorf("unsupported test database dialect %s", d.Database.Dialect.Name())
	}
	rows, err := d.SQLDB().QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables, rows.Err()
}

func (d *TestDatabase) quote(identifier string) string {
	return d.Database.Dialect.QuoteIdent(identifier)
}

func (d *TestDatabase) quoteList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = d.quote(value)
	}
	return strings.Join(quoted, ", ")
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
