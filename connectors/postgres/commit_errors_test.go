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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The injected driver reaches database/sql's Commit stage with a live caller
// context, then simulates the provider losing its commit acknowledgement. It
// does not assert a real network-loss experiment or retry an uncertain write.
type commitErrorConnector struct{ failure error }

func (c commitErrorConnector) Connect(context.Context) (driver.Conn, error) {
	return commitErrorConnection{c.failure}, nil
}
func (c commitErrorConnector) Driver() driver.Driver { return commitErrorDriver{c.failure} }

type commitErrorDriver struct{ failure error }

func (d commitErrorDriver) Open(string) (driver.Conn, error) {
	return commitErrorConnection{d.failure}, nil
}

type commitErrorConnection struct{ failure error }

func (c commitErrorConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fixture does not prepare statements")
}
func (c commitErrorConnection) Close() error { return nil }
func (c commitErrorConnection) Begin() (driver.Tx, error) {
	return commitErrorTransaction{c.failure}, nil
}

type commitErrorTransaction struct{ failure error }

func (t commitErrorTransaction) Commit() error { return t.failure }
func (commitErrorTransaction) Rollback() error { return nil }

func TestCommitClassifiesLostAcknowledgementSeparatelyFromDefiniteServerFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure error
		code    db.ErrorCode
	}{
		{"canceled-after-submit", context.Canceled, db.UnknownCommit},
		{"timeout-after-submit", fmt.Errorf("provider wrapper: %w", context.DeadlineExceeded), db.UnknownCommit},
		{"transport", io.EOF, db.UnknownCommit},
		{"bad-connection", driver.ErrBadConn, db.UnknownCommit},
		{"already-done-not-rollback-proof", sql.ErrTxDone, db.UnknownCommit},
		{"no-rows-not-rollback-proof", sql.ErrNoRows, db.UnknownCommit},
		{"confirmed-rollback-command-tag", pgx.ErrTxCommitRollback, db.Unavailable},
		{"deferred-unique", &pgconn.PgError{Code: "23505", ConstraintName: "test_unique", Message: "sensitive server detail"}, db.UniqueViolation},
		{"serialization", &pgconn.PgError{Code: "40001"}, db.SerializationFailure},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, db.Deadlock},
		{"server-canceled", &pgconn.PgError{Code: "57014"}, db.Canceled},
		{"server-completion-unknown", &pgconn.PgError{Code: "40003"}, db.UnknownCommit},
		{"server-transaction-resolution-unknown", &pgconn.PgError{Code: "08007"}, db.UnknownCommit},
		{"server-connection-failure", &pgconn.PgError{Code: "08006"}, db.UnknownCommit},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := sql.OpenDB(commitErrorConnector{test.failure})
			defer pool.Close()
			tx, err := pool.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			err = (&transaction{tx}).Commit()
			if !db.IsCode(err, test.code) || strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "provider wrapper") {
				t.Fatal("commit classification/redaction", err)
			}
		})
	}
}

func TestNonCommitCancellationKeepsCanceledContract(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		err := translate(fmt.Errorf("provider: %w", cause), false)
		if !db.IsCode(err, db.Canceled) || !errors.Is(err, cause) {
			t.Fatal("ordinary cancellation changed", err)
		}
	}
	if err := translate(nil, true); err != nil {
		t.Fatal(err)
	}
	if err := translate(&pgconn.PgError{Code: "23503", ConstraintName: "test_fk"}, false); !db.IsCode(err, db.ForeignKeyViolation) {
		t.Fatal(err)
	}
}
