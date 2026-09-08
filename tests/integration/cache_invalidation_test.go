package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/cache"
	"github.com/Newton-School/gogo/core/db"
)

type invalidationRedis struct {
	cache.Store
	delete func(context.Context, string) error
}

func (s *invalidationRedis) Delete(ctx context.Context, key string) error { return s.delete(ctx, key) }

func TestCacheInvalidationPostgresCommitRedis(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	if _, err := backend.Exec(ctx, "CREATE TABLE invalidation_business (id integer PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	config := fixture.Start(t)
	config.Role = connector.CacheRole
	connection, err := connector.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	store := &connector.Cache{Connection: connection, Version: 1}
	for index, mode := range []string{"rollback", "nested rollback", "commit", "lost delete reply"} {
		t.Run(mode, func(t *testing.T) {
			first, second, unrelated := cache.Key("catalog", 1, mode, "a"), cache.Key("catalog", 1, mode, "b"), cache.Key("private", 1, mode)
			for _, key := range []string{first, second, unrelated} {
				if err := store.Set(ctx, key, []byte("cached"), time.Minute); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			provider := &invalidationRedis{Store: store, delete: func(ctx context.Context, key string) error {
				calls++
				// An independent autocommit query observes the business row before
				// Redis receives any deletion: callback registration is not execution.
				var count int
				if err := db.QueryRow(ctx, backend, "SELECT count(*) FROM invalidation_business WHERE id=$1", []any{index}, &count); err != nil || count != 1 {
					t.Fatal("invalidation preceded visible commit", count, err)
				}
				if err := store.Delete(ctx, key); err != nil {
					return err
				}
				if mode == "lost delete reply" {
					return errors.New("reply lost after Redis deletion")
				}
				return nil
			}}
			plan, err := cache.NewInvalidation(cache.InvalidationConfig{Store: provider, Namespace: "catalog", Keys: []string{first, second}})
			if err != nil {
				t.Fatal(err)
			}
			abort := errors.New("abort business write")
			err = db.Atomic(ctx, backend, db.AtomicOptions{}, func(txctx context.Context) error {
				if _, err := db.ExecutorFor(txctx, backend).Exec(txctx, "INSERT INTO invalidation_business VALUES ($1)", index); err != nil {
					return err
				}
				if mode == "nested rollback" {
					err := db.Atomic(txctx, backend, db.AtomicOptions{}, func(nested context.Context) error {
						if err := plan.OnCommit(nested, backend.Alias(), false); err != nil {
							return err
						}
						return abort
					})
					if err != abort {
						return err
					}
				} else if err := plan.OnCommit(txctx, backend.Alias(), false); err != nil {
					return err
				}
				if calls != 0 {
					t.Fatal("deferred callback ran before commit")
				}
				if mode == "rollback" {
					return abort
				}
				return nil
			})
			if mode == "rollback" {
				if err != abort || calls != 0 {
					t.Fatal(err, calls)
				}
			} else if mode == "lost delete reply" {
				var committed *db.CommittedCallbackError
				var partial *cache.InvalidationError
				if !errors.As(err, &committed) || !errors.As(err, &partial) || partial.Report != (cache.InvalidationReport{Total: 2, Attempted: 1}) || calls != 1 {
					t.Fatal(err, calls)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow(ctx, backend, "SELECT count(*) FROM invalidation_business WHERE id=$1", []any{index}, &count); err != nil || (count == 0) != (mode == "rollback") {
				t.Fatal(count, err)
			}
			for _, key := range []string{first, second, unrelated} {
				value, err := store.Get(ctx, key)
				wantMiss := mode == "commit" && key != unrelated || mode == "lost delete reply" && key == first
				if wantMiss {
					if err != cache.ErrMiss {
						t.Fatal("expected exact deletion", err)
					}
				} else if err != nil || string(value) != "cached" {
					t.Fatal("unattempted or unrelated entry changed", err)
				}
			}
		})
	}
}
