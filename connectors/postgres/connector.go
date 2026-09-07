// Package postgres implements Gogo's relational contracts using pgx and database/sql.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

type Config struct {
	DSN, Alias                  string
	SearchPath                  string
	MaxOpen, MaxIdle            int
	ConnectTimeout, MaxLifetime time.Duration
	Production                  bool
	RequiredExtensions          []string
}
type Backend struct {
	pool         *sql.DB
	alias        string
	capabilities db.Capabilities
}

func (*Backend) MaxParameters() int { return 65535 }

func Open(ctx context.Context, config Config) (*Backend, error) {
	if config.DSN == "" {
		return nil, errors.New("postgres: DSN is required")
	}
	parsed, err := pgx.ParseConfig(config.DSN)
	if err != nil {
		return nil, errors.New("postgres: invalid connection configuration")
	}
	if config.Production {
		if parsed.TLSConfig == nil || parsed.TLSConfig.InsecureSkipVerify {
			return nil, errors.New("postgres: production requires verified TLS")
		}
		for _, fallback := range parsed.Fallbacks {
			if fallback.TLSConfig == nil || fallback.TLSConfig.InsecureSkipVerify {
				return nil, errors.New("postgres: insecure TLS fallback is not allowed")
			}
		}
	}
	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = 5 * time.Second
	}
	if config.ConnectTimeout < 0 || config.MaxOpen < 0 || config.MaxIdle < 0 || config.MaxLifetime < 0 {
		return nil, errors.New("postgres: negative connection limit")
	}
	if config.MaxOpen == 0 {
		config.MaxOpen = 20
	}
	if config.MaxIdle == 0 {
		config.MaxIdle = 5
	}
	if config.MaxIdle > config.MaxOpen {
		return nil, errors.New("postgres: idle connections exceed open limit")
	}
	if config.Alias == "" {
		config.Alias = "default"
	}
	parsed.ConnectTimeout = config.ConnectTimeout
	if config.SearchPath != "" {
		if !models.ValidIdentifier(config.SearchPath) {
			return nil, errors.New("postgres: invalid search path")
		}
		parsed.RuntimeParams["search_path"] = config.SearchPath
	}
	pool := stdlib.OpenDB(*parsed)
	pool.SetMaxOpenConns(config.MaxOpen)
	pool.SetMaxIdleConns(config.MaxIdle)
	pool.SetConnMaxLifetime(config.MaxLifetime)
	backend := &Backend{pool: pool, alias: config.Alias, capabilities: db.Capabilities{"returning": true, "on_conflict": true, "transactions": true, "savepoints": true, "row_locks": true, "row_lock_of": true, "row_lock_no_key": true, "json": true, "arrays": true, "ranges": true, "window": true, "regex": true, "distinct_on": true, "filtered_aggregates": true, "conditional_expressions": true, "grouped_queries": true, "grouped_relation_joins": true, "transactional_ddl": true, "concurrent_indexes": true}}
	// PostgreSQL's UPDATE command count includes matched rows whose values did
	// not change. Query.Update must not silently promise this for every driver.
	backend.capabilities["update_matched_rows"] = true
	probe, cancel := context.WithTimeout(ctx, config.ConnectTimeout)
	defer cancel()
	if err := backend.Ping(probe); err != nil {
		pool.Close()
		return nil, err
	}
	var versionText string
	if err := pool.QueryRowContext(probe, "SHOW server_version_num").Scan(&versionText); err != nil {
		pool.Close()
		return nil, translate(err, false)
	}
	version, err := strconv.Atoi(versionText)
	if err != nil || version < 160000 {
		pool.Close()
		return nil, &db.Error{Code: db.UnsupportedFeature, Message: "PostgreSQL 16 or newer is required"}
	}
	rows, err := pool.QueryContext(probe, "SELECT extname FROM pg_extension")
	if err != nil {
		pool.Close()
		return nil, translate(err, false)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			pool.Close()
			return nil, translate(err, false)
		}
		backend.capabilities["extension:"+name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		pool.Close()
		return nil, translate(err, false)
	}
	for _, extension := range config.RequiredExtensions {
		if !backend.capabilities["extension:"+extension] {
			pool.Close()
			return nil, &db.Error{Code: db.UnsupportedFeature, Message: "Required PostgreSQL extension is not installed"}
		}
	}
	return backend, nil
}
func (b *Backend) Alias() string { return b.alias }
func (b *Backend) Capabilities() db.Capabilities {
	copy := db.Capabilities{}
	for k, v := range b.capabilities {
		copy[k] = v
	}
	return copy
}
func (b *Backend) Dialect() db.Dialect            { return Dialect{Capabilities: b.Capabilities()} }
func (b *Backend) Close() error                   { return b.pool.Close() }
func (b *Backend) Ping(ctx context.Context) error { return translate(b.pool.PingContext(ctx), false) }
func (b *Backend) Exec(ctx context.Context, q string, args ...any) (db.Result, error) {
	result, err := b.pool.ExecContext(ctx, q, args...)
	return result, translate(err, false)
}
func (b *Backend) Query(ctx context.Context, q string, args ...any) (db.Rows, error) {
	rows, err := b.pool.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, translate(err, false)
	}
	return safeRows{rows}, nil
}
func (b *Backend) BeginTx(ctx context.Context, o db.TxOptions) (db.Transaction, error) {
	tx, err := b.pool.BeginTx(ctx, &sql.TxOptions{Isolation: o.Isolation, ReadOnly: o.ReadOnly})
	if err != nil {
		return nil, translate(err, false)
	}
	return &transaction{tx}, nil
}
func (b *Backend) Acquire(ctx context.Context) (db.Connection, error) {
	conn, err := b.pool.Conn(ctx)
	if err != nil {
		return nil, translate(err, false)
	}
	return &connection{conn}, nil
}

type safeRows struct{ *sql.Rows }

func (r safeRows) Scan(dest ...any) error { return translate(r.Rows.Scan(dest...), false) }
func (r safeRows) Err() error             { return translate(r.Rows.Err(), false) }

type transaction struct{ *sql.Tx }

func (t *transaction) Exec(ctx context.Context, q string, args ...any) (db.Result, error) {
	result, err := t.Tx.ExecContext(ctx, q, args...)
	return result, translate(err, false)
}
func (t *transaction) Query(ctx context.Context, q string, args ...any) (db.Rows, error) {
	rows, err := t.Tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, translate(err, false)
	}
	return safeRows{rows}, nil
}
func (t *transaction) Commit() error { return translate(t.Tx.Commit(), true) }
func (t *transaction) Rollback() error {
	err := t.Tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return translate(err, false)
}

type connection struct{ *sql.Conn }

func (c *connection) Exec(ctx context.Context, q string, args ...any) (db.Result, error) {
	result, err := c.Conn.ExecContext(ctx, q, args...)
	return result, translate(err, false)
}
func (c *connection) Query(ctx context.Context, q string, args ...any) (db.Rows, error) {
	rows, err := c.Conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, translate(err, false)
	}
	return safeRows{rows}, nil
}
func (c *connection) BeginTx(ctx context.Context, o db.TxOptions) (db.Transaction, error) {
	tx, err := c.Conn.BeginTx(ctx, &sql.TxOptions{Isolation: o.Isolation, ReadOnly: o.ReadOnly})
	if err != nil {
		return nil, translate(err, false)
	}
	return &transaction{tx}, nil
}
func translate(err error, commit bool) error {
	if err == nil {
		return nil
	}
	if !commit && errors.Is(err, sql.ErrNoRows) {
		return db.ErrNoRows
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		// A server can explicitly report that a statement/transaction outcome
		// is unknown. Connection-exception SQLSTATEs cannot confirm COMMIT
		// rollback either; preserve the same conservative recovery contract.
		if commit && (strings.HasPrefix(pg.Code, "08") || pg.Code == "40003") {
			return &db.Error{Code: db.UnknownCommit, Message: "Commit outcome is unknown; reconcile before retry"}
		}
		code := db.Unavailable
		switch pg.Code {
		case "23505":
			code = db.UniqueViolation
		case "23503":
			code = db.ForeignKeyViolation
		case "23514":
			code = db.CheckViolation
		case "23502":
			code = db.NotNullViolation
		case "40001":
			code = db.SerializationFailure
		case "40P01":
			code = db.Deadlock
		case "57014":
			code = db.Canceled
		}
		return &db.Error{Code: code, Constraint: pg.ConstraintName, Message: "PostgreSQL operation failed (" + pg.Code + ")"}
	}
	if commit && errors.Is(err, pgx.ErrTxCommitRollback) {
		// pgx received a ROLLBACK command tag in response to COMMIT. This
		// specific result is positive rollback evidence, unlike ErrTxDone.
		return &db.Error{Code: db.Unavailable, Message: "PostgreSQL transaction rolled back instead of committing"}
	}
	if commit {
		// pgx's database/sql wrapper forwards the transaction context to
		// COMMIT. Cancellation/timeout can arrive after submission and lose
		// the acknowledgement. A raw context error is not rollback proof.
		return &db.Error{Code: db.UnknownCommit, Message: "Commit outcome is unknown; reconcile before retry"}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &db.Error{Code: db.Canceled, Message: "Database operation canceled", Cause: err}
	}
	return &db.Error{Code: db.Unavailable, Message: "PostgreSQL operation unavailable"}
}
func (b *Backend) SchemaEditor() db.SchemaEditor {
	return SchemaEditor{Dialect: Dialect{Capabilities: b.Capabilities()}}
}
func (b *Backend) Introspector() db.Introspector { return Introspector{} }

// Native uses a dedicated connection for explicit PostgreSQL capabilities such
// as CopyFrom or Listen/WaitForNotification; the connection never escapes fn.
func (b *Backend) Native(ctx context.Context, fn func(*pgx.Conn) error) error {
	if fn == nil {
		return errors.New("postgres: nil native callback")
	}
	conn, err := b.pool.Conn(ctx)
	if err != nil {
		return translate(err, false)
	}
	defer conn.Close()
	return conn.Raw(func(driverConn any) error {
		wrapped, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return errors.New("postgres: unexpected driver")
		}
		return translate(fn(wrapped.Conn()), false)
	})
}
func placeholderList(start, count int) string {
	items := make([]string, count)
	for i := range items {
		items[i] = fmt.Sprintf("$%d", start+i)
	}
	return strings.Join(items, ", ")
}
