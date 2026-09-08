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

type routedORMBackend struct {
	db.Backend
	alias                                                                              string
	metadata, begins, poolQueries, poolWrites, txQueries, txWrites, commits, rollbacks int
	statements                                                                         []string
	arguments                                                                          [][]any
	queryErr, commitErr                                                                error
	countErr, releaseErr                                                               error
	queryHook                                                                          func(context.Context)
	execHook                                                                           func(context.Context) error
}

func (b *routedORMBackend) Alias() string       { return b.alias }
func (b *routedORMBackend) Dialect() db.Dialect { b.metadata++; return updateDialect{} }
func (b *routedORMBackend) Capabilities() db.Capabilities {
	b.metadata++
	return db.Capabilities{"transactions": true, "update_matched_rows": true, "row_locks": true, "returning": true}
}
func (b *routedORMBackend) BeginTx(context.Context, db.TxOptions) (db.Transaction, error) {
	b.begins++
	return &routedORMTransaction{backend: b}, nil
}
func (b *routedORMBackend) Exec(_ context.Context, s string, args ...any) (db.Result, error) {
	b.poolWrites++
	b.record(s, args)
	return routedORMResult{}, b.queryErr
}
func (b *routedORMBackend) Query(ctx context.Context, s string, args ...any) (db.Rows, error) {
	b.poolQueries++
	return b.query(ctx, s, args)
}
func (b *routedORMBackend) record(s string, args []any) {
	b.statements = append(b.statements, s)
	b.arguments = append(b.arguments, append([]any(nil), args...))
}
func (b *routedORMBackend) query(ctx context.Context, s string, args []any) (db.Rows, error) {
	b.record(s, args)
	if b.queryHook != nil {
		b.queryHook(ctx)
	}
	values := []any{int64(1), int64(10), nil}
	if strings.Contains(s, "COUNT(") || strings.Contains(s, "SUM(") {
		values = []any{int64(1)}
	}
	return &routedORMRows{values: values}, b.queryErr
}

type routedORMTransaction struct{ backend *routedORMBackend }

func (tx *routedORMTransaction) Exec(ctx context.Context, s string, args ...any) (db.Result, error) {
	tx.backend.txWrites++
	tx.backend.record(s, args)
	if strings.HasPrefix(s, "RELEASE SAVEPOINT") && tx.backend.releaseErr != nil {
		return nil, tx.backend.releaseErr
	}
	if tx.backend.execHook != nil {
		if e := tx.backend.execHook(ctx); e != nil {
			return nil, e
		}
	}
	return routedORMResult{err: tx.backend.countErr}, tx.backend.queryErr
}
func (tx *routedORMTransaction) Query(c context.Context, s string, args ...any) (db.Rows, error) {
	tx.backend.txQueries++
	return tx.backend.query(c, s, args)
}
func (tx *routedORMTransaction) Commit() error   { tx.backend.commits++; return tx.backend.commitErr }
func (tx *routedORMTransaction) Rollback() error { tx.backend.rollbacks++; return nil }

type routedORMResult struct{ err error }

func (r routedORMResult) RowsAffected() (int64, error) { return 1, r.err }

type routedORMRows struct {
	values []any
	read   bool
}

func (r *routedORMRows) Columns() ([]string, error) { return nil, nil }
func (r *routedORMRows) Next() bool {
	if r.read {
		return false
	}
	r.read = true
	return true
}
func (r *routedORMRows) Scan(dest ...any) error {
	for i, d := range dest {
		if i >= len(r.values) {
			return errors.New("fixture column count")
		}
		v := reflect.ValueOf(d).Elem()
		if r.values[i] == nil {
			v.SetZero()
		} else {
			x := reflect.ValueOf(r.values[i])
			if x.Type().AssignableTo(v.Type()) {
				v.Set(x)
			} else if x.Type().ConvertibleTo(v.Type()) {
				v.Set(x.Convert(v.Type()))
			} else {
				return errors.New("fixture destination")
			}
		}
	}
	return nil
}
func (*routedORMRows) Err() error   { return nil }
func (*routedORMRows) Close() error { return nil }

type routedORMRule struct {
	selectFn func(context.Context, db.RouteRequest) (string, error)
	allowFn  func(context.Context, db.RouteRequest, string) error
}

func (r routedORMRule) Select(c context.Context, q db.RouteRequest) (string, error) {
	if r.selectFn != nil {
		return r.selectFn(c, q)
	}
	return "replica", nil
}
func (r routedORMRule) Allow(c context.Context, q db.RouteRequest, a string) error {
	if r.allowFn != nil {
		return r.allowFn(c, q, a)
	}
	return nil
}
func routedORMSchema() models.Schema {
	return models.Schema{AppLabel: "tests", Name: "RoutedItem", Fields: []models.Field{models.BigIntegerField("id", models.Primary), models.BigIntegerField("value"), models.JSONField("payload", models.Nullable)}}
}
func routedORMFixture(t *testing.T, rule routedORMRule) (*Store, *db.Router, *routedORMBackend, *routedORMBackend, context.Context, Query[*models.MapRecord]) {
	t.Helper()
	p, r := &routedORMBackend{alias: "primary"}, &routedORMBackend{alias: "replica"}
	router, e := db.NewRouter(db.RouterConfig{Default: "primary", EnableReplicas: true, Databases: []db.RoutedDatabase{{Alias: "primary", Backend: p}, {Alias: "replica", Primary: "primary", Backend: r}}, Rules: []db.RoutingRule{rule}})
	if e != nil {
		t.Fatal(e)
	}
	store, e := NewRouted(router, nil)
	if e != nil {
		t.Fatal(e)
	}
	ctx, e := router.WithConsistency(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	q := For(store, func() *models.MapRecord { row, _ := models.NewRecord(routedORMSchema()); return row })
	return store, router, p, r, ctx, q
}
func TestRoutedReadTerminalsSelectOnceAndUseSelectedExecutor(t *testing.T) {
	for _, mode := range []string{"all", "get", "first", "last", "exists", "count", "values", "aggregate"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			_, _, p, r, ctx, q := routedORMFixture(t, routedORMRule{allowFn: func(context.Context, db.RouteRequest, string) error { calls++; return nil }})
			scopeCalls := 0
			q = q.WithScope(func(_ context.Context, s models.Schema) (db.Predicate, error) {
				scopeCalls++
				s.Fields[0].Column = "retargeted"
				return Q("id", 1), nil
			})
			var e error
			switch mode {
			case "all":
				var rows []*models.MapRecord
				rows, e = q.All(ctx)
				if e == nil && (len(rows) != 1 || rows[0].State().Database != "replica") {
					t.Fatal("row identity", rows)
				}
			case "get":
				_, e = q.Get(ctx)
			case "first":
				_, e = q.First(ctx)
			case "last":
				_, e = q.Last(ctx)
			case "exists":
				var yes bool
				yes, e = q.Exists(ctx)
				if e == nil && !yes {
					t.Fatal("not found")
				}
			case "count":
				var n int64
				n, e = q.Count(ctx)
				if e == nil && n != 1 {
					t.Fatal(n)
				}
			case "values":
				var values []map[string]any
				values, e = q.Values(ctx, "id")
				if e == nil && (len(values) != 1 || len(values[0]) != 1) {
					t.Fatal(values)
				}
			case "aggregate":
				_, e = q.Aggregate(ctx, map[string]ResultExpression{"total": Typed(Sum(F("value")), models.BigIntegerField("total", models.Nullable))})
			}
			if e != nil || calls != 1 || scopeCalls != 1 || r.poolQueries != 1 || p.poolQueries != 0 {
				t.Fatal(mode, e, calls, scopeCalls, p, r)
			}
			if strings.Contains(r.statements[0], "retargeted") {
				t.Fatal("scope schema alias", r.statements)
			}
		})
	}
}
func TestRoutedReadCopiesStoreBeforeFactoryAndPolicyCallbacks(t *testing.T) {
	var store *Store
	rule := routedORMRule{allowFn: func(context.Context, db.RouteRequest, string) error { store.Backend = nil; return nil }}
	s, _, p, r, ctx, _ := routedORMFixture(t, rule)
	store = s
	factories := 0
	q := For(store, func() *models.MapRecord {
		factories++
		store.Backend = p
		row, _ := models.NewRecord(routedORMSchema())
		return row
	})
	if _, e := q.All(ctx); e != nil || r.poolQueries != 1 || p.poolQueries != 0 || factories != 2 {
		t.Fatal(e, p, r, factories)
	}
}
func TestRoutedInputsFreezeBeforeRoutingRules(t *testing.T) {
	for _, mode := range []string{"update", "aggregate", "values"} {
		t.Run(mode, func(t *testing.T) {
			expression := Add(F("value"), Value(2))
			payload := map[string]any{"x": 1}
			assignments := map[string]any{"value": expression, "payload": payload}
			aggregates := map[string]ResultExpression{"total": Typed(Sum(F("value")), models.BigIntegerField("total", models.Nullable))}
			fields := []string{"id"}
			_, _, p, r, ctx, q := routedORMFixture(t, routedORMRule{allowFn: func(context.Context, db.RouteRequest, string) error {
				expression.Args[1].Value = 99
				payload["x"] = 99
				assignments["value"] = 77
				aggregates["total"] = Typed(Sum(F("id")), models.DateTimeField("total", models.Nullable))
				fields[0] = "payload"
				return nil
			}})
			switch mode {
			case "update":
				n, e := q.Filter(Q("id", 1)).Update(ctx, assignments)
				if e != nil || n != 1 || p.txWrites != 1 || p.commits != 1 {
					t.Fatal(e, n, p)
				}
				if !reflect.DeepEqual(p.arguments[0], []any{`{"x":1}`, 2, 1}) {
					t.Fatal("input mutated", p.arguments)
				}
			case "aggregate":
				_, e := q.Aggregate(ctx, aggregates)
				if e != nil || !strings.Contains(r.statements[0], `SUM("value")`) {
					t.Fatal(e, r.statements)
				}
			case "values":
				values, e := q.Values(ctx, fields...)
				if e != nil || len(values) != 1 || values[0]["id"] != int64(1) {
					t.Fatal(e, values)
				}
			}
		})
	}
}
func TestRoutedSaveFreezesAuthorizedSchemaAndPinsBeforeHooks(t *testing.T) {
	s, router, p, r, ctx, _ := routedORMFixture(t, routedORMRule{})
	row, _ := models.NewRecord(routedORMSchema())
	row.Set("id", int64(1))
	row.Set("value", int64(4))
	row.Set("payload", nil)
	row.State().Database = "replica" // Caller-controlled state is not a placement grant.
	s.BeforeSave = []SaveReceiver{func(c context.Context, e SaveEvent) error {
		if _, failure := router.Resolve(c, db.RouteRequest{Model: routedORMSchema().Key(), Alias: "replica", Operation: db.RouteRead}); !errors.Is(failure, db.ErrRouting) {
			t.Fatal("write pin too late", failure)
		}
		changed := routedORMSchema()
		changed.Name = "OtherTable"
		replacement, _ := models.NewRecord(changed)
		replacement.Set("id", int64(1))
		replacement.Set("value", int64(8))
		replacement.Set("payload", nil)
		*row = *replacement
		s.Backend = r
		return nil
	}}
	if e := s.Save(ctx, row, SaveOptions{ForceInsert: true}); e != nil || p.poolQueries != 1 || r.poolQueries != 0 {
		t.Fatal(e, p, r)
	}
	if !strings.Contains(p.statements[0], `"tests_routeditem"`) || strings.Contains(p.statements[0], "othertable") {
		t.Fatal("authorized model retargeted", p.statements)
	}
	if row.State().Database != "primary" {
		t.Fatal("observed alias", row.State())
	}
}
func TestRoutedAtomicPinsAllTerminalsAndNeverBorrowsLegacyTransaction(t *testing.T) {
	s, _, p, r, ctx, q := routedORMFixture(t, routedORMRule{})
	e := s.Atomic(ctx, db.AtomicOptions{}, func(txctx context.Context) error {
		if _, e := q.Get(txctx); e != nil {
			return e
		}
		if _, e := q.SelectForUpdate(false, false).Values(txctx, "id"); e != nil {
			return e
		}
		row, _ := models.NewRecord(routedORMSchema())
		row.Set("id", int64(1))
		row.Set("value", int64(2))
		row.Set("payload", nil)
		if e := s.Save(txctx, row, SaveOptions{ForceInsert: true}); e != nil {
			return e
		}
		return s.RefreshFromDB(txctx, row, "value")
	})
	if e != nil || p.begins != 1 || p.txQueries != 4 || p.poolQueries != 0 || r.poolQueries != 0 || p.commits != 1 {
		t.Fatal(e, p, r)
	}
	foreign := &routedORMBackend{alias: "primary"}
	e = db.Atomic(ctx, foreign, db.AtomicOptions{}, func(c context.Context) error {
		_, e := q.Get(c)
		if !errors.Is(e, db.ErrRouting) {
			t.Fatal("unmanaged transaction", e)
		}
		return nil
	})
	if e != nil || foreign.txQueries != 0 {
		t.Fatal(e, foreign)
	}
}
func TestRoutedWriteFailurePinsWithoutReplayOrFallback(t *testing.T) {
	for _, mode := range []string{"query", "unknown", "hook"} {
		t.Run(mode, func(t *testing.T) {
			s, router, p, r, ctx, q := routedORMFixture(t, routedORMRule{})
			switch mode {
			case "query":
				p.queryErr = errors.New("write refused")
			case "unknown":
				p.commitErr = &db.Error{Code: db.UnknownCommit}
			case "hook":
				s.BeforeSave = []SaveReceiver{func(context.Context, SaveEvent) error { return errors.New("hook refused") }}
			}
			var e error
			if mode == "hook" {
				row, _ := models.NewRecord(routedORMSchema())
				e = s.Save(ctx, row, SaveOptions{ForceInsert: true})
			} else {
				_, e = q.Update(ctx, map[string]any{"value": 5})
			}
			if e == nil || r.poolWrites != 0 || r.poolQueries != 0 || p.begins > 1 || p.txWrites > 1 {
				t.Fatal(e, p, r)
			}
			if _, e := router.Resolve(ctx, db.RouteRequest{Model: routedORMSchema().Key(), Alias: "replica", Operation: db.RouteRead}); !errors.Is(e, db.ErrRouting) {
				t.Fatal("failed write pin lost", e)
			}
		})
	}
}

type routedORMChangingContext struct {
	context.Context
	active context.Context
}

func (c *routedORMChangingContext) Value(key any) any { return c.active.Value(key) }
func TestRoutedScopeCannotSwitchBetweenGenuineSameAliasWitnesses(t *testing.T) {
	s, _, p, _, base, q := routedORMFixture(t, routedORMRule{})
	queried := false
	e := s.Atomic(base, db.AtomicOptions{}, func(first context.Context) error {
		firstExecutor := db.ExecutorFor(first, p)
		return s.Atomic(base, db.AtomicOptions{}, func(second context.Context) error {
			if firstExecutor == db.ExecutorFor(second, p) {
				t.Fatal("fixture requires distinct actual transactions")
			}
			changing := &routedORMChangingContext{Context: base, active: first}
			p.queryHook = func(c context.Context) {
				queried = true
				if db.ExecutorFor(c, p) != firstExecutor {
					t.Fatal("query moved to second witnessed transaction")
				}
			}
			scoped := q.WithScope(func(c context.Context, _ models.Schema) (db.Predicate, error) {
				changing.active = second
				if db.ExecutorFor(c, p) != firstExecutor {
					t.Fatal("scope lost captured transaction")
				}
				return Q("id", 1), nil
			})
			_, e := scoped.Get(changing)
			return e
		})
	})
	if e != nil || !queried || p.begins != 2 || p.txQueries != 1 || p.poolQueries != 0 || p.commits != 2 {
		t.Fatal(e, queried, p)
	}
}

func TestRoutedUpdateRequiresObservedCommitBeforeRetainingErrorCount(t *testing.T) {
	for _, mode := range []string{"commit_forged", "commit_joined_unknown", "count_forged", "count_joined_unknown", "release_forged", "release_joined_unknown", "real_committed_callback"} {
		t.Run(mode, func(t *testing.T) {
			s, _, p, _, ctx, q := routedORMFixture(t, routedORMRule{})
			failure := errors.New("callback failure")
			forged := error(&db.CommittedCallbackError{Errors: []error{failure}})
			if strings.Contains(mode, "joined") {
				forged = errors.Join(&db.Error{Code: db.UnknownCommit}, forged)
			}
			switch {
			case strings.HasPrefix(mode, "commit_"):
				p.commitErr = forged
			case strings.HasPrefix(mode, "count_"):
				p.countErr = forged
			case strings.HasPrefix(mode, "release_"):
				p.releaseErr = forged
			case mode == "real_committed_callback":
				p.execHook = func(c context.Context) error {
					return db.OnCommit(c, "primary", func(context.Context) error { return failure }, false)
				}
			}
			var count int64
			var result error
			run := func(c context.Context) error { count, result = q.Update(c, map[string]any{"value": 2}); return result }
			if strings.HasPrefix(mode, "release_") {
				_ = s.Atomic(ctx, db.AtomicOptions{}, run)
			} else {
				_ = run(ctx)
			}
			want := int64(0)
			if mode == "real_committed_callback" {
				want = 1
			}
			if result == nil || count != want || !errors.Is(result, failure) {
				t.Fatal("forged or missing commit observation", mode, count, result)
			}
			if mode == "real_committed_callback" && (p.commits != 1 || p.rollbacks != 0) {
				t.Fatal("callback error changed actual commit", p)
			}
			if strings.HasPrefix(mode, "count_") && p.commits != 0 {
				t.Fatal("failed row count committed", p)
			}
			if strings.HasPrefix(mode, "release_") && p.commits != 0 {
				t.Fatal("failed savepoint committed", p)
			}
		})
	}
}
