package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Newton-School/gogo/orm"
)

// AppliedMigration stores one migration history row.
type AppliedMigration struct {
	AppLabel        string
	Name            string
	Applied         time.Time
	Checksum        string
	ExecutorVersion string
}

// Recorder stores migration history in the database.
type Recorder struct {
	Database        *orm.Database
	ExecutorVersion string
}

// NewRecorder creates a migration recorder.
func NewRecorder(database *orm.Database, executorVersion string) Recorder {
	return Recorder{Database: database, ExecutorVersion: executorVersion}
}

type recorderStatements struct {
	EnsureSchema    string
	EnsureLock      string
	AcquireLock     string
	ReleaseLock     string
	IsApplied       string
	RecordApplied   string
	RecordUnapplied string
	Applied         string
}

// EnsureSchema creates the migration history table.
func (r Recorder) EnsureSchema(ctx context.Context) error {
	_, err := r.db().ExecContext(ctx, r.statements().EnsureSchema)
	return err
}

// AcquireLock takes the database-backed migration lock and returns a release function.
func (r Recorder) AcquireLock(ctx context.Context) (func(context.Context) error, error) {
	statements := r.statements()
	if _, err := r.db().ExecContext(ctx, statements.EnsureLock); err != nil {
		return nil, err
	}
	if _, err := r.db().ExecContext(ctx, statements.AcquireLock, "default", time.Now().UTC(), r.ExecutorVersion); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMigrationLocked, err)
	}
	return func(ctx context.Context) error {
		_, err := r.db().ExecContext(ctx, statements.ReleaseLock, "default")
		return err
	}, nil
}

// IsApplied reports whether a migration is applied.
func (r Recorder) IsApplied(ctx context.Context, dependency Dependency) (bool, error) {
	var exists int
	err := r.db().QueryRowContext(ctx, r.statements().IsApplied, dependency.AppLabel, dependency.Name).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// RecordApplied inserts or replaces an applied migration row.
func (r Recorder) RecordApplied(ctx context.Context, migration Migration, checksum string) error {
	if err := r.EnsureSchema(ctx); err != nil {
		return err
	}
	_, err := r.db().ExecContext(ctx, r.statements().RecordApplied, migration.AppLabel, migration.Name, time.Now().UTC(), checksum, r.ExecutorVersion)
	return err
}

// RecordUnapplied deletes an applied migration row.
func (r Recorder) RecordUnapplied(ctx context.Context, dependency Dependency) error {
	_, err := r.db().ExecContext(ctx, r.statements().RecordUnapplied, dependency.AppLabel, dependency.Name)
	return err
}

// Applied returns applied migration history.
func (r Recorder) Applied(ctx context.Context) ([]AppliedMigration, error) {
	rows, err := r.db().QueryContext(ctx, r.statements().Applied)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var applied []AppliedMigration
	for rows.Next() {
		var item AppliedMigration
		if err := rows.Scan(&item.AppLabel, &item.Name, &item.Applied, &item.Checksum, &item.ExecutorVersion); err != nil {
			return nil, err
		}
		applied = append(applied, item)
	}
	return applied, rows.Err()
}

// CheckConsistency verifies all applied rows exist in known migrations.
func (r Recorder) CheckConsistency(ctx context.Context, known []Migration) error {
	applied, err := r.Applied(ctx)
	if err != nil {
		return err
	}
	knownSet := map[string]bool{}
	for _, migration := range known {
		knownSet[migration.Identity()] = true
	}
	for _, item := range applied {
		key := item.AppLabel + "." + item.Name
		if !knownSet[key] {
			return fmt.Errorf("%w: %s", ErrInconsistentMigrationHistory, key)
		}
	}
	return nil
}

func (r Recorder) db() *sql.DB {
	return r.Database.SQLDB()
}

func (r Recorder) statements() recorderStatements {
	placeholder := r.placeholder
	return recorderStatements{
		EnsureSchema: `CREATE TABLE IF NOT EXISTS gogo_migrations (app TEXT NOT NULL, name TEXT NOT NULL, applied TIMESTAMP NOT NULL, checksum TEXT NOT NULL, executor_version TEXT NOT NULL, PRIMARY KEY(app, name))`,
		EnsureLock:   `CREATE TABLE IF NOT EXISTS gogo_migration_lock (name TEXT NOT NULL PRIMARY KEY, acquired TIMESTAMP NOT NULL, owner TEXT NOT NULL)`,
		AcquireLock: fmt.Sprintf(
			`INSERT INTO gogo_migration_lock(name, acquired, owner) VALUES (%s, %s, %s)`,
			placeholder(1),
			placeholder(2),
			placeholder(3),
		),
		ReleaseLock: fmt.Sprintf(`DELETE FROM gogo_migration_lock WHERE name = %s`, placeholder(1)),
		IsApplied:   fmt.Sprintf(`SELECT 1 FROM gogo_migrations WHERE app = %s AND name = %s`, placeholder(1), placeholder(2)),
		RecordApplied: fmt.Sprintf(
			`INSERT INTO gogo_migrations(app, name, applied, checksum, executor_version) VALUES (%s, %s, %s, %s, %s) ON CONFLICT(app, name) DO UPDATE SET applied = EXCLUDED.applied, checksum = EXCLUDED.checksum, executor_version = EXCLUDED.executor_version`,
			placeholder(1),
			placeholder(2),
			placeholder(3),
			placeholder(4),
			placeholder(5),
		),
		RecordUnapplied: fmt.Sprintf(`DELETE FROM gogo_migrations WHERE app = %s AND name = %s`, placeholder(1), placeholder(2)),
		Applied:         `SELECT app, name, applied, checksum, executor_version FROM gogo_migrations ORDER BY app, name`,
	}
}

func (r Recorder) placeholder(position int) string {
	if r.Database != nil && r.Database.Dialect != nil {
		return r.Database.Dialect.Placeholder(position)
	}
	return "?"
}
