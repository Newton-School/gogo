package postgres_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// These are real PostgreSQL operations against distinct owned schemas, with
// deliberately different data. They prove role/alias routing and transaction
// ownership, not physical replication, replica lag or a network-loss scenario.
type nativeRoutingBackend struct {
	db.Backend
	alias                   string
	queries, writes, begins atomic.Int64
	unknownCommit           bool
	afterCommitError        error
}

func (b *nativeRoutingBackend) Alias() string { return b.alias }
func (b *nativeRoutingBackend) Query(c context.Context, s string, args ...any) (db.Rows, error) {
	b.queries.Add(1)
	return b.Backend.Query(c, s, args...)
}
func (b *nativeRoutingBackend) Exec(c context.Context, s string, args ...any) (db.Result, error) {
	b.writes.Add(1)
	return b.Backend.Exec(c, s, args...)
}
func (b *nativeRoutingBackend) BeginTx(c context.Context, o db.TxOptions) (db.Transaction, error) {
	b.begins.Add(1)
	tx, e := b.Backend.BeginTx(c, o)
	if e != nil {
		return tx, e
	}
	return &nativeRoutingTransaction{Transaction: tx, backend: b, unknown: b.unknownCommit}, nil
}

type nativeRoutingTransaction struct {
	db.Transaction
	backend *nativeRoutingBackend
	unknown bool
}

func (t *nativeRoutingTransaction) Query(c context.Context, s string, args ...any) (db.Rows, error) {
	t.backend.queries.Add(1)
	return t.Transaction.Query(c, s, args...)
}
func (t *nativeRoutingTransaction) Exec(c context.Context, s string, args ...any) (db.Result, error) {
	t.backend.writes.Add(1)
	if failure := t.backend.afterCommitError; failure != nil {
		if e := db.OnCommit(c, t.backend.alias, func(context.Context) error { return failure }, false); e != nil {
			return nil, e
		}
	}
	return t.Transaction.Exec(c, s, args...)
}
func (t *nativeRoutingTransaction) Commit() error {
	e := t.Transaction.Commit()
	if e == nil && t.unknown {
		return &db.Error{Code: db.UnknownCommit}
	}
	return e
}

type nativeRoutingRule struct {
	deny     bool
	requests []db.RouteRequest
	aliases  []string
}

func (*nativeRoutingRule) Select(context.Context, db.RouteRequest) (string, error) {
	return "replica", nil
}
func (r *nativeRoutingRule) Allow(_ context.Context, q db.RouteRequest, a string) error {
	r.requests = append(r.requests, q)
	r.aliases = append(r.aliases, a)
	if r.deny {
		return errors.New("private placement denial")
	}
	return nil
}
func nativeRoutingFixture(t *testing.T) (*orm.Store, *db.Router, *nativeRoutingBackend, *nativeRoutingBackend, *nativeRoutingRule, models.Schema) {
	t.Helper()
	primary, replica := openTest(t), openTest(t)
	schema := models.Schema{AppLabel: "tests", Name: "Routed", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.BigIntegerField("value")}}
	for i, b := range []interface {
		SchemaEditor() db.SchemaEditor
		db.Backend
	}{primary, replica} {
		if e := b.SchemaEditor().CreateModel(context.Background(), b, schema); e != nil {
			t.Fatal(e)
		}
		initial := int64(10)
		if i == 1 {
			initial = 5
		}
		saveMap(t, orm.New(b, nil), schema, map[string]any{"tenant": 1, "value": initial})
		saveMap(t, orm.New(b, nil), schema, map[string]any{"tenant": 2, "value": int64(90)})
	}
	p, r := &nativeRoutingBackend{Backend: primary, alias: "primary"}, &nativeRoutingBackend{Backend: replica, alias: "replica"}
	rule := &nativeRoutingRule{}
	router, e := db.NewRouter(db.RouterConfig{Default: "primary", EnableReplicas: true, Databases: []db.RoutedDatabase{{Alias: "primary", Backend: p}, {Alias: "replica", Primary: "primary", Backend: r}}, Rules: []db.RoutingRule{rule}})
	if e != nil {
		t.Fatal(e)
	}
	store, e := orm.NewRouted(router, nil)
	if e != nil {
		t.Fatal(e)
	}
	return store, router, p, r, rule, schema
}
func nativeRoutingQuery(s *orm.Store, schema models.Schema) orm.Query[*models.MapRecord] {
	return orm.For(s, func() *models.MapRecord { row, _ := models.NewRecord(schema); return row }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil })
}
func TestRoutingNativeRoleSelectionScopeAndWriteConsistency(t *testing.T) {
	s, router, p, r, _, schema := nativeRoutingFixture(t)
	ctx, _ := router.WithConsistency(context.Background())
	q := nativeRoutingQuery(s, schema)
	first, e := q.Get(ctx)
	if e != nil || mustValue(t, first, "value") != int64(5) || first.State().Database != "replica" || r.queries.Load() != 1 || p.queries.Load() != 0 {
		t.Fatal("initial replica role", e)
	}
	if n, e := q.Count(ctx); e != nil || n != 1 {
		t.Fatal("scoped count", n, e)
	}
	if values, e := q.Values(ctx, "id", "value"); e != nil || len(values) != 1 || values[0]["value"] != int64(5) {
		t.Fatal("values", values, e)
	}
	if agg, e := q.Aggregate(ctx, map[string]orm.ResultExpression{"total": orm.Typed(orm.Sum(orm.F("value")), models.BigIntegerField("total", models.Nullable))}); e != nil || agg["total"] != int64(5) {
		t.Fatal("aggregate", agg, e)
	}
	row, _ := models.NewRecord(schema)
	row.Set("tenant", 1)
	row.Set("value", int64(11))
	if e = s.Save(ctx, row, orm.SaveOptions{ForceInsert: true}); e != nil {
		t.Fatal(e)
	}
	if n, e := q.Count(ctx); e != nil || n != 2 {
		t.Fatal("primary count after write", n, e)
	}
	if _, e := q.Using("replica").All(ctx); !errors.Is(e, db.ErrRouting) {
		t.Fatal("explicit stale alias", e)
	}
	if e = s.RefreshFromDB(ctx, row, "value"); e != nil || mustValue(t, row, "value") != int64(11) {
		t.Fatal("refresh", e)
	}
	fresh, _ := router.WithConsistency(context.Background())
	again, e := q.Get(fresh)
	if e != nil || mustValue(t, again, "value") != int64(5) {
		t.Fatal("request pin leaked", e)
	}
	if n, e := q.Count(context.Background()); e != nil || n != 2 {
		t.Fatal("unmanaged request should use primary", n, e)
	}
}
func TestRoutingNativeObservedTransactionRollbackAndSameAliasRefusal(t *testing.T) {
	s, router, p, r, _, schema := nativeRoutingFixture(t)
	ctx, _ := router.WithConsistency(context.Background())
	q := nativeRoutingQuery(s, schema)
	stop := errors.New("owned rollback")
	e := s.Atomic(ctx, db.AtomicOptions{Durable: true}, func(txctx context.Context) error {
		locked, e := q.SelectForUpdate(false, false).Get(txctx)
		if e != nil || mustValue(t, locked, "value") != int64(10) {
			t.Fatal("transaction primary read", e)
		}
		n, e := q.Update(txctx, map[string]any{"value": orm.Add(orm.F("value"), orm.Value(4))})
		if e != nil || n != 1 {
			return e
		}
		current, e := q.Get(txctx)
		if e != nil || mustValue(t, current, "value") != int64(14) {
			t.Fatal("read escaped transaction", e)
		}
		if _, e := q.Using("replica").All(txctx); !errors.Is(e, db.ErrRouting) {
			t.Fatal("cross-alias tx", e)
		}
		return stop
	})
	if !errors.Is(e, stop) || p.begins.Load() != 1 || r.queries.Load() != 0 {
		t.Fatal(e, p.begins.Load(), r.queries.Load())
	}
	after, e := q.Get(ctx)
	if e != nil || mustValue(t, after, "value") != int64(10) {
		t.Fatal("rollback absent", e)
	}
	// The unrelated real backend intentionally claims the same alias. Only its
	// own legacy transaction may use it; router catalog authority cannot borrow it.
	foreign := &nativeRoutingBackend{Backend: r.Backend, alias: "primary"}
	e = db.Atomic(ctx, foreign, db.AtomicOptions{}, func(c context.Context) error {
		before := p.queries.Load()
		if _, e := q.Get(c); !errors.Is(e, db.ErrRouting) || p.queries.Load() != before {
			t.Fatal("same alias escaped ownership", e)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestRoutingNativeUnknownCommitNeverReplaysAndKeepsPrimaryPin(t *testing.T) {
	s, router, p, r, _, schema := nativeRoutingFixture(t)
	ctx, _ := router.WithConsistency(context.Background())
	q := nativeRoutingQuery(s, schema)
	p.unknownCommit = true
	n, e := q.Update(ctx, map[string]any{"value": orm.Add(orm.F("value"), orm.Value(1))})
	if e == nil || !db.IsCode(e, db.UnknownCommit) || n != 0 || p.begins.Load() != 1 || p.writes.Load() != 1 || r.writes.Load() != 0 {
		t.Fatal("unknown outcome retried or claimed", n, e, p.begins.Load(), p.writes.Load())
	}
	p.unknownCommit = false
	actual, e := q.Get(ctx)
	if e != nil || mustValue(t, actual, "value") != int64(11) {
		t.Fatal("confirmed actual SQL effect missing", e)
	}
	if _, e := q.Using("replica").Get(ctx); !errors.Is(e, db.ErrRouting) {
		t.Fatal("unknown commit pin cleared", e)
	}
}
func TestRoutingNativeDeniedPlacementTouchesNoSelectedProvider(t *testing.T) {
	s, router, p, r, rule, schema := nativeRoutingFixture(t)
	ctx, _ := router.WithConsistency(context.Background())
	rule.deny = true
	q := nativeRoutingQuery(s, schema)
	if _, e := q.All(ctx); !errors.Is(e, db.ErrRouting) || p.queries.Load() != 0 || r.queries.Load() != 0 || p.begins.Load() != 0 {
		t.Fatal("denied placement effects", e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	rule.deny = false
	if _, e := q.All(canceled); !errors.Is(e, context.Canceled) || p.queries.Load() != 0 || r.queries.Load() != 0 {
		t.Fatal("canceled query", e)
	}
}

func TestRoutingNativeRetainsCountOnlyAfterActualCommitWithCallbackFailure(t *testing.T) {
	s, router, p, r, _, schema := nativeRoutingFixture(t)
	ctx, _ := router.WithConsistency(context.Background())
	q := nativeRoutingQuery(s, schema)
	failure := errors.New("owned after-commit callback failed")
	p.afterCommitError = failure
	count, e := q.Update(ctx, map[string]any{"value": orm.Add(orm.F("value"), orm.Value(1))})
	var committed *db.CommittedCallbackError
	if count != 1 || !errors.As(e, &committed) || !errors.Is(e, failure) || p.begins.Load() != 1 || p.writes.Load() != 1 || r.writes.Load() != 0 {
		t.Fatal("actual commit receipt absent", count, e, p.begins.Load(), p.writes.Load())
	}
	p.afterCommitError = nil
	actual, e := q.Get(ctx)
	if e != nil || mustValue(t, actual, "value") != int64(11) {
		t.Fatal("committed increment lost or repeated", e)
	}
	if _, e := q.Using("replica").Get(ctx); !errors.Is(e, db.ErrRouting) {
		t.Fatal("committed error cleared primary pin", e)
	}
}
