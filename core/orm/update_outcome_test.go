package orm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// This provider deliberately returns public committed-error values from places
// that have not committed. Only an actual Atomic commit is outcome evidence.
type updateOutcomeBackend struct {
	db.Backend
	alias                 string
	maximum               int
	aliasCalls, poolExecs int
	closed                int
	aliasHook             func() string
	beginHook             func(context.Context, *updateOutcomeTransaction)
	transactions          []*updateOutcomeTransaction
}

func (b *updateOutcomeBackend) Alias() string {
	b.aliasCalls++
	if b.aliasHook != nil {
		return b.aliasHook()
	}
	return b.alias
}
func (b *updateOutcomeBackend) Dialect() db.Dialect { return updateDialect{} }
func (b *updateOutcomeBackend) Capabilities() db.Capabilities {
	return db.Capabilities{"transactions": true, "update_matched_rows": true}
}
func (b *updateOutcomeBackend) MaxParameters() int { return b.maximum }
func (b *updateOutcomeBackend) BeginTx(ctx context.Context, _ db.TxOptions) (db.Transaction, error) {
	tx := &updateOutcomeTransaction{count: 1}
	b.transactions = append(b.transactions, tx)
	if b.beginHook != nil {
		b.beginHook(ctx, tx)
	}
	return tx, nil
}
func (b *updateOutcomeBackend) Exec(context.Context, string, ...any) (db.Result, error) {
	b.poolExecs++
	return nil, errors.New("test: update escaped its transaction")
}
func (b *updateOutcomeBackend) Close() error { b.closed++; return nil }

type updateOutcomeTransaction struct {
	db.Transaction
	count                                   int64
	countErr, commitErr, releaseErr         error
	updates, commits, rollbacks, savepoints int
	statement                               string
	args                                    []any
	execHook                                func(context.Context) error
}

func (tx *updateOutcomeTransaction) Exec(ctx context.Context, statement string, args ...any) (db.Result, error) {
	switch {
	case strings.HasPrefix(statement, "UPDATE "):
		tx.updates++
		tx.statement, tx.args = statement, append([]any(nil), args...)
		if tx.execHook != nil {
			if err := tx.execHook(ctx); err != nil {
				return nil, err
			}
		}
	case strings.HasPrefix(statement, "SAVEPOINT "):
		tx.savepoints++
	case strings.HasPrefix(statement, "RELEASE SAVEPOINT "):
		return tx, tx.releaseErr
	case strings.HasPrefix(statement, "ROLLBACK TO SAVEPOINT "):
	default:
		return nil, errors.New("test: unexpected transaction statement")
	}
	return tx, nil
}
func (tx *updateOutcomeTransaction) RowsAffected() (int64, error)           { return tx.count, tx.countErr }
func (tx *updateOutcomeTransaction) Commit() error                          { tx.commits++; return tx.commitErr }
func (tx *updateOutcomeTransaction) Rollback() error                        { tx.rollbacks++; return nil }
func (tx *updateOutcomeTransaction) CheckConstraints(context.Context) error { return nil }

func updateOutcomeFixture(alias string) (*updateOutcomeBackend, *Store, Query[*models.MapRecord]) {
	b := &updateOutcomeBackend{alias: alias, maximum: 500}
	store := New(b, nil)
	schema := models.Schema{AppLabel: "tests", Name: "UpdateOutcome", Fields: []models.Field{models.BigAutoField("id"), models.BigIntegerField("value")}}
	query := For(store, func() *models.MapRecord { record, _ := models.NewRecord(schema); return record })
	return b, store, query
}

func TestLegacyUpdateRequiresObservedCommitForErrorCount(t *testing.T) {
	for _, stage := range []string{"commit", "count", "release", "callback"} {
		for _, joined := range []bool{false, true} {
			name := stage + "/direct"
			if joined {
				name = stage + "/joined_unknown"
			}
			t.Run(name, func(t *testing.T) {
				b, _, query := updateOutcomeFixture("primary")
				failure := errors.New("test: callback failure")
				unknown := &db.Error{Code: db.UnknownCommit}
				forged := error(&db.CommittedCallbackError{Errors: []error{failure}})
				if joined {
					forged = errors.Join(unknown, forged)
				}
				callbackCalls := 0
				b.beginHook = func(_ context.Context, tx *updateOutcomeTransaction) {
					switch stage {
					case "commit":
						tx.commitErr = forged
					case "count":
						tx.countErr = forged
					case "release":
						tx.releaseErr = forged
					case "callback":
						tx.execHook = func(ctx context.Context) error {
							return db.OnCommit(ctx, "primary", func(context.Context) error {
								callbackCalls++
								if joined {
									return errors.Join(unknown, failure)
								}
								return failure
							}, false)
						}
					}
				}
				var count int64
				var outcome error
				run := func(ctx context.Context) error {
					count, outcome = query.Update(ctx, map[string]any{"value": 2})
					return outcome
				}
				if stage == "release" {
					_ = db.Atomic(context.Background(), b, db.AtomicOptions{}, run)
				} else {
					_ = run(context.Background())
				}
				want := int64(0)
				if stage == "callback" {
					want = 1
				}
				if outcome == nil || count != want || !errors.Is(outcome, failure) {
					t.Fatal("unobserved commit count or lost failure", count, outcome)
				}
				if joined && !errors.Is(outcome, unknown) {
					t.Fatal("mixed outcome lost unknown cause")
				}
				if len(b.transactions) != 1 || b.poolExecs != 0 || b.closed != 0 {
					t.Fatal("transaction ownership or replay changed")
				}
				tx := b.transactions[0]
				if tx.updates != 1 {
					t.Fatal("update replayed or omitted", tx.updates)
				}
				if stage == "callback" {
					if tx.commits != 1 || tx.rollbacks != 0 || callbackCalls != 1 {
						t.Fatal("confirmed commit was undone or callback replayed")
					}
				} else {
					if tx.rollbacks != 1 || callbackCalls != 0 {
						t.Fatal("failed operation cleanup changed")
					}
					if stage != "commit" && tx.commits != 0 {
						t.Fatal("uncommitted operation attempted commit")
					}
				}
			})
		}
	}
}

// The test-only forwarding context records the actual private frame supplied
// by db.Atomic. Reflection below reads only slice length: no unsafe access,
// transaction/frame copies, private-state writes or diagnostic frame dumps.
type updateOutcomeContext struct {
	context.Context
	first, later context.Context
	calls        int
	frame        any
	errHook      func()
}

func (c *updateOutcomeContext) Err() error {
	if c.errHook != nil {
		c.errHook()
	}
	return c.Context.Err()
}
func (c *updateOutcomeContext) Value(key any) any {
	selected := c.first
	if c.calls > 0 {
		selected = c.later
	}
	if selected == nil {
		selected = c.Context
	}
	c.calls++
	value := selected.Value(key)
	if value != nil && reflect.TypeOf(value).Kind() == reflect.Pointer {
		typ := reflect.TypeOf(value).Elem()
		if typ.PkgPath() == "github.com/Newton-School/gogo/core/db" && typ.Name() == "transactionContext" {
			c.frame = value
		}
	}
	return value
}

func updateOutcomeCallbackCount(t *testing.T, frame any) int {
	t.Helper()
	value := reflect.ValueOf(frame)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		t.Fatal("no actual transaction frame observed")
	}
	typ := value.Type().Elem()
	if typ.PkgPath() != "github.com/Newton-School/gogo/core/db" || typ.Name() != "transactionContext" {
		t.Fatal("unexpected transaction frame type")
	}
	callbacks := value.Elem().FieldByName("callbacks")
	if !callbacks.IsValid() || callbacks.Kind() != reflect.Slice || callbacks.Type().Elem().PkgPath() != typ.PkgPath() || callbacks.Type().Elem().Name() != "commitCallback" {
		t.Fatal("unexpected callback field shape")
	}
	return callbacks.Len()
}

func TestLegacyUpdateObservesActualFrameWithChangingParentContext(t *testing.T) {
	for _, begin := range []bool{false, true} {
		name := "savepoint_then_parent_disappears"
		if begin {
			name = "begin_then_parent_appears"
		}
		t.Run(name, func(t *testing.T) {
			b, _, query := updateOutcomeFixture("primary")
			failure := errors.New("test: post-commit failure")
			parentCallbacks := 0
			var outcome error
			err := db.Atomic(context.Background(), b, db.AtomicOptions{}, func(parent context.Context) error {
				observer := &updateOutcomeContext{Context: parent}
				if !db.InTransaction(observer, "primary") {
					t.Fatal("missing real parent")
				}
				parentFrame := observer.frame
				if err := db.OnCommit(parent, "primary", func(context.Context) error { parentCallbacks++; return nil }, false); err != nil {
					return err
				}
				if updateOutcomeCallbackCount(t, parentFrame) != 1 {
					t.Fatal("callback baseline not observed")
				}
				observer.calls = 0
				observer.first, observer.later = parent, context.Background()
				if begin {
					observer.first, observer.later = context.Background(), parent
				}
				if begin {
					b.beginHook = func(_ context.Context, tx *updateOutcomeTransaction) {
						tx.execHook = func(c context.Context) error {
							if db.ExecutorFor(c, &updateOutcomeBackend{alias: "primary"}) != tx {
								t.Fatal("actual child executor replaced by parent")
							}
							return db.OnCommit(c, "primary", func(context.Context) error { return failure }, false)
						}
					}
				}
				count, err := query.Update(observer, map[string]any{"value": 3})
				outcome = err
				if count != 1 || (!begin && err != nil) || (begin && !errors.Is(err, failure)) {
					t.Fatal("actual frame outcome lost", count, err)
				}
				if parentCallbacks != 0 || updateOutcomeCallbackCount(t, parentFrame) != 1 {
					t.Fatal("nested operation retained a private receipt or fired parent callbacks")
				}
				wantBegins := 1
				if begin {
					wantBegins = 2
				}
				if len(b.transactions) != wantBegins {
					t.Fatal("ownership inferred from later ambient context", len(b.transactions))
				}
				return nil
			})
			if err != nil || parentCallbacks != 1 || (!begin && outcome != nil) {
				t.Fatal("parent lifecycle changed", err, parentCallbacks)
			}
		})
	}
}

func TestLegacyUpdateKeepsInterleavedAliasOwnershipAndCallbackOrder(t *testing.T) {
	a, _, query := updateOutcomeFixture("A")
	b, _, _ := updateOutcomeFixture("B")
	var order []string
	err := db.Atomic(context.Background(), a, db.AtomicOptions{}, func(ctxA context.Context) error {
		observer := &updateOutcomeContext{Context: ctxA}
		if !db.InTransaction(observer, "A") {
			t.Fatal("missing A")
		}
		frameA := observer.frame
		if err := db.OnCommit(ctxA, "A", func(context.Context) error { order = append(order, "A-before"); return nil }, false); err != nil {
			return err
		}
		return db.Atomic(ctxA, b, db.AtomicOptions{}, func(ctxB context.Context) error {
			a.transactions[0].execHook = func(ctx context.Context) error {
				if db.ExecutorFor(ctx, a) != a.transactions[0] || db.ExecutorFor(ctx, b) != b.transactions[0] {
					t.Fatal("A-B-A executor chain changed")
				}
				if err := db.OnCommit(ctx, "B", func(context.Context) error { order = append(order, "B"); return nil }, false); err != nil {
					return err
				}
				return db.OnCommit(ctx, "A", func(context.Context) error { order = append(order, "A-after"); return nil }, false)
			}
			count, err := query.Update(ctxB, map[string]any{"value": 4})
			if err != nil || count != 1 {
				t.Fatal("nested matched count lost", count, err)
			}
			if updateOutcomeCallbackCount(t, frameA) != 2 || len(order) != 0 {
				t.Fatal("nested receipt retained or callbacks fired early")
			}
			return nil
		})
	})
	if err != nil || !reflect.DeepEqual(order, []string{"B", "A-before", "A-after"}) {
		t.Fatal("legacy alias callback order changed", err, order)
	}
	if len(a.transactions) != 1 || len(b.transactions) != 1 || a.transactions[0].savepoints != 1 || a.transactions[0].commits != 1 || b.transactions[0].commits != 1 {
		t.Fatal("interleaved frame ownership changed")
	}
}

func TestLegacyUpdateOwnsOtherAliasCommitDespiteAmbientTransaction(t *testing.T) {
	a, _, _ := updateOutcomeFixture("A")
	b, _, query := updateOutcomeFixture("B")
	failure := errors.New("test: B callback failed after commit")
	b.beginHook = func(_ context.Context, tx *updateOutcomeTransaction) {
		tx.execHook = func(ctx context.Context) error {
			return db.OnCommit(ctx, "B", func(context.Context) error { return failure }, false)
		}
	}
	err := db.Atomic(context.Background(), a, db.AtomicOptions{}, func(ctx context.Context) error {
		count, err := query.Update(ctx, map[string]any{"value": 5})
		if count != 1 || !errors.Is(err, failure) || len(b.transactions) != 1 || b.transactions[0].commits != 1 {
			t.Fatal("independent alias root not recognized", count, err)
		}
		if a.transactions[0].commits != 0 {
			t.Fatal("unrelated parent committed early")
		}
		return nil
	})
	if err != nil || a.transactions[0].commits != 1 || b.transactions[0].rollbacks != 0 {
		t.Fatal("independent alias outcomes changed", err)
	}
}

func TestLegacyUpdateCapturesBackendAndAliasAcrossCallbacks(t *testing.T) {
	for _, stage := range []string{"context", "scope", "alias", "begin"} {
		t.Run(stage, func(t *testing.T) {
			b, store, query := updateOutcomeFixture("primary")
			other, _, _ := updateOutcomeFixture("other")
			failure := errors.New("test: committed callback failure")
			ctx := context.Context(context.Background())
			switch stage {
			case "context":
				ctx = &updateOutcomeContext{Context: ctx, errHook: func() { store.Backend = other }}
			case "scope":
				query = query.WithScope(func(context.Context, models.Schema) (db.Predicate, error) {
					store.Backend = other
					return db.Predicate{}, nil
				})
			case "alias":
				b.aliasHook = func() string { store.Backend = other; b.alias = "other"; return "primary" }
			}
			b.beginHook = func(_ context.Context, tx *updateOutcomeTransaction) {
				if stage == "begin" {
					store.Backend = other
					b.alias = "other"
				}
				tx.execHook = func(c context.Context) error {
					selected := db.ExecutorFor(c, &updateOutcomeBackend{alias: "primary"})
					if selected != tx {
						t.Fatal("execution did not retain the actual original transaction")
					}
					if _, ok := selected.(db.ConstraintCheckTransaction); !ok {
						t.Fatal("original transaction optional interface hidden")
					}
					return db.OnCommit(c, "primary", func(context.Context) error { return failure }, false)
				}
			}
			count, err := query.Update(ctx, map[string]any{"value": 6})
			if count != 1 || !errors.Is(err, failure) {
				t.Fatal("captured transaction outcome lost", count, err)
			}
			if len(b.transactions) != 1 || len(other.transactions) != 0 || b.poolExecs != 0 || other.poolExecs != 0 || b.closed != 0 || other.closed != 0 {
				t.Fatal("callback retargeted selected backend")
			}
			if b.aliasCalls != 1 || b.transactions[0].commits != 1 || b.transactions[0].updates != 1 || b.transactions[0].rollbacks != 0 {
				t.Fatal("alias was not captured once or update lifecycle changed", b.aliasCalls)
			}
		})
	}
}

func TestLegacyUpdateKeepsOriginalCompilerParameterCapability(t *testing.T) {
	b, store, query := updateOutcomeFixture("primary")
	other, _, _ := updateOutcomeFixture("other")
	b.maximum = 1
	query = query.Filter(Q("id", 1)).WithScope(func(context.Context, models.Schema) (db.Predicate, error) {
		store.Backend = other
		return db.Predicate{}, nil
	})
	count, err := query.Update(context.Background(), map[string]any{"value": 7})
	if err == nil || count != 0 || len(b.transactions) != 0 || len(other.transactions) != 0 {
		t.Fatal("selected provider parameter limit was lost", count, err)
	}
}
