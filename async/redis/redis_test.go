package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/ratelimit"
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

func TestReclaimAdvancesPastLiveReservationsAndBoundsPrefetch(t *testing.T) {
	ctx := context.Background()
	broker, _ := backends(t)
	var last async.Delivery
	for i := 0; i < 25; i++ {
		e := taskEnvelope(t)
		if err := broker.Publish(ctx, e); err != nil {
			t.Fatal(err)
		}
		var err error
		last, err = broker.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "original"})
		if err != nil {
			t.Fatal(err)
		}
	}
	stream, receipt, _ := strings.Cut(last.Receipt, "\x00")
	// Only the final reservation is abandoned. The first twenty-four remain
	// live and require XAUTOCLAIM's returned scan cursor to reach the tail.
	if err := broker.Connection.Client().Do(ctx, "XCLAIM", stream, "gogo-workers", "original", 0, receipt, "IDLE", 120000).Err(); err != nil {
		t.Fatal(err)
	}
	found := false
	for turn := 0; turn < 4; turn++ {
		items, err := broker.Reclaim(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "replacement"}, time.Minute)
		if err != nil || len(items) > 1 {
			t.Fatal(items, err)
		}
		if len(items) == 1 {
			if items[0].Receipt != last.Receipt {
				t.Fatal("reclaimed a live reservation", items)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("abandoned tail reservation starved behind live prefix")
	}
}

func TestInvalidBrokerConfigurationReturnsErrors(t *testing.T) {
	ctx := context.Background()
	broker := &adapter.Broker{}
	if err := broker.Ack(ctx, async.Delivery{}); !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := broker.Reclaim(ctx, async.ConsumeOptions{}, time.Minute); !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := broker.Inspect(ctx, []string{"default"}); !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestDistributedRateBudgetIsSharedAcrossWorkers(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	registry := async.NewRegistry()
	calls := 0
	task, err := async.Register(registry, "test.rate", 1, func(context.Context, async.TaskContext, int) (int, error) { calls++; return 1, nil }, async.TaskOptions{Rate: &async.TaskRate{Scope: "distributed", Limit: ratelimit.Limit{Rate: 1, Burst: 1, Period: time.Hour}}})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results})
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"first", "second"} {
		result, err := task.Delay(ctx, client, i)
		if err != nil {
			t.Fatal(err)
		}
		delivery, err := broker.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: name})
		if err != nil {
			t.Fatal(err)
		}
		worker := &async.Worker{ID: name, Registry: registry, Broker: broker, Results: results, RateLimiter: &connector.Limiter{Connection: broker.Connection}}
		bounded, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		err = worker.Process(bounded, delivery)
		cancel()
		if i == 0 && err != nil || i == 1 && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(i, err)
		}
		if i == 1 {
			record, err := result.Snapshot(ctx)
			if err != nil || record.State != async.Queued || record.DeliveryCount != 0 || record.Envelope.Retries != 0 {
				t.Fatal(record, err)
			}
			if err := result.Revoke(ctx); err != nil {
				t.Fatal(err)
			}
			if err := worker.Process(ctx, delivery); err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls != 1 {
		t.Fatal("workers did not share a rate budget", calls)
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

func TestRealRedisChordAndDelayedLease(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	schedules := &adapter.Schedules{Connection: results.Connection}
	registry := async.NewRegistry()
	member, err := async.Register(registry, "test.member", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n + 1, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	body, err := async.Register(registry, "test.body", 1, func(_ context.Context, _ async.TaskContext, values []int) (int, error) {
		calls++
		return values[0] + values[1], nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows, Schedules: schedules})
	if err != nil {
		t.Fatal(err)
	}
	one, _ := member.Signature(1)
	two, _ := member.Signature(3)
	callback, _ := body.Signature(nil)
	canvas, _ := async.Chord(async.Group(one, two), callback)
	group, err := client.ApplyCanvas(ctx, canvas)
	if err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{results, workflows}, ID: "relay"}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, ID: "worker"}
	for i := 0; i < 4; i++ {
		if err := relay.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		for {
			d, err := broker.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
			if errors.Is(err, async.ErrNotFound) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.Process(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
	}
	out, err := group.Join(ctx)
	if err != nil || len(out) != 1 || string(out[0]) != "6" || calls != 1 {
		t.Fatal(out, calls, err)
	}
	graph, _ := group.Snapshot(ctx)
	for _, e := range graph.Children {
		record, err := results.Lookup(ctx, e.ID)
		if err != nil || record.Pinned {
			t.Fatal(record, err)
		}
	}
	e := taskEnvelope(t)
	e.ETA = time.Now().Add(-time.Minute)
	if err := schedules.Schedule(ctx, e); err != nil {
		t.Fatal(err)
	}
	items, err := schedules.LeaseDue(ctx, "scheduler", 10, time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	wrong := items[0]
	wrong.Owner = "other"
	if err := schedules.CommitFire(ctx, wrong); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal(err)
	}
	if err := schedules.CommitFire(ctx, items[0]); err != nil {
		t.Fatal(err)
	}
	items, err = schedules.LeaseDue(ctx, "scheduler", 10, time.Minute)
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
}

func TestRealPeriodicBeatDurableOccurrence(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	schedules := &adapter.Schedules{Connection: results.Connection}
	registry := async.NewRegistry()
	calls := 0
	task, err := async.Register(registry, "test.periodic", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { calls++; return n, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Schedules: schedules})
	if err != nil {
		t.Fatal(err)
	}
	signature, _ := task.Signature(1)
	id, _ := async.NewID()
	p := async.PeriodicSchedule{ID: id, Signature: signature, Rule: async.Every(time.Hour), Enabled: true, NextDue: time.Now().Add(-time.Minute), Misfire: "coalesce", CatchUpLimit: 1, Overlap: "allow", Revision: 1}
	if err := schedules.UpsertSchedule(ctx, p, 0); err != nil {
		t.Fatal(err)
	}
	beat := &async.Beat{Client: client, Store: schedules, ID: "beat"}
	if err := beat.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	intents, err := schedules.ListIntents(ctx, 10)
	if err != nil || len(intents) != 1 {
		t.Fatal(intents, err)
	}
	// Restarting beat before publication discovers the existing durable intent;
	// the advanced occurrence does not allocate a second logical task ID.
	beat = &async.Beat{Client: client, Store: schedules, ID: "replacement"}
	if err := beat.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	again, _ := schedules.ListIntents(ctx, 10)
	if len(again) != 1 || again[0].ID != intents[0].ID {
		t.Fatal(again)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{schedules}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	delivery, err := broker.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{ID: "worker", Registry: registry, Broker: broker, Results: results}
	if err := worker.Process(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	list, err := schedules.List(ctx, 10)
	if err != nil || len(list) != 1 || !list[0].NextDue.After(time.Now()) {
		t.Fatal(list, err)
	}
	if err := schedules.Disable(ctx, list[0].ID, list[0].Revision); err != nil {
		t.Fatal(err)
	}
}

func TestRealScopedMonitorEventsAndDispatchConflicts(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	events := &adapter.Events{Connection: results.Connection}
	event := async.Event{Kind: "task_started", TaskID: "id", Task: "test.task", Scope: "one", State: async.Running, At: time.Now().UTC()}
	if err := events.PublishEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	visible, cursor, err := events.Read(ctx, "one", "", 10)
	if err != nil || len(visible) != 1 || cursor == "" {
		t.Fatal(visible, cursor, err)
	}
	other, _, err := events.Read(ctx, "two", "", 10)
	if err != nil || len(other) != 0 {
		t.Fatal(other, err)
	}
	e := taskEnvelope(t)
	if err := broker.Publish(ctx, e); err != nil {
		t.Fatal(err)
	}
	e.ETA = time.Now().Add(time.Hour)
	if err := broker.Publish(ctx, e); !errors.Is(err, async.ErrConflict) {
		t.Fatal(err)
	}
}

func TestRealResultCleanupPreservesTerminalTombstone(t *testing.T) {
	ctx := context.Background()
	_, results := backends(t)
	results.ResultTTL = time.Millisecond
	e := taskEnvelope(t)
	claim, err := results.Claim(ctx, e, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := results.Transition(ctx, async.Transition{ID: e.ID, Fence: claim.Record.Fence, Owner: "worker", State: async.Succeeded, Output: json.RawMessage(`42`)}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	cleared := false
	for time.Now().Before(deadline) {
		_, report, err := results.Cleanup(ctx, adapter.Cursor{Partition: connector.Partition(e.ID)}, 100)
		if err != nil {
			t.Fatal(err)
		}
		if report.PayloadsRemoved == 1 {
			cleared = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !cleared {
		t.Fatal("result payload not pruned")
	}
	record, err := results.Lookup(ctx, e.ID)
	if err != nil || record.State != async.Succeeded || len(record.Output) != 0 {
		t.Fatal(record, err)
	}
	duplicate, err := results.Claim(ctx, e, "redelivery", time.Minute)
	if err != nil || !duplicate.Duplicate || duplicate.Acquired {
		t.Fatal(duplicate, err)
	}
}
