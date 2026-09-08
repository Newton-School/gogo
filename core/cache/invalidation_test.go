package cache_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Newton-School/gogo/core/cache"
	"github.com/Newton-School/gogo/core/db"
)

type invalidationStore struct {
	cache.Store
	delete func(context.Context, string) error
}

func (s *invalidationStore) Delete(ctx context.Context, key string) error { return s.delete(ctx, key) }

func invalidationPlan(t *testing.T, store cache.Store, keys ...string) *cache.Invalidation {
	t.Helper()
	plan, err := cache.NewInvalidation(cache.InvalidationConfig{Store: store, Namespace: "catalog", Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestInvalidationConfigurationAndOwnership(t *testing.T) {
	first, second := cache.Key("catalog", 1, "first"), cache.Key("catalog", 1, "second")
	var calls []string
	store := &invalidationStore{delete: func(_ context.Context, key string) error { calls = append(calls, key); return nil }}
	keys := []string{first, second, first}
	plan := invalidationPlan(t, store, keys...)
	keys[0] = cache.Key("private", 1, "other")
	other := invalidationPlan(t, store, second)
	ctx := &invalidationContext{Context: context.Background(), onErr: func() error { *plan = *other; return nil }}
	report, err := plan.Apply(ctx)
	if err != nil || report != (cache.InvalidationReport{Total: 2, Attempted: 2, Acknowledged: 2}) || !reflect.DeepEqual(calls, []string{first, second}) {
		t.Fatal(report, err, calls)
	}
	var nilStore *invalidationStore
	for name, config := range map[string]cache.InvalidationConfig{
		"nil":                {Namespace: "catalog", Keys: []string{first}},
		"typed nil":          {Store: nilStore, Namespace: "catalog", Keys: []string{first}},
		"empty keys":         {Store: store, Namespace: "catalog"},
		"over count":         {Store: store, Namespace: "catalog", Keys: make([]string, 257)},
		"empty namespace":    {Store: store, Keys: []string{first}},
		"wildcard namespace": {Store: store, Namespace: "catalog*", Keys: []string{first}},
		"other namespace":    {Store: store, Namespace: "catalog", Keys: []string{cache.Key("private", 1)}},
		"raw key":            {Store: store, Namespace: "catalog", Keys: []string{"catalog:secret"}},
		"uppercase digest":   {Store: store, Namespace: "catalog", Keys: []string{"catalog:" + strings.Repeat("A", 64)}},
		"glob":               {Store: store, Namespace: "catalog", Keys: []string{"catalog:*"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := cache.NewInvalidation(config); got != nil || err != cache.ErrInvalidationConfig {
				t.Fatal(got, err)
			}
		})
	}
}

type invalidationContext struct {
	context.Context
	onErr func() error
}

func (c *invalidationContext) Err() error { return c.onErr() }

type invalidationPrivateError struct{ calls *int }

func (e *invalidationPrivateError) Error() string { *e.calls++; panic("private provider details") }

func TestInvalidationPartialUnknownAndCancellation(t *testing.T) {
	keys := []string{cache.Key("catalog", 1, "a"), cache.Key("catalog", 1, "b"), cache.Key("catalog", 1, "c")}
	for _, mode := range []string{"error", "panic", "cancel after first", "late cancel", "wrapped miss"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, formatted := 0, 0
			private := &invalidationPrivateError{calls: &formatted}
			store := &invalidationStore{delete: func(context.Context, string) error {
				calls++
				if mode == "cancel after first" && calls == 1 || mode == "late cancel" && calls == 3 {
					cancel()
				}
				if calls == 2 {
					switch mode {
					case "error":
						return private
					case "panic":
						panic("private provider details")
					case "wrapped miss":
						return fmt.Errorf("failed after write: %w", cache.ErrMiss)
					}
				}
				return nil
			}}
			report, err := invalidationPlan(t, store, keys...).Apply(ctx)
			want := cache.InvalidationReport{Total: 3, Attempted: 2, Acknowledged: 1}
			if mode == "cancel after first" {
				want.Attempted = 1
			}
			if mode == "late cancel" {
				want.Attempted, want.Acknowledged = 3, 3
			}
			if report != want || calls != report.Attempted {
				t.Fatal(report, err, calls)
			}
			if mode == "late cancel" {
				if err != nil {
					t.Fatal("late cancellation erased completed deletion", err)
				}
				return
			}
			var partial *cache.InvalidationError
			if !errors.As(err, &partial) || partial.Report != report {
				t.Fatal(report, err)
			}
			wantErr := cache.ErrInvalidationUnavailable
			if mode == "cancel after first" {
				wantErr = context.Canceled
			}
			if !errors.Is(err, wantErr) {
				t.Fatal(err)
			}
			for _, verb := range []string{"%s", "%v", "%+v", "%#v", "%q"} {
				if message := fmt.Sprintf(verb, err); strings.Contains(message, "private") || strings.Contains(message, "catalog:") || strings.Contains(message, "PANIC") {
					t.Fatal(message)
				}
			}
			if formatted != 0 {
				t.Fatal("formatted provider cause")
			}
			if mode == "error" && !errors.Is(err, private) {
				t.Fatal("lost explicit cause")
			}
		})
	}
}

func TestInvalidationContextsAndConcurrentUse(t *testing.T) {
	zero := &cache.InvalidationError{}
	if !errors.Is(zero, cache.ErrInvalidationUnavailable) || zero.Error() == "" {
		t.Fatal("zero error has an invalid unwrap tree")
	}
	var mu sync.Mutex
	calls := 0
	store := &invalidationStore{delete: func(context.Context, string) error { mu.Lock(); calls++; mu.Unlock(); return nil }}
	plan := invalidationPlan(t, store, cache.Key("catalog", 1))
	var typedNil *invalidationContext
	for _, ctx := range []context.Context{nil, typedNil,
		&invalidationContext{Context: context.Background(), onErr: func() error { panic("private") }},
		&invalidationContext{Context: context.Background(), onErr: func() error { return errors.New("private") }},
	} {
		if report, err := plan.Apply(ctx); err == nil || report.Attempted != 0 || calls != 0 {
			t.Fatal(report, err, calls)
		}
	}
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			if report, err := plan.Apply(context.Background()); err != nil || report.Acknowledged != 1 {
				t.Error(report, err)
			}
		})
	}
	group.Wait()
	if calls != 16 {
		t.Fatal(calls)
	}
}

type invalidationTx struct {
	db.Transaction
	commits, rollbacks int
	commitError        error
}

func (tx *invalidationTx) Commit() error                                        { tx.commits++; return tx.commitError }
func (tx *invalidationTx) Rollback() error                                      { tx.rollbacks++; return nil }
func (*invalidationTx) Exec(context.Context, string, ...any) (db.Result, error) { return nil, nil }

type invalidationBackend struct {
	db.Backend
	alias string
	tx    *invalidationTx
}

func (b *invalidationBackend) Alias() string { return b.alias }
func (b *invalidationBackend) BeginTx(context.Context, db.TxOptions) (db.Transaction, error) {
	return b.tx, nil
}

func TestInvalidationOnCommitBoundaries(t *testing.T) {
	for _, mode := range []string{"commit", "rollback", "savepoint rollback", "unknown commit", "after failure", "robust failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			tx := &invalidationTx{}
			backend := &invalidationBackend{alias: "default", tx: tx}
			if mode == "unknown commit" {
				tx.commitError = errors.New("lost reply")
			}
			calls, later := 0, false
			plan := invalidationPlan(t, &invalidationStore{delete: func(context.Context, string) error {
				calls++
				if tx.commits != 1 {
					t.Fatal("invalidation before actual commit")
				}
				if strings.Contains(mode, "failure") {
					return errors.New("private")
				}
				return nil
			}}, cache.Key("catalog", 1))
			abort := errors.New("abort")
			err := db.Atomic(ctx, backend, db.AtomicOptions{}, func(txctx context.Context) error {
				if mode == "savepoint rollback" {
					if err := db.Atomic(txctx, backend, db.AtomicOptions{}, func(nested context.Context) error {
						if err := plan.OnCommit(nested, "default", false); err != nil {
							return err
						}
						return abort
					}); err != abort {
						return err
					}
				} else if err := plan.OnCommit(txctx, "default", mode == "robust failure"); err != nil {
					return err
				}
				if calls != 0 {
					t.Fatal("registration ran invalidation")
				}
				if err := db.OnCommit(txctx, "default", func(context.Context) error { later = true; return nil }, false); err != nil {
					return err
				}
				if mode == "rollback" {
					return abort
				}
				return nil
			})
			if mode == "rollback" || mode == "unknown commit" {
				if err == nil || calls != 0 || later {
					t.Fatal(err, calls, later)
				}
			} else if mode == "savepoint rollback" {
				if err != nil || calls != 0 || !later {
					t.Fatal(err, calls, later)
				}
			} else if strings.Contains(mode, "failure") {
				var committed *db.CommittedCallbackError
				var partial *cache.InvalidationError
				if !errors.As(err, &committed) || !errors.As(err, &partial) || calls != 1 || tx.rollbacks != 0 || later != (mode == "robust failure") {
					t.Fatal(err, calls, later)
				}
			} else if err != nil || calls != 1 || !later {
				t.Fatal(err, calls, later)
			}
		})
	}
}

func TestInvalidationOnCommitFreezesPlanAndAlias(t *testing.T) {
	first, second := cache.Key("catalog", 1, "first"), cache.Key("catalog", 1, "second")
	var calls []string
	store := &invalidationStore{delete: func(_ context.Context, key string) error { calls = append(calls, key); return nil }}
	plan, replacement := invalidationPlan(t, store, first), invalidationPlan(t, store, second)
	backend := &invalidationBackend{alias: "selected", tx: &invalidationTx{}}
	err := db.Atomic(context.Background(), backend, db.AtomicOptions{}, func(ctx context.Context) error {
		if err := plan.OnCommit(ctx, "selected", false); err != nil {
			return err
		}
		*plan = *replacement
		// Another alias has no ambient transaction: existing OnCommit semantics
		// execute immediately and do not attach to the selected alias.
		return replacement.OnCommit(ctx, "other", false)
	})
	if err != nil || !reflect.DeepEqual(calls, []string{second, first}) {
		t.Fatal(err, calls)
	}
	var nilPlan *cache.Invalidation
	for _, value := range []*cache.Invalidation{nilPlan, {}} {
		if _, err := value.Apply(context.Background()); err != cache.ErrInvalidationConfig {
			t.Fatal(err)
		}
		if err := value.OnCommit(context.Background(), "default", false); err != cache.ErrInvalidationConfig {
			t.Fatal(err)
		}
	}
	if err := plan.OnCommit(context.Background(), "*", false); err != cache.ErrInvalidationConfig {
		t.Fatal(err)
	}
}

func FuzzInvalidationNamespace(f *testing.F) {
	f.Add("catalog", "catalog:"+strings.Repeat("a", 64))
	f.Add("catalog", "private:"+strings.Repeat("b", 64))
	f.Fuzz(func(t *testing.T, namespace, key string) {
		if len(namespace) > 256 || len(key) > 1024 {
			t.Skip()
		}
		calls := 0
		store := &invalidationStore{delete: func(_ context.Context, actual string) error {
			if actual != key || !strings.HasPrefix(actual, namespace+":") {
				t.Fatal("retargeted invalidation")
			}
			calls++
			return nil
		}}
		plan, err := cache.NewInvalidation(cache.InvalidationConfig{Store: store, Namespace: namespace, Keys: []string{key}})
		if err != nil {
			if calls != 0 {
				t.Fatal("constructor effect")
			}
			return
		}
		if report, err := plan.Apply(context.Background()); err != nil || report.Acknowledged != 1 || calls != 1 {
			t.Fatal(report, err, calls)
		}
	})
}
