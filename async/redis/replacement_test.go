package redis_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
	connector "github.com/Newton-School/gogo/connectors/redis"
)

type uncertainIntentAck struct {
	async.IntentStore
	failed bool
}

func (s *uncertainIntentAck) MarkIntentDelivered(ctx context.Context, source, id string, fence uint64, owner string) error {
	if !s.failed {
		s.failed = true
		return async.ErrUnavailable
	}
	return s.IntentStore.MarkIntentDelivered(ctx, source, id, fence, owner)
}

func TestRealRedisReplacementRecoversGraphAcceptanceAndOriginalResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	broker, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	registry := async.NewRegistry()
	var originals, children atomic.Int64
	child, _ := async.Register(registry, "replace.redis_child", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { children.Add(1); return n + 1, nil }, async.TaskOptions{})
	original, _ := async.Register(registry, "replace.redis_original", 1, func(_ context.Context, tc async.TaskContext, n int) (int, error) {
		originals.Add(1)
		first, _ := child.Signature(n)
		next, _ := child.Signature(0)
		return 0, tc.Replace(async.Chain(first, next))
	}, async.TaskOptions{})
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, Client: client, ID: "replace-worker"}
	result, err := original.Delay(ctx, client, 40)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := broker.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "replace-worker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	waiting, err := result.Snapshot(ctx)
	if err != nil || waiting.State != async.Running || waiting.ReplacementID == "" || waiting.Owner != "" || !waiting.LeaseUntil.IsZero() {
		t.Fatal(waiting, err)
	}
	if err := worker.Process(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if originals.Load() != 1 {
		t.Fatal("original handler repeated")
	}
	uncertain := &uncertainIntentAck{IntentStore: results}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{uncertain, workflows}, ID: "replace-relay", Lease: 10 * time.Millisecond}
	if err := relay.Tick(ctx); !errors.Is(err, async.ErrUnavailable) {
		t.Fatal("missing uncertain graph acceptance", err)
	}
	graph, err := workflows.ReadGraph(ctx, waiting.ReplacementID)
	if err != nil || graph.OriginTaskID != result.Receipt.ID {
		t.Fatal(graph, err)
	}
	// Let the unacknowledged source lease expire; the graph is already durable.
	time.Sleep(15 * time.Millisecond)
	for range 20 {
		if err := relay.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if err := worker.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	value, err := result.Get(ctx)
	if err != nil || value != 42 || children.Load() != 2 || originals.Load() != 1 {
		t.Fatal(value, children.Load(), originals.Load(), err)
	}
	graph, err = workflows.ReadGraph(ctx, waiting.ReplacementID)
	if err != nil || graph.State != async.Succeeded {
		t.Fatal(graph, err)
	}
	for _, child := range graph.Children {
		record, err := results.Lookup(ctx, child.ID)
		if err != nil || record.Pinned {
			t.Fatal(record, err)
		}
	}
	if applied, err := results.ResolveReplacement(ctx, result.Receipt.ID, waiting.ReplacementID, func(async.Record) (async.Transition, error) {
		t.Fatal("terminal replacement builder repeated")
		return async.Transition{}, nil
	}); err != nil || applied {
		t.Fatal(applied, err)
	}
}

func TestRealRedisReplacementRevocationCancelsRunningChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	broker, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	registry := async.NewRegistry()
	started := make(chan struct{})
	child, _ := async.Register(registry, "replace.redis_running", 1, func(ctx context.Context, _ async.TaskContext, _ int) (int, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	}, async.TaskOptions{})
	original, _ := async.Register(registry, "replace.redis_cancel", 1, func(context.Context, async.TaskContext, int) (int, error) {
		s, _ := child.Signature(0)
		return 0, async.Replace(s.Canvas())
	}, async.TaskOptions{})
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, Client: client, ID: "cancel-worker", Heartbeat: 10 * time.Millisecond, Lease: time.Second}
	result, err := original.Delay(ctx, client, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{results, workflows}, ID: "cancel-relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.RunOnce(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := result.Revoke(ctx); err != nil {
		t.Fatal(err)
	}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for range 5 {
		if err := relay.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	record, err := result.Snapshot(ctx)
	if err != nil || record.State != async.Revoked {
		t.Fatal(record, err)
	}
	graph, err := workflows.ReadGraph(ctx, record.ReplacementID)
	if err != nil || graph.State != async.Revoked {
		t.Fatal(graph, err)
	}
	member, err := results.Lookup(ctx, graph.Children[0].ID)
	if err != nil || member.State != async.Revoked || member.Owner != "" || member.Pinned {
		t.Fatal(member, err)
	}
}

func TestRealRedisReplacementCleanupRetainsWaitingAndFutureReplay(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	results.ResultTTL = time.Millisecond
	workflows := &adapter.Workflows{Connection: results.Connection}
	schedules := &adapter.Schedules{Connection: results.Connection}
	registry := async.NewRegistry()
	child, _ := async.Register(registry, "replace.retained_child", 1, func(context.Context, async.TaskContext, int) (int, error) { t.Error("future child ran"); return 0, nil }, async.TaskOptions{})
	eta := time.Now().UTC().Add(30 * 24 * time.Hour)
	original, _ := async.Register(registry, "replace.retained_original", 1, func(context.Context, async.TaskContext, int) (int, error) {
		s, _ := child.Signature(0)
		return 0, async.Replace(s.Set(async.WithETA(eta)).Canvas())
	}, async.TaskOptions{})
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows, Schedules: schedules})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, Client: client, ID: "retained-worker"}
	result, err := original.Delay(ctx, client, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{results, workflows}, ID: "retained-relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Millisecond)
	_, report, err := results.Cleanup(ctx, adapter.Cursor{Partition: connector.Partition(result.Receipt.ID)}, 100)
	if err != nil || report.Pinned < 1 || report.TombstonesRemoved != 0 {
		t.Fatal(report, err)
	}
	waiting, err := result.Snapshot(ctx)
	if err != nil || waiting.State != async.Running || !waiting.ReplayUntil.Equal(eta) {
		t.Fatal(waiting, err)
	}
	if err := result.Revoke(ctx); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if err := relay.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	finished, err := result.Snapshot(ctx)
	if err != nil || finished.State != async.Revoked || finished.TombstoneUntil.Before(eta.Add(7*24*time.Hour)) {
		t.Fatal(finished, err)
	}
	claim, err := results.Claim(ctx, waiting.Envelope, "replay", time.Minute)
	if err != nil || !claim.Duplicate || claim.Acquired {
		t.Fatal(claim, err)
	}
}
