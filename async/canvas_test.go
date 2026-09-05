package async_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func drain(t *testing.T, client *async.Client, worker *async.Worker, backend *fakes.Memory) {
	t.Helper()
	ctx := context.Background()
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{backend}, ID: "relay"}
	for turn := 0; turn < 12; turn++ {
		if err := relay.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		for {
			d, err := backend.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
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
}
func TestChainForwardingAndRetentionPinRelease(t *testing.T) {
	ctx := context.Background()
	task, client, worker, backend := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n + 1, nil }, async.TaskOptions{})
	first, _ := task.Signature(1)
	next, _ := task.Signature(0)
	result, err := client.ApplyCanvas(ctx, async.Chain(first, next, next))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	outputs, err := result.Join(ctx)
	if err != nil || len(outputs) != 1 || string(outputs[0]) != "4" {
		t.Fatal(outputs, err)
	}
	graph, _ := result.Snapshot(ctx)
	for _, child := range graph.Children {
		record, err := backend.Lookup(ctx, child.ID)
		if err != nil || record.Pinned {
			t.Fatal(record, err)
		}
		if err := backend.Forget(ctx, child.ID); err != nil {
			t.Fatal(err)
		}
	}
}
func TestChordOrderedBarrierAndDuplicateCompletion(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	calls := 0
	member, err := async.Register(registry, "test.member", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n * 2, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := async.Register(registry, "test.body", 1, func(_ context.Context, _ async.TaskContext, values []int) (int, error) {
		calls++
		return values[0] + values[1], nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "worker"}
	one, _ := member.Signature(2)
	two, _ := member.Signature(3)
	callback, _ := body.Signature(nil)
	canvas, err := async.Chord(async.Group(one, two), callback)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ApplyCanvas(ctx, canvas)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	outputs, err := result.Join(ctx)
	if err != nil || string(outputs[0]) != "10" || calls != 1 {
		t.Fatal(outputs, calls, err)
	}
	graph, _ := result.Snapshot(ctx)
	completion := graph.Members[graph.Children[0].ID]
	if err := backend.RecordMember(ctx, graph.ID, completion, func(async.Graph) (async.Graph, []async.Intent, error) {
		t.Fatal("duplicate advanced workflow")
		return async.Graph{}, nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	unknown, _ := async.NewID()
	if err := backend.RecordMember(ctx, graph.ID, async.Completion{ID: unknown, State: async.Succeeded, Output: json.RawMessage(`1`)}, func(g async.Graph) (async.Graph, []async.Intent, error) { return g, nil, nil }); !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
}
func TestWorkflowEmptyAndTypeMismatch(t *testing.T) {
	ctx := context.Background()
	task, client, worker, backend := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{})
	result, err := client.ApplyCanvas(ctx, async.Group())
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := result.Join(ctx)
	if err != nil || len(outputs) != 0 {
		t.Fatal(outputs, err)
	}
	s, _ := task.Signature(1)
	canvas, err := async.Chord(async.Group(s), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ApplyCanvas(ctx, canvas); !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
}

func TestCallbackAndErrbackDurableLinks(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	successes, failures := 0, 0
	parent, err := async.Register(registry, "test.parent", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) {
		if n < 0 {
			return 0, errors.New("private failure")
		}
		return n + 1, nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	callback, err := async.Register(registry, "test.callback", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { successes++; return n, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	errback, err := async.Register(registry, "test.errback", 1, func(_ context.Context, _ async.TaskContext, f async.Failure) (int, error) {
		failures++
		if f.Message == "private failure" {
			t.Fatal("unredacted failure")
		}
		return 0, nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "worker"}
	good, _ := parent.Signature(1)
	bad, _ := parent.Signature(-1)
	link, _ := callback.Signature(0)
	errorLink, _ := errback.Signature(async.Failure{})
	if _, err := client.Enqueue(ctx, good.Link(link).LinkError(errorLink)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Enqueue(ctx, bad.Link(link).LinkError(errorLink)); err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	if successes != 1 || failures != 1 {
		t.Fatal(successes, failures)
	}
}

func TestObserverPanicDoesNotUndoCommittedResult(t *testing.T) {
	ctx := context.Background()
	task, client, worker, backend := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{Hooks: async.Hooks{OnSuccess: func(context.Context, async.TaskContext, json.RawMessage) error { panic("observer") }}})
	reported := 0
	worker.OnError = func(error) { reported++ }
	result, err := task.Delay(ctx, client, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(ctx, take(t, backend)); err != nil {
		t.Fatal(err)
	}
	out, err := result.Get(ctx)
	if err != nil || out != 5 || reported != 1 {
		t.Fatal(out, reported, err)
	}
}

func TestErrorObserverPanicIsContainedAfterDurableSuccess(t *testing.T) {
	ctx := context.Background()
	task, client, worker, backend := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{Hooks: async.Hooks{OnSuccess: func(context.Context, async.TaskContext, json.RawMessage) error { return errors.New("observer failed") }}})
	worker.OnError = func(error) { panic("error observer failed") }
	result, err := task.Delay(ctx, client, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(ctx, take(t, backend)); err != nil {
		t.Fatal(err)
	}
	if value, err := result.Get(ctx); err != nil || value != 7 {
		t.Fatal(value, err)
	}
}

func TestWorkflowInputRejectionFinishesWithoutPoisoningRelay(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	first, err := async.Register(registry, "test.source", 1, func(context.Context, async.TaskContext, int) (int, error) { return 99, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	next, err := async.Register(registry, "test.validated", 1, func(context.Context, async.TaskContext, int) (int, error) { called = true; return 1, nil }, async.TaskOptions{ValidatePayload: func(raw json.RawMessage) error {
		if string(raw) != "0" {
			return errors.New("private validation detail")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := async.Register(registry, "test.validated_body", 1, func(context.Context, async.TaskContext, []int) (int, error) { called = true; return 1, nil }, async.TaskOptions{ValidatePayload: func(raw json.RawMessage) error {
		if string(raw) != "null" {
			return async.ErrInvalid
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "worker"}
	source, _ := first.Signature(0)
	successor, _ := next.Signature(0)
	callback, _ := body.Signature(nil)
	chord, _ := async.Chord(async.Group(source), callback)
	for _, canvas := range []async.Canvas{async.Chain(source, successor), chord} {
		result, err := client.ApplyCanvas(ctx, canvas)
		if err != nil {
			t.Fatal(err)
		}
		drain(t, client, worker, backend)
		graph, err := result.Snapshot(ctx)
		if err != nil || graph.State != async.Failed || graph.Failure == nil {
			t.Fatal(graph, err)
		}
	}
	if called {
		t.Fatal("invalid bound input reached a handler")
	}
	intents, err := backend.ListIntents(ctx, 100)
	if err != nil || len(intents) != 0 {
		t.Fatal("validation failure left a permanently pending relay intent", intents, err)
	}
}
