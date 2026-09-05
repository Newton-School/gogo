// Package db defines public relational backend contracts and transaction control.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/Newton-School/gogo/core/models"
)

type Rows interface {
	Columns() ([]string, error)
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}
type Result interface{ RowsAffected() (int64, error) }
type Executor interface {
	Exec(context.Context, string, ...any) (Result, error)
	Query(context.Context, string, ...any) (Rows, error)
}
type Transaction interface {
	Executor
	Commit() error
	Rollback() error
}
type Connection interface {
	Executor
	BeginTx(context.Context, TxOptions) (Transaction, error)
	Close() error
}
type TxOptions struct {
	Isolation sql.IsolationLevel
	ReadOnly  bool
}
type Capabilities map[string]bool

func (c Capabilities) Require(names ...string) error {
	for _, name := range names {
		if !c[name] {
			return &Error{Code: UnsupportedFeature, Message: "Backend does not support " + name}
		}
	}
	return nil
}

type Dialect interface {
	Name() string
	QuoteIdentifier(string) (string, error)
	Placeholder(int) string
	FieldType(models.Field) (string, error)
}

// FeatureDialect advertises optional SQL syntax without coupling the private
// compiler to a concrete connector name. Unknown capabilities fail closed.
type FeatureDialect interface {
	SupportsFeature(string) bool
}
type Backend interface {
	Executor
	Alias() string
	BeginTx(context.Context, TxOptions) (Transaction, error)
	Acquire(context.Context) (Connection, error)
	Ping(context.Context) error
	Close() error
	Dialect() Dialect
	Capabilities() Capabilities
}
type SchemaEditor interface {
	CreateModel(context.Context, Executor, models.Schema) error
	DeleteModel(context.Context, Executor, models.Schema) error
	AddField(context.Context, Executor, models.Schema, models.Field) error
	RemoveField(context.Context, Executor, models.Schema, models.Field) error
	AlterField(context.Context, Executor, models.Schema, models.Field, models.Field) error
	RenameField(context.Context, Executor, models.Schema, string, string) error
	AddIndex(context.Context, Executor, models.Schema, models.Index) error
	RemoveIndex(context.Context, Executor, string) error
}
type Introspector interface {
	Introspect(context.Context, Executor, string) (models.Schema, error)
	Tables(context.Context, Executor) ([]string, error)
}

// SchemaResolverEditor binds historical model metadata to relational DDL. It
// returns a separate editor so migration state never mutates a shared backend.
type SchemaResolverEditor interface {
	WithSchemas([]models.Schema) (SchemaEditor, error)
}

// AutoKeyAllocator reserves generated numeric keys before a batch so RETURNING
// values can be matched by identity, never by undocumented database row order.
type AutoKeyAllocator interface {
	ReserveAutoKeys(context.Context, Executor, models.Schema, models.Field, int) ([]any, error)
}

// DeferredSchemaEditor emits relation tables only after all primary tables in
// a migration exist, allowing source/target creation order to remain explicit.
type DeferredSchemaEditor interface {
	FlushDeferred(context.Context, Executor) error
}

// ParameterLimiter advertises the maximum bind parameters in one statement.
// Query planners batch below this connector limit; unknown connectors use 500.
type ParameterLimiter interface{ MaxParameters() int }

// MigrationLocker holds a dedicated session lock for the entire migration run.
type MigrationLocker interface {
	LockMigrations(context.Context) (func() error, error)
}

type ErrorCode string

const (
	UniqueViolation      ErrorCode = "unique_violation"
	ForeignKeyViolation  ErrorCode = "foreign_key_violation"
	CheckViolation       ErrorCode = "check_violation"
	NotNullViolation     ErrorCode = "not_null_violation"
	SerializationFailure ErrorCode = "serialization_failure"
	Deadlock             ErrorCode = "deadlock"
	Unavailable          ErrorCode = "unavailable"
	Canceled             ErrorCode = "canceled"
	UnsupportedFeature   ErrorCode = "unsupported_feature"
	UnknownCommit        ErrorCode = "unknown_commit"
)

// Error deliberately excludes raw SQL, parameter values and connection strings.
type Error struct {
	Code                ErrorCode
	Message, Constraint string
	Cause               error
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "database operation failed: " + string(e.Code)
}
func (e *Error) Unwrap() error { return e.Cause }
func IsCode(err error, code ErrorCode) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

var ErrNoRows = sql.ErrNoRows

func QueryRow(ctx context.Context, executor Executor, query string, args []any, dest ...any) error {
	rows, err := executor.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return ErrNoRows
	}
	if err := rows.Scan(dest...); err != nil {
		return err
	}
	return rows.Err()
}
func Quote(dialect Dialect, identifier string) (string, error) {
	value, err := dialect.QuoteIdentifier(identifier)
	if err != nil {
		return "", fmt.Errorf("db: invalid identifier: %w", err)
	}
	return value, nil
}
