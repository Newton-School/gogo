package redis_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
)

func TestRealRedisWorkflowRepairConvergesWithoutCompletionDelivery(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	registry := async.NewRegistry()
	calls := 0
	task, err := async.Register(registry, "test.repair", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { calls++; return n + 1, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	one, _ := task.Signature(1)
	two, _ := task.Signature(2)
	group, err := client.ApplyCanvas(ctx, async.Group(one, two))
	if err != nil {
		t.Fatal(err)
	}
	// Intentionally omit result-local completion delivery while preserving
	// the authoritative results and graph. Repair must use actual records.
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{workflows}, ID: "workflow-relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, ID: "worker"}
	for range 2 {
		if err := worker.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	before, err := group.Snapshot(ctx)
	if err != nil || len(before.Members) != 0 {
		t.Fatal("fixture delivered completion", err)
	}
	var wait sync.WaitGroup
	for range 4 {
		wait.Go(func() {
			if _, err := client.ReconcileWorkflow(ctx, group.ID, async.WorkflowRepairOptions{}); err != nil {
				t.Error(err)
			}
		})
	}
	wait.Wait()
	after, err := group.Snapshot(ctx)
	if err != nil || after.State != async.Succeeded || len(after.Members) != 2 || calls != 2 {
		t.Fatal("concurrent barrier repair failed", after.State, calls, err)
	}
	for _, child := range after.Children {
		record, err := results.Lookup(ctx, child.ID)
		if err != nil || !record.Pinned {
			t.Fatal("repair directly released pin", err)
		}
	}
	relay.Sources = []async.IntentStore{results, workflows}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal("late original notification conflicted", err)
	}
	for _, child := range after.Children {
		record, err := results.Lookup(ctx, child.ID)
		if err != nil || record.Pinned {
			t.Fatal("ordinary durable unpin failed", err)
		}
	}
	values, err := group.Join(ctx)
	if err != nil || len(values) != 2 || string(values[0]) != "2" || string(values[1]) != "3" {
		t.Fatal("ordered outcomes changed", values, err)
	}
}

func TestRealRedisWorkflowRepairReportsMissingSourceWithoutReexecution(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	registry := async.NewRegistry()
	calls := 0
	task, err := async.Register(registry, "test.gap", 1, func(context.Context, async.TaskContext, int) (int, error) { calls++; return 42, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	signature, _ := task.Signature(1)
	group, err := client.ApplyCanvas(ctx, async.Group(signature))
	if err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{workflows}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, ID: "worker"}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	graph, err := group.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := graph.Children[0].ID
	// Delete exactly one task record in this test's ephemeral Redis database
	// to model missing replicated state; never delete an arbitrary user's key.
	if err := results.Connection.Client().Del(ctx, results.Connection.PartitionKey("task", id, "state")).Err(); err != nil {
		t.Fatal(err)
	}
	report, err := client.ReconcileWorkflow(ctx, group.ID, async.WorkflowRepairOptions{})
	if !errors.Is(err, async.ErrWorkflowStateGap) || report.Converged != 0 || len(report.Gaps) != 1 || report.Gaps[0].TaskID != id || calls != 1 {
		t.Fatal(report, err)
	}
	after, err := group.Snapshot(ctx)
	if err != nil || after.Revision != graph.Revision || after.State != async.Running || len(after.Members) != 0 {
		t.Fatal("missing state fabricated barrier completion", err)
	}
	stats, err := broker.Inspect(ctx, []string{"default"})
	if err != nil || len(stats) != 1 || stats[0].Queued != 0 {
		t.Fatal("repair republished missing business work", err)
	}
}

func TestRealRedisWorkflowRepairClaimsChordBodyOnceAcrossConcurrentRepairs(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	registry := async.NewRegistry()
	task, err := async.Register(registry, "test.header", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bodyCalls := 0
	body, err := async.Register(registry, "test.body", 1, func(_ context.Context, _ async.TaskContext, values []int) (int, error) {
		bodyCalls++
		return values[0] + values[1], nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	one, _ := task.Signature(1)
	two, _ := task.Signature(2)
	callback, _ := body.Signature(nil)
	canvas, err := async.Chord(async.Group(one, two), callback)
	if err != nil {
		t.Fatal(err)
	}
	group, err := client.ApplyCanvas(ctx, canvas)
	if err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{workflows}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, ID: "worker"}
	for range 2 {
		if err := worker.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	repair := func() {
		var wait sync.WaitGroup
		for range 8 {
			wait.Go(func() {
				_, err := client.ReconcileWorkflow(ctx, group.ID, async.WorkflowRepairOptions{})
				// A repair starting after callback claim can observe its
				// pending publication. It must report that gap, not dispatch.
				if err != nil && !errors.Is(err, async.ErrWorkflowStateGap) {
					t.Error(err)
				}
			})
		}
		wait.Wait()
	}
	repair()
	claimed, err := group.Snapshot(ctx)
	if err != nil || !claimed.CallbackClaimed || claimed.CallbackEnvelope == nil || claimed.State != async.Running || bodyCalls != 0 {
		t.Fatal("callback claim missing or executed during repair", err)
	}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	repair()
	after, err := group.Snapshot(ctx)
	if err != nil || after.State != async.Succeeded || string(after.Output) != "3" || bodyCalls != 1 || after.CallbackID != claimed.CallbackID {
		t.Fatal("concurrent repairs repeated callback", bodyCalls, err)
	}
	relay.Sources = []async.IntentStore{results, workflows}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal("original completions conflicted", err)
	}
	stats, err := broker.Inspect(ctx, []string{"default"})
	if err != nil || len(stats) != 1 || stats[0].Queued != 0 {
		t.Fatal("callback republished after completion", err)
	}
}
