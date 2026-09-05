package redis_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/cache"
	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/sessions"
	redigo "github.com/redis/go-redis/v9"
)

func TestRealCacheAtomicAddIncrementExpiryIsolation(t *testing.T) {
	ctx := context.Background()
	cfg := fixture.Start(t)
	conn, err := connector.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	store := &connector.Cache{Connection: conn}
	if _, err := store.Get(ctx, "missing"); !errors.Is(err, cache.ErrMiss) {
		t.Fatal(err)
	}
	var winners atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.Add(ctx, "count", []byte("0"), time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal(winners.Load())
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Increment(ctx, "count", 1); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	v, err := store.Get(ctx, "count")
	if err != nil || string(v) != "20" {
		t.Fatal(string(v), err)
	}
	if _, err := store.Increment(ctx, "unknown", 1); !errors.Is(err, cache.ErrMiss) {
		t.Fatal(err)
	}
	other := &connector.Cache{Connection: conn, Version: 2}
	if _, err := other.Get(ctx, "count"); !errors.Is(err, cache.ErrMiss) {
		t.Fatal(err)
	}
	if _, err := conn.Atomic(ctx, redigo.NewScript(`return 1`), []string{"test:{one}:a", "test:{two}:b"}); !errors.Is(err, connector.ErrInvalid) {
		t.Fatal(err)
	}
}
func TestRealSessionCASAndLogoutTombstone(t *testing.T) {
	ctx := context.Background()
	cfg := fixture.Start(t)
	cfg.Role = connector.SessionRole
	conn, err := connector.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	store := &connector.Sessions{Connection: conn}
	id, _ := sessions.NewID()
	record := sessions.Record{ID: id, Version: 1, ExpiresAt: time.Now().Add(time.Minute)}
	if err := store.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, id)
	if err != nil || loaded.Version != 1 {
		t.Fatal(loaded, err)
	}
	record.Version = 2
	if err := store.Save(ctx, record, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, record, 1); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	record.Version = 3
	if err := store.Save(ctx, record, 2); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, id); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal(err)
	}
	record.Version = 1
	if err := store.Create(ctx, record); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal("resurrected", err)
	}
}
func TestRealRateLimitAndDurableReadiness(t *testing.T) {
	ctx := context.Background()
	cfg := fixture.Start(t)
	conn, err := connector.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	limiter := &connector.Limiter{Connection: conn}
	limit := ratelimit.Limit{Rate: 1, Burst: 2, Period: time.Hour}
	for i := 0; i < 3; i++ {
		d, err := limiter.Allow(ctx, "scope", limit, 1)
		if err != nil || d.Allowed != (i < 2) {
			t.Fatal(d, err)
		}
	}
	cfg.Role = connector.TaskRole
	cfg.Development = false
	if _, err := connector.Open(ctx, cfg); !errors.Is(err, connector.ErrDurability) {
		t.Fatal(err)
	}
}
