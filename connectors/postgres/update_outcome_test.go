package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// These serial fixtures execute real PostgreSQL statements and commits in the
// marked, disposable openTest schema. Only the returned diagnostic is injected:
// a synthetic lost reply is not a claim to have simulated a network failure.
type nativeUpdateOutcomeBackend struct {
	db.Backend
	transactions          []*nativeUpdateOutcomeTransaction
	callbackErr, countErr error
	commitReport          error
	callbackCalls         int
	poolWrites            int
}

func (*nativeUpdateOutcomeBackend) Alias() string { return "legacy_update" }
func (b *nativeUpdateOutcomeBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil {
		return tx, err
	}
	owned := &nativeUpdateOutcomeTransaction{Transaction: tx, backend: b}
	b.transactions = append(b.transactions, owned)
	return owned, nil
}
func (b *nativeUpdateOutcomeBackend) Exec(ctx context.Context, statement string, args ...any) (db.Result, error) {
	b.poolWrites++
	return b.Backend.Exec(ctx, statement, args...)
}

type nativeUpdateOutcomeTransaction struct {
	db.Transaction
	backend                                         *nativeUpdateOutcomeBackend
	updates, commits, rollbacks, rollbackSavepoints int
	committed                                       bool
	cleanupErr                                      error
}

func (tx *nativeUpdateOutcomeTransaction) Exec(ctx context.Context, statement string, args ...any) (db.Result, error) {
	update := strings.HasPrefix(statement, "UPDATE ")
	if update {
		tx.updates++
	}
	if strings.HasPrefix(statement, "ROLLBACK TO SAVEPOINT ") {
		tx.rollbackSavepoints++
	}
	result, err := tx.Transaction.Exec(ctx, statement, args...)
	if err != nil || !update {
		return result, err
	}
	if failure := tx.backend.callbackErr; failure != nil {
		if err := db.OnCommit(ctx, "legacy_update", func(context.Context) error {
			tx.backend.callbackCalls++
			return failure
		}, false); err != nil {
			return nil, err
		}
	}
	return nativeUpdateOutcomeResult{Result: result, failure: tx.backend.countErr}, nil
}
func (tx *nativeUpdateOutcomeTransaction) Commit() error {
	tx.commits++
	if err := tx.Transaction.Commit(); err != nil {
		return err
	}
	tx.committed = true
	return tx.backend.commitReport
}
func (tx *nativeUpdateOutcomeTransaction) Rollback() error {
	tx.rollbacks++
	tx.cleanupErr = tx.Transaction.Rollback()
	return tx.cleanupErr
}

type nativeUpdateOutcomeResult struct {
	db.Result
	failure error
}

func (r nativeUpdateOutcomeResult) RowsAffected() (int64, error) {
	count, err := r.Result.RowsAffected()
	if err != nil {
		return count, err
	}
	return count, r.failure
}

func nativeUpdateOutcomeFixture(t *testing.T) (*nativeUpdateOutcomeBackend, orm.Query[*models.MapRecord], orm.Query[*models.MapRecord]) {
	t.Helper()
	actual := openTest(t)
	schema := models.Schema{AppLabel: "tests", Name: "LegacyUpdateOutcome", Fields: []models.Field{models.BigAutoField("id"), models.BigIntegerField("value")}}
	if err := actual.SchemaEditor().CreateModel(context.Background(), actual, schema); err != nil {
		t.Fatal(err)
	}
	row := saveMap(t, orm.New(actual, nil), schema, map[string]any{"value": int64(10)})
	provider := &nativeUpdateOutcomeBackend{Backend: actual}
	query := func(backend db.Backend) orm.Query[*models.MapRecord] {
		return orm.For(orm.New(backend, nil), func() *models.MapRecord { result, _ := models.NewRecord(schema); return result }).Filter(orm.Q("id", mustValue(t, row, "id")))
	}
	// The independent reader uses the real pool, not the diagnostic wrapper or
	// any context retained from an already-finished transaction.
	return provider, query(provider), query(actual)
}

func TestLegacyUpdateNativeConfirmedAndUnknownCommitCounts(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "confirmed_commit_callback_failure"
		if unknown {
			name = "actual_commit_lost_reply_joined_forgery"
		}
		t.Run(name, func(t *testing.T) {
			b, query, fresh := nativeUpdateOutcomeFixture(t)
			failure := errors.New("test: after-commit failure")
			unknownCause := &db.Error{Code: db.UnknownCommit}
			b.callbackErr = failure
			if unknown {
				b.commitReport = errors.Join(unknownCause, &db.CommittedCallbackError{Errors: []error{failure}})
			}
			count, err := query.Update(context.Background(), map[string]any{"value": orm.Add(orm.F("value"), orm.Value(1))})
			wantCount, wantCallbacks, wantRollbacks := int64(1), 1, 0
			if unknown {
				wantCount, wantCallbacks, wantRollbacks = 0, 0, 1
			}
			if count != wantCount || !errors.Is(err, failure) || (unknown && !errors.Is(err, unknownCause)) {
				t.Fatal("wrong observed outcome or missing cause", count, err)
			}
			if len(b.transactions) != 1 || b.poolWrites != 0 || b.callbackCalls != wantCallbacks {
				t.Fatal("statement replay, pool escape or premature callback")
			}
			tx := b.transactions[0]
			if !tx.committed || tx.commits != 1 || tx.updates != 1 || tx.rollbacks != wantRollbacks || tx.cleanupErr != nil {
				t.Fatal("real transaction or cleanup outcome changed", tx.commits, tx.updates, tx.rollbacks, tx.cleanupErr)
			}
			stored, readErr := fresh.Get(context.Background())
			if readErr != nil || mustValue(t, stored, "value") != int64(11) {
				t.Fatal("actual committed increment missing or replayed", readErr)
			}
		})
	}
}

func TestLegacyUpdateNativeCountFailureRollsBackOnlyItsSavepoint(t *testing.T) {
	b, query, fresh := nativeUpdateOutcomeFixture(t)
	failure := errors.New("test: forged row-count diagnostic")
	b.countErr = errors.Join(&db.Error{Code: db.UnknownCommit}, &db.CommittedCallbackError{Errors: []error{failure}})
	parentCallbacks := 0
	err := db.Atomic(context.Background(), b, db.AtomicOptions{}, func(ctx context.Context) error {
		if err := db.OnCommit(ctx, "legacy_update", func(context.Context) error { parentCallbacks++; return nil }, false); err != nil {
			return err
		}
		count, err := query.Update(ctx, map[string]any{"value": orm.Add(orm.F("value"), orm.Value(100))})
		if count != 0 || !errors.Is(err, failure) || parentCallbacks != 0 {
			t.Fatal("failed savepoint claimed a committed count", count, err)
		}
		current, err := query.Get(ctx)
		if err != nil || mustValue(t, current, "value") != int64(10) {
			t.Fatal("failed increment survived savepoint rollback", err)
		}
		b.countErr = nil
		count, err = query.Update(ctx, map[string]any{"value": orm.Add(orm.F("value"), orm.Value(2))})
		if err != nil || count != 1 || parentCallbacks != 0 {
			t.Fatal("savepoint cleanup poisoned parent or committed it early", count, err)
		}
		return nil
	})
	if err != nil || parentCallbacks != 1 || len(b.transactions) != 1 || b.poolWrites != 0 {
		t.Fatal("parent completion changed", err, parentCallbacks)
	}
	tx := b.transactions[0]
	if tx.commits != 1 || !tx.committed || tx.rollbacks != 0 || tx.rollbackSavepoints != 1 || tx.updates != 2 {
		t.Fatal("savepoint/parent ownership or replay changed")
	}
	stored, err := fresh.Get(context.Background())
	if err != nil || mustValue(t, stored, "value") != int64(12) {
		t.Fatal("committed parent has wrong exact increments", err)
	}
}

func TestLegacyUpdateNativeSuccessfulNestedCountRemainsPending(t *testing.T) {
	b, query, fresh := nativeUpdateOutcomeFixture(t)
	stop := errors.New("test: parent deliberately rolls back")
	err := db.Atomic(context.Background(), b, db.AtomicOptions{}, func(ctx context.Context) error {
		count, err := query.Update(ctx, map[string]any{"value": orm.Add(orm.F("value"), orm.Value(3))})
		if err != nil || count != 1 {
			t.Fatal("successful savepoint lost provisional matched count", count, err)
		}
		pending, err := query.Get(ctx)
		if err != nil || mustValue(t, pending, "value") != int64(13) {
			t.Fatal("pending update escaped actual transaction", err)
		}
		return stop
	})
	if !errors.Is(err, stop) || len(b.transactions) != 1 || b.poolWrites != 0 {
		t.Fatal("parent rollback outcome changed", err)
	}
	tx := b.transactions[0]
	if tx.committed || tx.commits != 0 || tx.rollbacks != 1 || tx.cleanupErr != nil || tx.updates != 1 {
		t.Fatal("nested update committed or cleanup failed", tx.commits, tx.rollbacks, tx.cleanupErr)
	}
	stored, err := fresh.Get(context.Background())
	if err != nil || mustValue(t, stored, "value") != int64(10) {
		t.Fatal("parent rollback did not restore initial value", err)
	}
}
