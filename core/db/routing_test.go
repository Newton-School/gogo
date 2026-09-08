package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

type routingTestDialect struct{}

func (routingTestDialect) Name() string                             { return "routing-test" }
func (routingTestDialect) Placeholder(i int) string                 { return fmt.Sprintf("$%d", i) }
func (routingTestDialect) QuoteIdentifier(s string) (string, error) { return `"` + s + `"`, nil }
func (routingTestDialect) FieldType(models.Field) (string, error)   { return "text", nil }

type routingTestBackend struct {
	Backend
	alias                                             string
	aliases, metadata, begins, poolQueries, poolExecs int
	txs                                               []*routingTestTransaction
	aliasHook                                         func()
	queryErr, commitErr                               error
}
type routingNonComparableBackend struct {
	*routingTestBackend
	data []int
}

func (b *routingTestBackend) Alias() string {
	b.aliases++
	if b.aliasHook != nil {
		b.aliasHook()
	}
	return b.alias
}
func (b *routingTestBackend) Dialect() Dialect { b.metadata++; return routingTestDialect{} }
func (b *routingTestBackend) Capabilities() Capabilities {
	b.metadata++
	return Capabilities{"transactions": true}
}
func (b *routingTestBackend) BeginTx(context.Context, TxOptions) (Transaction, error) {
	b.begins++
	tx := &routingTestTransaction{commitErr: b.commitErr}
	b.txs = append(b.txs, tx)
	return tx, nil
}
func (b *routingTestBackend) Query(context.Context, string, ...any) (Rows, error) {
	b.poolQueries++
	return &routingTestRows{}, b.queryErr
}
func (b *routingTestBackend) Exec(context.Context, string, ...any) (Result, error) {
	b.poolExecs++
	return txFixtureResult{}, b.queryErr
}

type routingTestRows struct{}

func (*routingTestRows) Columns() ([]string, error) { return nil, nil }
func (*routingTestRows) Next() bool                 { return false }
func (*routingTestRows) Scan(...any) error          { return nil }
func (*routingTestRows) Err() error                 { return nil }
func (*routingTestRows) Close() error               { return nil }

type routingTestTransaction struct {
	queries, execs, commits, rollbacks int
	statements                         []string
	commitErr                          error
}

func (t *routingTestTransaction) Exec(_ context.Context, s string, _ ...any) (Result, error) {
	t.execs++
	t.statements = append(t.statements, s)
	return txFixtureResult{}, nil
}
func (t *routingTestTransaction) Query(context.Context, string, ...any) (Rows, error) {
	t.queries++
	return &routingTestRows{}, nil
}
func (t *routingTestTransaction) Commit() error   { t.commits++; return t.commitErr }
func (t *routingTestTransaction) Rollback() error { t.rollbacks++; return nil }

type routingTestRule struct {
	selectFn func(context.Context, RouteRequest) (string, error)
	allowFn  func(context.Context, RouteRequest, string) error
}

func (r routingTestRule) Select(c context.Context, q RouteRequest) (string, error) {
	if r.selectFn != nil {
		return r.selectFn(c, q)
	}
	return "", nil
}
func (r routingTestRule) Allow(c context.Context, q RouteRequest, a string) error {
	if r.allowFn != nil {
		return r.allowFn(c, q, a)
	}
	return nil
}
func routingTestRouter(t *testing.T, rules ...RoutingRule) (*Router, *routingTestBackend, *routingTestBackend) {
	t.Helper()
	p, r := &routingTestBackend{alias: "primary"}, &routingTestBackend{alias: "replica"}
	router, e := NewRouter(RouterConfig{Default: "primary", EnableReplicas: true, Databases: []RoutedDatabase{{Alias: "primary", Backend: p}, {Alias: "replica", Primary: "primary", Backend: r}}, Rules: rules})
	if e != nil {
		t.Fatal(e)
	}
	return router, p, r
}
func routingRead(alias string) RouteRequest {
	return RouteRequest{Model: "tests.Item", Alias: alias, Operation: RouteRead}
}
func routingWrite() RouteRequest { return RouteRequest{Model: "tests.Item", Operation: RouteWrite} }

func TestRoutingConfigurationAndSafeDiagnostics(t *testing.T) {
	p := &routingTestBackend{alias: "primary"}
	databases := []RoutedDatabase{{Alias: "primary", Backend: p}}
	p.aliasHook = func() { databases[0].Backend = nil }
	router, e := NewRouter(RouterConfig{Default: "primary", Databases: databases})
	if e != nil || router == nil || p.metadata != 0 || p.begins != 0 {
		t.Fatal(e, p)
	}
	invalid := RouteOperation(strings.Repeat("private-operation", 1000))
	_, e = router.Resolve(context.Background(), RouteRequest{Operation: invalid})
	raw, _ := json.Marshal(e)
	if !errors.Is(e, ErrRouting) || strings.Contains(string(raw), "private-operation") {
		t.Fatal(e, string(raw))
	}
	for _, s := range []string{fmt.Sprintf("%v", e), fmt.Sprintf("%#v", e), fmt.Sprintf("%+v", e)} {
		if s != ErrRouting.Error() {
			t.Fatal(s)
		}
	}
	var zero *DatabaseRoutingError
	if zero.Error() != ErrRouting.Error() {
		t.Fatal("nil unsafe")
	}
	var typed *routingTestBackend
	for _, c := range []RouterConfig{
		{}, {Default: "primary", Databases: []RoutedDatabase{{Alias: "primary", Backend: typed}}},
		{Default: "primary", Databases: []RoutedDatabase{{Alias: "wrong", Backend: p}}},
		{Default: "primary", Databases: []RoutedDatabase{{Alias: "primary", Primary: "absent", Backend: p}}},
		{Default: "primary", Databases: []RoutedDatabase{{Alias: "primary", Backend: p}}, Rules: []RoutingRule{nil}},
		{Default: "primary", Databases: []RoutedDatabase{{Alias: "primary", Backend: routingNonComparableBackend{routingTestBackend: p}}}},
	} {
		if _, e := NewRouter(c); !errors.Is(e, ErrRouting) {
			t.Fatal("invalid config", e)
		}
	}
	facade := router.RoutingBackend()
	if CheckBoundBackend(facade) == nil || facade.Dialect() == nil {
		t.Fatal("unbound facade")
	}
	if _, e := facade.Query(context.Background(), "private SQL"); !errors.Is(e, ErrRouting) {
		t.Fatal(e)
	}
	if e := facade.Close(); !errors.Is(e, ErrRouting) {
		t.Fatal(e)
	}
}

func TestRoutingFamilyAffinityAndCrossAliasTransactionRefusal(t *testing.T) {
	p := &routingTestBackend{alias: "primary"}
	other := &routingTestBackend{alias: "other"}
	chosen := "primary"
	router, e := NewRouter(RouterConfig{Default: "primary", Databases: []RoutedDatabase{{Alias: "primary", Backend: p}, {Alias: "other", Backend: other}}, Rules: []RoutingRule{routingTestRule{selectFn: func(context.Context, RouteRequest) (string, error) { return chosen, nil }}}})
	if e != nil {
		t.Fatal(e)
	}
	ctx, _ := router.WithConsistency(context.Background())
	if _, e = router.Resolve(ctx, routingRead("")); e != nil {
		t.Fatal(e)
	}
	chosen = "other"
	if _, e = router.Resolve(ctx, routingRead("")); !errors.Is(e, ErrRouting) || other.metadata != 0 {
		t.Fatal("model family moved", e, other)
	}
	if e = router.Atomic(ctx, "primary", AtomicOptions{}, func(c context.Context, _ *RouteBinding) error {
		if e := router.Atomic(c, "other", AtomicOptions{}, func(context.Context, *RouteBinding) error { t.Fatal("cross-alias callback"); return nil }); !errors.Is(e, ErrRouting) || other.begins != 0 {
			t.Fatal(e, other)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

type routingBadContext struct {
	context.Context
	err        error
	panicValue bool
}

func (c routingBadContext) Err() error {
	if c.panicValue {
		panic("private context failure")
	}
	return c.err
}
func TestRoutingContextErrorsAreSafeAndZeroBindingContextIsNil(t *testing.T) {
	router, p, _ := routingTestRouter(t)
	for _, ctx := range []context.Context{nil, routingBadContext{Context: context.Background(), err: errors.New("private")}, routingBadContext{Context: context.Background(), panicValue: true}} {
		if _, e := router.Resolve(ctx, routingRead("primary")); !errors.Is(e, ErrRouting) || p.metadata != 0 {
			t.Fatal(e, p)
		}
	}
	var binding *RouteBinding
	if binding.Context() != nil || (&RouteBinding{}).Context() != nil {
		t.Fatal("invalid context")
	}
}
func TestRoutingSelectionAdmissionAndMonotonicPins(t *testing.T) {
	selects, allows := 0, 0
	router, p, r := routingTestRouter(t, routingTestRule{selectFn: func(context.Context, RouteRequest) (string, error) { selects++; return "replica", nil }, allowFn: func(_ context.Context, q RouteRequest, a string) error {
		allows++
		if a == "replica" && q.PrimaryRequired {
			t.Fatal("primary constraint missed")
		}
		return nil
	}})
	ctx, e := router.WithConsistency(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	first, e := router.Resolve(ctx, routingRead(""))
	if e != nil || first.Alias() != "replica" {
		t.Fatal(e)
	}
	if _, e = first.Backend().Query(ctx, "read"); e != nil {
		t.Fatal(e)
	}
	explicit, e := router.Resolve(ctx, routingRead("replica"))
	if e != nil || explicit.Alias() != "replica" || selects != 1 || allows != 2 {
		t.Fatal(e, selects, allows)
	}
	write, e := router.Resolve(ctx, routingWrite())
	if e != nil || write.Alias() != "primary" {
		t.Fatal(e)
	}
	if e = write.BeforeWrite(ctx); e != nil {
		t.Fatal(e)
	}
	p.queryErr = &Error{Code: UnknownCommit}
	if _, e = write.Backend().Exec(ctx, "write"); e == nil {
		t.Fatal("missing injected failure")
	}
	later, e := router.Resolve(ctx, routingRead(""))
	if e != nil || later.Alias() != "primary" {
		t.Fatal(e)
	}
	if _, e = router.Resolve(ctx, routingRead("replica")); !errors.Is(e, ErrRouting) {
		t.Fatal("explicit replica bypass", e)
	}
	if _, e = first.Backend().Query(ctx, "stale admitted binding reused"); !errors.Is(e, ErrRouting) {
		t.Fatal("late pin ignored", e)
	}
	if r.poolQueries != 1 || p.poolExecs != 1 {
		t.Fatal("fallback/retry", p, r)
	}
	fresh, _ := router.WithConsistency(context.Background())
	unrelated, e := router.Resolve(fresh, routingRead(""))
	if e != nil || unrelated.Alias() != "replica" {
		t.Fatal("pin leaked requests", e)
	}
	noState, e := router.Resolve(context.Background(), routingRead(""))
	if e != nil || noState.Alias() != "primary" {
		t.Fatal("replica without consistency", e)
	}
}
func TestRoutingDenialBeforeMetadataAndNoRuleErrorExposure(t *testing.T) {
	secret := errors.New("connection-secret")
	router, p, r := routingTestRouter(t, routingTestRule{allowFn: func(context.Context, RouteRequest, string) error { return secret }})
	_, e := router.Resolve(context.Background(), routingRead("primary"))
	if !errors.Is(e, ErrRouting) || errors.Is(e, secret) || p.metadata != 0 || r.metadata != 0 || p.begins != 0 {
		t.Fatal(e, p, r)
	}
}

func TestRoutingInvalidRuleAliasRefusesBeforeMetadataWithoutFallback(t *testing.T) {
	for _, alias := range []string{"bad.alias", strings.Repeat("a", 129)} {
		router, p, r := routingTestRouter(t, routingTestRule{selectFn: func(context.Context, RouteRequest) (string, error) { return alias, nil }})
		if _, e := router.Resolve(context.Background(), routingRead("")); !errors.Is(e, ErrRouting) || p.metadata != 0 || r.metadata != 0 {
			t.Fatal("invalid rule alias", e, p, r)
		}
	}
}
func TestRoutingWitnessedTransactionsAndLegacySameAliasRefusal(t *testing.T) {
	router, p, _ := routingTestRouter(t)
	ctx, _ := router.WithConsistency(context.Background())
	var retained context.Context
	e := router.Atomic(ctx, "primary", AtomicOptions{}, func(txctx context.Context, _ *RouteBinding) error {
		retained = txctx
		read, e := router.Resolve(txctx, routingRead(""))
		if e != nil {
			return e
		}
		if ExecutorFor(txctx, read.Backend()) != read.Backend() {
			t.Fatal("executor bypass")
		}
		rows, e := read.Backend().Query(txctx, "read")
		if e != nil {
			return e
		}
		rows.Close()
		return router.Atomic(txctx, "primary", AtomicOptions{}, func(nested context.Context, _ *RouteBinding) error {
			w, e := router.Resolve(nested, routingWrite())
			if e != nil {
				return e
			}
			_, e = ExecutorFor(nested, w.Backend()).Exec(nested, "write")
			return e
		})
	})
	if e != nil || p.poolQueries != 0 || p.poolExecs != 0 || p.begins != 1 || p.txs[0].queries != 1 || p.txs[0].execs != 3 || p.txs[0].commits != 1 {
		t.Fatal(e, p, p.txs)
	}
	if _, e := router.Resolve(retained, routingRead("")); !errors.Is(e, ErrRouting) {
		t.Fatal("expired witness accepted", e)
	}
	foreign := &routingTestBackend{alias: "primary"}
	e = Atomic(ctx, foreign, AtomicOptions{}, func(foreignCtx context.Context) error {
		before := p.metadata
		if _, e := router.Resolve(foreignCtx, routingRead("primary")); !errors.Is(e, ErrRouting) || p.metadata != before {
			t.Fatal("foreign same alias used", e)
		}
		return nil
	})
	if e != nil || foreign.txs[0].queries != 0 || foreign.txs[0].execs != 0 {
		t.Fatal(e)
	}
}

type routingSwitchContext struct {
	context.Context
	next     context.Context
	calls    int
	switchAt int
}

func (c *routingSwitchContext) Value(key any) any {
	if _, ok := key.(transactionKey); ok {
		c.calls++
		if c.calls >= c.switchAt {
			return c.next.Value(key)
		}
	}
	return c.Context.Value(key)
}
func TestRoutingFreezesAmbientBeforeAtomicAndExecutorHandoffs(t *testing.T) {
	router, p, _ := routingTestRouter(t)
	base, _ := router.WithConsistency(context.Background())
	foreignTx := &routingTestTransaction{}
	foreignCtx := context.WithValue(base, transactionKey{}, &transactionContext{alias: "primary", tx: foreignTx})
	binding, e := router.Resolve(base, RouteRequest{Operation: RouteTransaction, Alias: "primary"})
	if e != nil {
		t.Fatal(e)
	}
	changing := &routingSwitchContext{Context: base, next: foreignCtx, switchAt: 2}
	e = binding.Atomic(changing, AtomicOptions{}, func(ctx context.Context) error {
		read, e := router.Resolve(ctx, routingRead(""))
		if e != nil {
			return e
		}
		later := &routingSwitchContext{Context: ctx, next: foreignCtx, switchAt: 2}
		rows, e := ExecutorFor(later, read.Backend()).Query(later, "owned")
		if rows != nil {
			rows.Close()
		}
		return e
	})
	if e != nil || p.begins != 1 || p.txs[0].queries != 1 || foreignTx.queries != 0 || foreignTx.execs != 0 {
		t.Fatal("handoff retargeted", e, p, foreignTx)
	}
}
func TestRoutingRollbackUnknownAndCallbackOutcomes(t *testing.T) {
	for _, mode := range []string{"rollback", "unknown", "callback", "panic"} {
		t.Run(mode, func(t *testing.T) {
			router, p, _ := routingTestRouter(t)
			ctx, _ := router.WithConsistency(context.Background())
			failure := errors.New("operation failure")
			if mode == "unknown" {
				p.commitErr = &Error{Code: UnknownCommit}
			}
			var e error
			func() {
				defer func() {
					v := recover()
					if mode == "panic" && v != failure {
						t.Fatal("panic changed", v)
					}
					if mode != "panic" && v != nil {
						t.Fatal(v)
					}
				}()
				e = router.Atomic(ctx, "primary", AtomicOptions{}, func(txctx context.Context, _ *RouteBinding) error {
					switch mode {
					case "rollback":
						return failure
					case "panic":
						panic(failure)
					case "callback":
						return OnCommit(txctx, "primary", func(context.Context) error { return failure }, false)
					}
					return nil
				})
			}()
			if mode != "panic" && e == nil {
				t.Fatal("outcome concealed")
			}
			if mode == "callback" {
				var committed *CommittedCallbackError
				if !errors.As(e, &committed) || p.txs[0].rollbacks != 0 {
					t.Fatal(e)
				}
			}
			if _, e := router.Resolve(ctx, routingRead("replica")); !errors.Is(e, ErrRouting) {
				t.Fatal("pin cleared", e)
			}
		})
	}
}
func TestRoutingConsistencyConcurrentBookkeeping(t *testing.T) {
	router, _, _ := routingTestRouter(t)
	ctx, _ := router.WithConsistency(context.Background())
	// Resolve serially: configured fake backend metadata is intentionally not a
	// concurrent provider. Only the request-owned pin/family bookkeeping races.
	binding, e := router.Resolve(ctx, routingWrite())
	if e != nil {
		t.Fatal(e)
	}
	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			if e := binding.BeforeWrite(ctx); e != nil {
				t.Error(e)
			}
		}()
	}
	group.Wait()
	if _, e := router.Resolve(ctx, routingRead("replica")); !errors.Is(e, ErrRouting) {
		t.Fatal(e)
	}
}
