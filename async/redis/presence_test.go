package redis_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
)

func redisPresence(t *testing.T, id string) (async.WorkerLease, async.WorkerSnapshot) {
	t.Helper()
	token, _ := async.NewID()
	idTask, _ := async.NewID()
	at := time.Date(2026, 1, 2, 3, 4, 5, 987654321, time.UTC)
	return async.WorkerLease{WorkerID: id, Token: token}, async.WorkerSnapshot{ID: id, Queues: []string{"default"}, Concurrency: 2, Registered: []string{"test.run@1"}, Active: []async.TaskActivity{{ID: idTask, Task: "test.run", Scope: "tenant", StartedAt: at, Retries: 9007199254740993}}, At: at}
}

func TestRealRedisWorkerPresencePrecisionExpiryAndInstanceTakeover(t *testing.T) {
	ctx := context.Background()
	_, results := backends(t)
	store := &adapter.Workers{Connection: results.Connection}
	lease, snapshot := redisPresence(t, "worker")
	if err := store.ClaimWorker(ctx, lease, snapshot, time.Minute); err != nil {
		t.Fatal(err)
	}
	p, err := store.LookupWorker(ctx, "worker")
	if err != nil || p.Status != async.PresenceOnline || p.Snapshot.Active[0].Retries != 9007199254740993 || !p.Snapshot.Active[0].StartedAt.Equal(snapshot.Active[0].StartedAt) || p.ObservedAt.Equal(snapshot.At) || p.ExpiresAt.Sub(p.ObservedAt) != time.Minute {
		t.Fatal(p, err)
	}
	key := results.Connection.PartitionKey("worker", "worker", "presence")
	raw, err := results.Connection.Client().HGet(ctx, key, "snapshot").Result()
	if err != nil || !strings.Contains(raw, "9007199254740993") || strings.Contains(raw, lease.Token) {
		t.Fatal("opaque precision or token boundary", err)
	}
	if owner := results.Connection.Client().HGet(ctx, key, "owner").Val(); owner == lease.Token || len(owner) != 64 {
		t.Fatal("plaintext ownership token persisted")
	}
	other, _ := redisPresence(t, "worker")
	if err := store.ClaimWorker(ctx, other, snapshot, time.Minute); !errors.Is(err, async.ErrConflict) {
		t.Fatal(err)
	}
	// Expire only this namespace-owned fixture's observational lease, not its
	// retained snapshot, to avoid a timing-dependent test or real clock drift.
	if err := results.Connection.Client().HSet(ctx, key, "expires", p.ObservedAt.UnixMilli()).Err(); err != nil {
		t.Fatal(err)
	}
	p, err = store.LookupWorker(ctx, "worker")
	if err != nil || p.Status != async.PresenceLost || len(p.Snapshot.Active) != 1 {
		t.Fatal(p, err)
	}
	if err := store.ClaimWorker(ctx, other, snapshot, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewWorker(ctx, lease, snapshot, time.Minute); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal(err)
	}
	if err := store.ReleaseWorker(ctx, lease, snapshot); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal(err)
	}
	if err := store.ReleaseWorker(ctx, other, snapshot); err != nil {
		t.Fatal(err)
	}
	p, err = store.LookupWorker(ctx, "worker")
	if err != nil || p.Status != async.PresenceOffline || len(p.Snapshot.Active) != 1 {
		t.Fatal("offline falsely implied task stopped", p, err)
	}
	if ttl := results.Connection.Client().PTTL(ctx, key).Val(); ttl < 23*time.Hour || ttl > 24*time.Hour {
		t.Fatal("retention unbounded", ttl)
	}
	if _, err := store.LookupWorker(ctx, "missing"); !errors.Is(err, async.ErrNotFound) {
		t.Fatal(err)
	}
	if err := store.RenewWorker(ctx, other, snapshot, time.Minute); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal("released instance resurrected", err)
	}
}

func TestRealRedisWorkerPresenceConcurrentClaimsAndCorruptionFailClosed(t *testing.T) {
	ctx := context.Background()
	_, results := backends(t)
	store := &adapter.Workers{Connection: results.Connection}
	first, snapshot := redisPresence(t, "concurrent")
	second, _ := redisPresence(t, "concurrent")
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for _, lease := range []async.WorkerLease{first, second} {
		wg.Go(func() {
			err := store.ClaimWorker(ctx, lease, snapshot, time.Minute)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, async.ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal(accepted.Load())
	}
	key := results.Connection.PartitionKey("worker", "concurrent", "presence")
	if err := results.Connection.Client().HSet(ctx, key, "snapshot", `{"ID":"other"}`).Err(); err != nil {
		t.Fatal(err)
	}
	if p, err := store.LookupWorker(ctx, "concurrent"); !errors.Is(err, async.ErrUnavailable) || p.Snapshot.ID != "" {
		t.Fatal(p, err)
	}
}

func TestRealRedisWorkerPresenceRunsWithDurableTaskAndRemoteInspection(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	store := &adapter.Workers{Connection: results.Connection}
	registry := async.NewRegistry()
	task, err := async.Register(registry, "test.run", 1, func(context.Context, async.TaskContext, int) (int, error) { return 42, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Presence: store, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := task.Delay(ctx, client, 1)
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{ID: "live", Registry: registry, Broker: broker, Results: results, Presence: store, Concurrency: 1}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := (async.Control{Client: client}).InspectWorkers(ctx, []string{"live", "missing"})
	if err != nil || len(p) != 2 || p[0].Status != async.PresenceOffline || len(p[0].Snapshot.Active) != 0 || p[1].Status != async.PresenceUnknown {
		t.Fatal(p, err)
	}
	if value, err := result.Get(ctx); err != nil || value != 42 {
		t.Fatal(value, err)
	}
}
