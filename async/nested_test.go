package async_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func TestNestedChainGroupChordAndEmptyBarrier(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	add, err := async.Register(registry, "nested.add", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n + 1, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sum, err := async.Register(registry, "nested.sum", 1, func(_ context.Context, _ async.TaskContext, ns []int) (int, error) {
		total := 0
		for _, n := range ns {
			total += n
		}
		return total, nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "nested"}
	first, _ := add.Signature(1)
	next, _ := add.Signature(0)
	body, _ := sum.Signature(nil)
	// (1+1) -> parallel[(2+1)+1, (2+1)] -> sum[4,3] -> +1 = 8.
	canvas := first.Canvas().Then(async.Parallel(async.Chain(next, next), next.Canvas())).Then(body.Canvas()).Then(next.Canvas())
	result, err := client.ApplyCanvas(ctx, canvas)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	outputs, err := result.Join(ctx)
	if err != nil || len(outputs) != 1 || string(outputs[0]) != "8" {
		t.Fatal(outputs, err)
	}
	graph, err := result.Snapshot(ctx)
	if err != nil || len(graph.Children) != 6 {
		t.Fatal(graph, err)
	}
	for _, child := range graph.Children {
		record, err := backend.Lookup(ctx, child.ID)
		if err != nil || record.Pinned {
			t.Fatal(record, err)
		}
	}
	empty, err := client.ApplyCanvas(ctx, async.Parallel().Then(body.Canvas()))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	outputs, err = empty.Join(ctx)
	if err != nil || string(outputs[0]) != "0" {
		t.Fatal(outputs, err)
	}
	parallel, err := client.ApplyCanvas(ctx, async.Parallel(async.Chain(first, next), first.Canvas()))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	outputs, err = parallel.Join(ctx)
	if err != nil || len(outputs) != 2 || string(outputs[0]) != "3" || string(outputs[1]) != "2" {
		t.Fatal(outputs, err)
	}
	nestedChord, err := async.Chord(async.Parallel(async.Chain(first, next), first.Canvas()), body)
	if err != nil {
		t.Fatal(err)
	}
	chord, err := client.ApplyCanvas(ctx, nestedChord)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	outputs, err = chord.Join(ctx)
	if err != nil || len(outputs) != 1 || string(outputs[0]) != "5" {
		t.Fatal(outputs, err)
	}
}

func TestNestedFailureFinalizesBlockedDescendantsIndependently(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	called := 0
	member, err := async.Register(registry, "nested.failure", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) {
		if n < 0 {
			return 0, errors.New("private failure")
		}
		called++
		return n, nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := async.Register(registry, "nested.body", 1, func(context.Context, async.TaskContext, []int) (int, error) {
		t.Error("failed barrier invoked body")
		return 0, nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "nested"}
	bad, _ := member.Signature(-1)
	good, _ := member.Signature(1)
	callback, _ := body.Signature(nil)
	result, err := client.ApplyCanvas(ctx, async.Parallel(async.Chain(bad, good), good.Canvas()).Then(callback.Canvas()))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	graph, err := result.Snapshot(ctx)
	if err != nil || graph.State != async.Failed || called != 1 {
		t.Fatal(graph.State, called, err)
	}
	for _, child := range graph.Children {
		record, err := backend.Lookup(ctx, child.ID)
		if err != nil || !record.State.Terminal() || record.Pinned {
			t.Fatal(record, err)
		}
	}
	intents, err := backend.ListIntents(ctx, 100)
	if err != nil || len(intents) != 0 {
		t.Fatal(intents, err)
	}
}

func TestNestedTypeAndScopeValidationBeforeGraphCreation(t *testing.T) {
	ctx := context.Background()
	task, client, _, backend := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{})
	s, _ := task.Signature(0)
	if _, err := client.ApplyCanvas(ctx, async.Parallel(s.Canvas()).Then(s.Canvas())); !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := client.ApplyCanvas(ctx, async.Parallel(s.Canvas(), s.Set(async.WithScope("other", "user")).Canvas())); !errors.Is(err, async.ErrDenied) {
		t.Fatal(err)
	}
	intents, err := backend.ListIntents(ctx, 100)
	if err != nil || len(intents) != 0 {
		t.Fatal(intents, err)
	}
}
