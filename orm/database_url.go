package orm

import (
	"context"
	"errors"
	"strings"

	postgresdialect "github.com/Newton-School/gogo/orm/dialects/postgres"
	sqlitedialect "github.com/Newton-School/gogo/orm/dialects/sqlite"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// DatabaseConfigFromURL converts a Django-style DATABASE_URL into a database config.
func DatabaseConfigFromURL(alias, databaseURL string) (DatabaseConfig, error) {
	if strings.TrimSpace(alias) == "" {
		alias = DefaultDatabase
	}
	switch {
	case strings.HasPrefix(databaseURL, "sqlite://"):
		dsn := strings.TrimPrefix(databaseURL, "sqlite://")
		if dsn == "" {
			return DatabaseConfig{}, errors.New("sqlite database path is empty")
		}
		return DatabaseConfig{Name: alias, Driver: "sqlite", DSN: dsn, Dialect: sqlitedialect.New()}, nil
	case strings.HasPrefix(databaseURL, "sqlite3://"):
		dsn := strings.TrimPrefix(databaseURL, "sqlite3://")
		if dsn == "" {
			return DatabaseConfig{}, errors.New("sqlite database path is empty")
		}
		return DatabaseConfig{Name: alias, Driver: "sqlite", DSN: dsn, Dialect: sqlitedialect.New()}, nil
	case strings.HasPrefix(databaseURL, "postgres://"), strings.HasPrefix(databaseURL, "postgresql://"):
		return DatabaseConfig{Name: alias, Driver: "pgx", DSN: databaseURL, Dialect: postgresdialect.New()}, nil
	default:
		return DatabaseConfig{}, errors.New("unsupported or empty DATABASE_URL")
	}
}

// OpenDatabaseURL opens a database configured by DATABASE_URL syntax.
func OpenDatabaseURL(ctx context.Context, alias, databaseURL string) (*Database, error) {
	config, err := DatabaseConfigFromURL(alias, databaseURL)
	if err != nil {
		return nil, err
	}
	return OpenDatabase(ctx, config)
}
