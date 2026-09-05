package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
)

func TestRealRedisDelayedExpiryDispatchesDeclaredErrback(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	schedules := &adapter.Schedules{Connection: results.Connection}
	registry := async.NewRegistry()
	_, _ = async.Register(registry, "test.run", 1, func(context.Context, async.TaskContext, int) (int, error) {
		t.Error("expired handler ran")
		return 0, nil
	}, async.TaskOptions{})
	called := 0
	errback, _ := async.Register(registry, "links.redis_expiry", 1, func(_ context.Context, _ async.TaskContext, failure async.Failure) (int, error) {
		if failure.Code != "EXPIRED" {
			t.Error(failure.Code)
		}
		called++
		return 42, nil
	}, async.TaskOptions{})
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Schedules: schedules})
	if err != nil {
		t.Fatal(err)
	}
	failure, _ := errback.Signature(async.Failure{})
	e := taskEnvelope(t)
	// Recover a valid delayed wire record whose lifetime elapsed while its
	// dispatcher was offline. No wall-clock sleeps or provider time edits.
	e.ETA = time.Now().Add(-2 * time.Minute)
	e.ExpiresAt = time.Now().Add(-time.Minute)
	e.Errbacks = []async.Signature{failure}
	if err := results.Register(ctx, e, async.Scheduled); err != nil {
		t.Fatal(err)
	}
	if err := schedules.Schedule(ctx, e); err != nil {
		t.Fatal(err)
	}
	dispatcher := &async.DelayedDispatcher{Client: client, ID: "expiry-dispatcher"}
	if err := dispatcher.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{results}, ID: "expiry-relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, ID: "expiry-worker"}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	record, err := results.Lookup(ctx, e.ID)
	if err != nil || record.State != async.Expired || called != 1 {
		t.Fatal(record.State, called, err)
	}
	child, err := results.Lookup(ctx, async.StableID(e.ID, "errback-0"))
	if err != nil || child.State != async.Succeeded || string(child.Output) != "42" {
		t.Fatal(child, err)
	}
}
