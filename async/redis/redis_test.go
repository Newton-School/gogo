package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
)

func backends(t *testing.T) (*adapter.Broker, *adapter.Results) {
	t.Helper()
	cfg := fixture.Start(t)
	cfg.Role = connector.TaskRole
	conn, err := connector.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	cfg.Role = connector.ResultRole
	results, err := connector.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = results.Close() })
	return &adapter.Broker{Connection: conn}, &adapter.Results{Connection: results}
}
func taskEnvelope(t *testing.T) async.Envelope {
	t.Helper()
	id, err := async.NewID()
	if err != nil {
		t.Fatal(err)
	}
	n := 3
	return async.Envelope{ProtocolVersion: 1, ID: id, Task: "test.run", Version: 1, Args: json.RawMessage(`1`), CreatedAt: time.Now().UTC(), Queue: "default", MaxRetries: &n}
}

func TestRealRedisWorkerRoundTripAndPublishDedupe(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	r := async.NewRegistry()
	calls := 0
	task, err := async.Register(r, "test.run", 1, func(ctx context.Context, tc async.TaskContext, arg int) (int, error) { calls++; return arg + 1, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := async.NewClient(async.ClientConfig{Registry: r, Broker: broker, Results: results})
	if err != nil {
		t.Fatal(err)
	}
	result, err := task.Delay(ctx, c, 1)
	if err != nil {
		t.Fatal(err)
	}
	d, err := broker.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "one"})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := async.DecodeEnvelope(d.Body)
	if err := broker.Publish(ctx, e); err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{ID: "worker", Registry: r, Broker: broker, Results: results}
	if err := worker.Process(ctx, d); err != nil {
		t.Fatal(err)
	}
	out, err := result.Get(ctx)
	if err != nil || out != 2 {
		t.Fatal(out, err)
	}
	if err := worker.Process(ctx, d); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	stats, err := broker.Inspect(ctx, []string{"default"})
	if err != nil || stats[0].Queued != 0 || stats[0].Pending != 0 {
		t.Fatal(stats, err)
	}
}
func TestRealRedisFencesAndConcurrentClaims(t *testing.T) {
	ctx := context.Background()
	_, results := backends(t)
	e := taskEnvelope(t)
	if err := results.Register(ctx, e, async.Queued); err != nil {
		t.Fatal(err)
	}
	var winners atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := results.Claim(ctx, e, "worker", time.Minute)
			if err != nil {
				t.Error(err)
			}
			if claim.Acquired {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal(winners.Load())
	}
	record, err := results.Lookup(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := results.Transition(ctx, async.Transition{ID: e.ID, Fence: record.Fence + 1, Owner: "worker", State: async.Succeeded, Output: json.RawMessage(`1`)}); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal(err)
	}
	if err := results.Transition(ctx, async.Transition{ID: e.ID, Fence: record.Fence, Owner: "worker", State: async.Succeeded, Output: json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := results.Renew(ctx, e.ID, record.Fence, "worker", time.Minute); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal(err)
	}
}
func TestRealRedisRetryIntentDiscoverability(t *testing.T) {
	ctx := context.Background()
	_, results := backends(t)
	e := taskEnvelope(t)
	claim, err := results.Claim(ctx, e, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	next := e
	next.Retries = 1
	next.ETA = time.Now().Add(time.Minute)
	intent := async.Intent{ID: async.StableID(e.ID, "retry1"), SourceID: e.ID, Kind: "publish", Envelope: &next}
	if err := results.Transition(ctx, async.Transition{ID: e.ID, Fence: claim.Record.Fence, Owner: "worker", State: async.RetryWait, Next: &next, Intents: []async.Intent{intent}}); err != nil {
		t.Fatal(err)
	}
	pending, err := results.ListIntents(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	i, err := results.ClaimIntent(ctx, e.ID, intent.ID, "relay", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := results.MarkIntentDelivered(ctx, e.ID, intent.ID, i.Fence, "wrong"); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal(err)
	}
	if err := results.MarkIntentDelivered(ctx, e.ID, intent.ID, i.Fence, "relay"); err != nil {
		t.Fatal(err)
	}
	pending, err = results.ListIntents(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
}
