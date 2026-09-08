package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/jackc/pgx/v5/pgconn"
)

// This driver exposes a successful RETURNING row followed by a completion
// failure. It exercises database/sql's real Close/Err behavior without claiming
// to simulate a network failure or to determine an autocommit write's outcome.
type closeErrorConnector struct{ rows *closeErrorRows }

func (c closeErrorConnector) Connect(context.Context) (driver.Conn, error) {
	return closeErrorConnection{c.rows}, nil
}
func (c closeErrorConnector) Driver() driver.Driver { return closeErrorDriver{c.rows} }

type closeErrorDriver struct{ rows *closeErrorRows }

func (d closeErrorDriver) Open(string) (driver.Conn, error) {
	return closeErrorConnection{d.rows}, nil
}

type closeErrorConnection struct{ rows *closeErrorRows }

func (closeErrorConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fixture does not prepare statements")
}
func (closeErrorConnection) Close() error { return nil }
func (closeErrorConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("fixture does not start transactions")
}
func (c closeErrorConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return c.rows, nil
}

type closeErrorRows struct {
	failure error
	read    bool
	closes  int
}

func (*closeErrorRows) Columns() []string { return []string{"value"} }
func (r *closeErrorRows) Close() error {
	r.closes++
	return r.failure
}
func (r *closeErrorRows) Next(values []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	values[0] = int64(7)
	return nil
}

func TestRowsCloseTranslatesDeferredErrorsWithoutProviderDetails(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure error
		code    db.ErrorCode
		cause   error
	}{
		{"success", nil, "", nil},
		{"foreign-key", &pgconn.PgError{Code: "23503", ConstraintName: "fixture_fk", Message: "synthetic-private-provider-detail", Detail: "synthetic-private-value"}, db.ForeignKeyViolation, nil},
		{"unique", &pgconn.PgError{Code: "23505", ConstraintName: "fixture_unique", Message: "synthetic-private-provider-detail"}, db.UniqueViolation, nil},
		{"check", &pgconn.PgError{Code: "23514", ConstraintName: "fixture_check"}, db.CheckViolation, nil},
		{"not-null", &pgconn.PgError{Code: "23502"}, db.NotNullViolation, nil},
		{"serialization", &pgconn.PgError{Code: "40001"}, db.SerializationFailure, nil},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, db.Deadlock, nil},
		{"server-canceled", &pgconn.PgError{Code: "57014"}, db.Canceled, nil},
		{"context-canceled", fmt.Errorf("synthetic-private-wrapper: %w", context.Canceled), db.Canceled, context.Canceled},
		{"context-deadline", context.DeadlineExceeded, db.Canceled, context.DeadlineExceeded},
		{"transport-not-rollback-proof", errors.New("synthetic-private-transport-detail"), db.Unavailable, nil},
		{"server-completion-unknown", &pgconn.PgError{Code: "40003"}, db.Unavailable, nil},
		{"server-connection-failure", &pgconn.PgError{Code: "08006"}, db.Unavailable, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, exhaust := range []bool{false, true} {
				t.Run(fmt.Sprintf("exhaust=%v", exhaust), func(t *testing.T) {
					fixture := &closeErrorRows{failure: test.failure}
					pool := sql.OpenDB(closeErrorConnector{fixture})
					defer pool.Close()
					native, err := pool.QueryContext(context.Background(), "fixture-returning")
					if err != nil {
						t.Fatal(err)
					}
					rows := safeRows{native}
					defer rows.Close()
					var value int64
					if !rows.Next() {
						t.Fatal("fixture did not expose its RETURNING row", rows.Err())
					}
					if err := rows.Scan(&value); err != nil || value != 7 || rows.Err() != nil {
						t.Fatal("fixture failed before result completion", value, err, rows.Err())
					}
					if exhaust {
						if rows.Next() {
							t.Fatal("fixture returned another row")
						}
						err = rows.Err()
					} else {
						err = rows.Close()
					}
					if fixture.closes != 1 || pool.Stats().InUse != 0 {
						t.Fatal("result completion did not release its connection", fixture.closes)
					}
					if test.failure == nil {
						if err != nil || rows.Err() != nil {
							t.Fatal("successful completion changed", err, rows.Err())
						}
					} else {
						for _, observed := range []error{err, rows.Err()} {
							if !db.IsCode(observed, test.code) || strings.Contains(observed.Error(), "synthetic-private") {
								t.Fatal("completion classification or redaction failed", observed)
							}
							if test.cause != nil && !errors.Is(observed, test.cause) {
								t.Fatal("cancellation cause was discarded", observed)
							}
							var provider *pgconn.PgError
							if errors.As(observed, &provider) {
								t.Fatal("raw PostgreSQL error escaped translation")
							}
							if pg, ok := test.failure.(*pgconn.PgError); ok {
								var translated *db.Error
								if !errors.As(observed, &translated) || translated.Constraint != pg.ConstraintName {
									t.Fatal("constraint identity was discarded", observed)
								}
							}
						}
					}
					if err := rows.Close(); err != nil || fixture.closes != 1 {
						t.Fatal("idempotent close changed", err, fixture.closes)
					}
				})
			}
		})
	}
}
