package redis_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
)

func TestRealRedisOversizedWorkflowSettlesWithoutPoisoningRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	broker, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	registry := async.NewRegistry()
	task, _ := async.Register(registry, "limits.redis_member", 1, func(context.Context, async.TaskContext, int) (string, error) {
		return strings.Repeat("x", 240<<10), nil
	}, async.TaskOptions{})
	body, _ := async.Register(registry, "limits.redis_body", 1, func(context.Context, async.TaskContext, []string) (int, error) {
		t.Error("oversized barrier invoked body")
		return 0, nil
	}, async.TaskOptions{})
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, ID: "limits-worker"}
	var branches []async.Canvas
	for range 20 {
		s, _ := task.Signature(0)
		branches = append(branches, s.Canvas())
	}
	callback, _ := body.Signature(nil)
	group, err := client.ApplyCanvas(ctx, async.Parallel(branches...).Then(callback.Canvas()))
	if err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{results, workflows}, ID: "limits-relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if err := worker.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for range 5 {
		if err := relay.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	graph, err := group.Snapshot(ctx)
	if err != nil || graph.State != async.Failed || graph.Failure == nil || graph.Failure.Code != "WORKFLOW_SIZE" {
		t.Fatal(graph.State, graph.Failure, err)
	}
	for index, child := range graph.Children {
		record, err := results.Lookup(ctx, child.ID)
		if err != nil || record.Pinned {
			t.Fatal(index, record, err)
		}
		if index < 20 && record.State != async.Succeeded || index == 20 && record.State != async.Failed {
			t.Fatal(index, record.State)
		}
	}
	for _, store := range []async.IntentStore{results, workflows} {
		intents, err := store.ListIntents(ctx, 100)
		if err != nil || len(intents) != 0 {
			t.Fatal(len(intents), err)
		}
	}
}
