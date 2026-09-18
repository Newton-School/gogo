package redis_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/cache"
	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/sessions"
	redigo "github.com/redis/go-redis/v9"
)

func TestRealRedisURLDatabaseIsolationWithoutApplicationPrefixes(t *testing.T) {
	ctx := context.Background()
	config := fixture.Start(t)
	open := func(database int) *connector.Connection {
		t.Helper()
		cfg := config
		cfg.URL += fmt.Sprintf("/%d", database)
		cfg.Role = connector.SessionRole
		conn, err := connector.Open(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if conn.Database() != database {
			t.Fatal("URL database was not selected")
		}
		return conn
	}
	a, b, peer := open(1), open(2), open(1)
	cacheA, cacheB, cachePeer := &connector.Cache{Connection: a}, &connector.Cache{Connection: b}, &connector.Cache{Connection: peer}
	if err := cacheA.Set(ctx, "shared-key", []byte("first"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := cacheB.Get(ctx, "shared-key"); !errors.Is(err, cache.ErrMiss) {
		t.Fatal("cache leaked across databases", err)
	}
	if value, err := cachePeer.Get(ctx, "shared-key"); err != nil || string(value) != "first" {
		t.Fatal("connections to the same database must share data", err)
	}
	if err := cacheB.Set(ctx, "shared-key", []byte("second"), time.Minute); err != nil {
		t.Fatal(err)
	}
	id, err := sessions.NewID()
	if err != nil {
		t.Fatal(err)
	}
	sessionsA, sessionsB := &connector.Sessions{Connection: a}, &connector.Sessions{Connection: b}
	if err := sessionsA.Create(ctx, sessions.Record{ID: id, Version: 1, ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionsB.Load(ctx, id); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("session leaked across databases", err)
	}
	limit := ratelimit.Limit{Rate: 1, Burst: 1, Period: time.Hour}
	for _, check := range []struct {
		conn *connector.Connection
		want bool
	}{{a, true}, {peer, false}, {b, true}} {
		decision, err := (&connector.Limiter{Connection: check.conn}).Allow(ctx, "shared-key", limit, 1)
		if err != nil || decision.Allowed != check.want {
			t.Fatal("rate-limit database isolation failed", err)
		}
	}
	for _, key := range []string{"cache:0:" + connector.Digest("shared-key"), "session:{" + connector.Digest(id) + "}", "limit:{" + connector.Digest("shared-key") + "}"} {
		if exists, err := a.Client().Exists(ctx, key).Result(); err != nil || exists != 1 {
			t.Fatal("missing unprefixed adapter key", key, err)
		}
	}
	if err := cacheA.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cacheA.Get(ctx, "shared-key"); !errors.Is(err, cache.ErrMiss) {
		t.Fatal("cache clear retained its own entry", err)
	}
	if value, err := cacheB.Get(ctx, "shared-key"); err != nil || string(value) != "second" {
		t.Fatal("cache clear touched another database", err)
	}
	if _, err := sessionsA.Load(ctx, id); err != nil {
		t.Fatal("cache clear touched a session", err)
	}
	if decision, err := (&connector.Limiter{Connection: a}).Allow(ctx, "shared-key", limit, 1); err != nil || decision.Allowed {
		t.Fatal("cache clear touched rate-limit state", err)
	}
}

func TestRedisDatabaseConfigurationValidation(t *testing.T) {
	for name, config := range map[string]connector.Config{
		"negative database":         {Database: -1},
		"negative URL database":     {URL: "redis://127.0.0.1:6379/-1"},
		"invalid URL database":      {URL: "redis://127.0.0.1:6379/not-a-db"},
		"conflicting URL database":  {URL: "redis://127.0.0.1:6379/1", Database: 2},
		"cluster database":          {Addresses: []string{"127.0.0.1:6379"}, Cluster: true, Database: 1},
		"implicit cluster database": {Addresses: []string{"127.0.0.1:6379", "127.0.0.1:6380"}, Database: 1},
	} {
		t.Run(name, func(t *testing.T) {
			config.Role = connector.CacheRole
			if _, err := connector.Open(context.Background(), config); !errors.Is(err, connector.ErrInvalid) {
				t.Fatal("invalid database configuration accepted", err)
			}
		})
	}
}

func TestRedisPartitionKeysAndAtomicValidationWithoutPrefixes(t *testing.T) {
	connection := &connector.Connection{}
	id := "record"
	if key := connection.PartitionKey("task", id, "state"); key != fmt.Sprintf("{task-p%02d}:record:state", connector.Partition(id)) {
		t.Fatal(key)
	}
	if key, err := connection.PartitionIndex("workflow", 3, "records-v1"); err != nil || key != "{workflow-p03}:records-v1" {
		t.Fatal(key, err)
	}
	for _, family := range []string{"", "bad:family", "{slot}", strings.Repeat("a", 65)} {
		if _, err := connection.PartitionIndex(family, 0, "records"); !errors.Is(err, connector.ErrInvalid) {
			t.Fatal("invalid key family accepted", err)
		}
	}
	for _, keys := range [][]string{nil, {""}, {"{one}:a", "{two}:b"}} {
		if _, err := connection.Atomic(context.Background(), redigo.NewScript(`return 1`), keys); !errors.Is(err, connector.ErrInvalid) {
			t.Fatal("invalid atomic keys accepted", err)
		}
	}
}
