package async_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func TestCanvasBudgetFailureWaitsForAllAcceptedChildren(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	registry := async.NewRegistry()
	called := 0
	task, _ := async.Register(registry, "limits.output", 1, func(context.Context, async.TaskContext, int) (string, error) {
		called++
		return strings.Repeat("x", 240<<10), nil
	}, async.TaskOptions{})
	backend := fakes.NewMemory()
	backend.Clock = func() time.Time { return now }
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend, Schedules: backend, Clock: backend.Clock})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, Clock: backend.Clock, ID: "limits-worker"}
	var signatures []async.Signature
	for range 20 {
		s, _ := task.Signature(0)
		signatures = append(signatures, s)
	}
	signatures[len(signatures)-1] = signatures[len(signatures)-1].Set(async.WithCountdown(time.Hour))
	group, err := client.ApplyCanvas(ctx, async.Group(signatures...))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	graph, err := group.Snapshot(ctx)
	if err != nil || graph.State.Terminal() || graph.AbortFailure == nil || graph.AbortFailure.Code != "WORKFLOW_SIZE" || called != 19 {
		t.Fatal(graph.State, graph.AbortFailure, called, err)
	}
	for _, child := range graph.Children {
		record, err := backend.Lookup(ctx, child.ID)
		if err != nil || !record.Pinned {
			t.Fatal(record, err)
		}
	}
	now = now.Add(time.Hour)
	dispatcher := &async.DelayedDispatcher{Client: client, ID: "limits-delayed"}
	if err := dispatcher.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	graph, err = group.Snapshot(ctx)
	if err != nil || graph.State != async.Failed || graph.Failure == nil || graph.Failure.Code != "WORKFLOW_SIZE" || called != 20 {
		t.Fatal(graph.State, graph.Failure, called, err)
	}
	if _, err := group.Join(ctx); err == nil {
		t.Fatal("oversized group fabricated success")
	}
	for _, child := range graph.Children {
		record, err := backend.Lookup(ctx, child.ID)
		if err != nil || record.State != async.Succeeded || record.Pinned || len(record.Output) < 240<<10 {
			t.Fatal(record.State, record.Pinned, len(record.Output), err)
		}
	}
}

func TestCanvasBudgetFailureSuppressesUndispatchedDAGDependents(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	task, _ := async.Register(registry, "limits.member", 1, func(context.Context, async.TaskContext, int) (string, error) {
		return strings.Repeat("x", 240<<10), nil
	}, async.TaskOptions{})
	body, _ := async.Register(registry, "limits.body", 1, func(context.Context, async.TaskContext, []string) (int, error) {
		t.Error("oversized barrier body executed")
		return 0, nil
	}, async.TaskOptions{})
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "limits-worker"}
	var canvases []async.Canvas
	for range 20 {
		s, _ := task.Signature(0)
		canvases = append(canvases, s.Canvas())
	}
	callback, _ := body.Signature(nil)
	group, err := client.ApplyCanvas(ctx, async.Parallel(canvases...).Then(callback.Canvas()))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	graph, err := group.Snapshot(ctx)
	if err != nil || graph.State != async.Failed || graph.Failure == nil || graph.Failure.Code != "WORKFLOW_SIZE" {
		t.Fatal(graph.State, graph.Failure, err)
	}
	last, err := backend.Lookup(ctx, graph.Children[len(graph.Children)-1].ID)
	if err != nil || last.State != async.Failed || last.Pinned || last.Failure.Code != "WORKFLOW_SIZE" {
		t.Fatal(last, err)
	}
}

func TestCanvasRejectsOversizedInitialSnapshotBeforeAcceptance(t *testing.T) {
	registry := async.NewRegistry()
	task, _ := async.Register(registry, "limits.input", 1, func(_ context.Context, _ async.TaskContext, value string) (string, error) { return value, nil }, async.TaskOptions{})
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend})
	if err != nil {
		t.Fatal(err)
	}
	var signatures []async.Signature
	for range 16 {
		s, _ := task.Signature(strings.Repeat("x", 100<<10))
		signatures = append(signatures, s)
	}
	if _, err := client.ApplyCanvas(context.Background(), async.Group(signatures...)); !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
	intents, err := backend.ListIntents(context.Background(), 100)
	if err != nil || len(intents) != 0 {
		t.Fatal(intents, err)
	}
}

func TestCanvasExpansionBudgetRollsBackUnpublishedClaims(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	first, _ := async.Register(registry, "limits.source", 1, func(context.Context, async.TaskContext, int) (string, error) {
		return strings.Repeat("x", 200<<10), nil
	}, async.TaskOptions{})
	child, _ := async.Register(registry, "limits.expanded", 1, func(context.Context, async.TaskContext, string) (string, error) {
		t.Error("uncommitted expanded child ran")
		return "", nil
	}, async.TaskOptions{})
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "expansion-worker"}
	var branches []async.Canvas
	for range 30 {
		s, _ := child.Signature("")
		branches = append(branches, s.Canvas())
	}
	source, _ := first.Signature(0)
	group, err := client.ApplyCanvas(ctx, source.Canvas().Then(async.Parallel(branches...)))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	graph, err := group.Snapshot(ctx)
	if err != nil || graph.State != async.Failed || graph.Failure == nil || graph.Failure.Code != "WORKFLOW_SIZE" {
		t.Fatal(graph.State, graph.Failure, err)
	}
	for index, child := range graph.Children {
		record, err := backend.Lookup(ctx, child.ID)
		if err != nil || record.Pinned || index > 0 && record.State != async.Failed {
			t.Fatal(index, record.State, record.Pinned, err)
		}
	}
}
