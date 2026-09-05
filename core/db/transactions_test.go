package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type txFixtureBackend struct {
	Backend
	alias       string
	transaction *txFixture
}

func (b *txFixtureBackend) Alias() string { return b.alias }
func (b *txFixtureBackend) BeginTx(context.Context, TxOptions) (Transaction, error) {
	return b.transaction, nil
}

type txFixture struct {
	commits, rollbacks      int
	failPrefix              string
	failed, cleanupDeadline bool
}

func (t *txFixture) Exec(ctx context.Context, sql string, _ ...any) (Result, error) {
	if strings.HasPrefix(sql, "ROLLBACK TO") {
		_, t.cleanupDeadline = ctx.Deadline()
	}
	if !t.failed && t.failPrefix != "" && strings.HasPrefix(sql, t.failPrefix) {
		t.failed = true
		return nil, errors.New("injected transaction failure")
	}
	return txFixtureResult{}, nil
}
func (t *txFixture) Query(context.Context, string, ...any) (Rows, error) {
	return nil, errors.New("unexpected query")
}
func (t *txFixture) Commit() error   { t.commits++; return nil }
func (t *txFixture) Rollback() error { t.rollbacks++; return nil }

type txFixtureResult struct{}

func (txFixtureResult) RowsAffected() (int64, error) { return 0, nil }

func TestAtomicPreservesInterleavedAliasChain(t *testing.T) {
	a := &txFixtureBackend{alias: "A", transaction: &txFixture{}}
	b := &txFixtureBackend{alias: "B", transaction: &txFixture{}}
	order := []string{}
	err := Atomic(context.Background(), a, AtomicOptions{}, func(ctxA context.Context) error {
		return Atomic(ctxA, b, AtomicOptions{}, func(ctxB context.Context) error {
			return Atomic(ctxB, a, AtomicOptions{}, func(ctxABA context.Context) error {
				if ExecutorFor(ctxABA, a) != a.transaction || ExecutorFor(ctxABA, b) != b.transaction {
					t.Fatal("interleaved alias escaped its transaction")
				}
				if err := OnCommit(ctxABA, b.alias, func(context.Context) error { order = append(order, "B"); return nil }, false); err != nil {
					return err
				}
				return OnCommit(ctxABA, a.alias, func(context.Context) error { order = append(order, "A"); return nil }, false)
			})
		})
	})
	if err != nil || a.transaction.commits != 1 || b.transaction.commits != 1 || !reflect.DeepEqual(order, []string{"B", "A"}) {
		t.Fatal(err, order, a.transaction, b.transaction)
	}
}
func TestFailedNestedCleanupCannotBeSwallowed(t *testing.T) {
	for _, prefix := range []string{"SAVEPOINT ", "RELEASE SAVEPOINT ", "ROLLBACK TO SAVEPOINT "} {
		t.Run(prefix, func(t *testing.T) {
			transaction := &txFixture{failPrefix: prefix}
			backend := &txFixtureBackend{alias: "default", transaction: transaction}
			err := Atomic(context.Background(), backend, AtomicOptions{}, func(ctx context.Context) error {
				nested := Atomic(ctx, backend, AtomicOptions{}, func(context.Context) error {
					if strings.HasPrefix(prefix, "ROLLBACK") {
						return errors.New("work failed")
					}
					return nil
				})
				if nested == nil {
					t.Fatal("injected error disappeared")
				}
				return nil // Caller attempts to swallow the nested error.
			})
			if err == nil || transaction.commits != 0 || transaction.rollbacks != 1 {
				t.Fatal("damaged outer transaction committed", err, transaction)
			}
			if strings.HasPrefix(prefix, "ROLLBACK") && !transaction.cleanupDeadline {
				t.Fatal("cleanup has no deadline")
			}
		})
	}
}
