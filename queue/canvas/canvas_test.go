package canvas_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	q "github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/backends"
	"github.com/Newton-School/gogo/queue/brokers"
	"github.com/Newton-School/gogo/queue/canvas"
)

func TestChainSerializationExecutionOrderCallbacksAndErrbacks(t *testing.T) {
	ctx := context.Background()
	app, broker, backend := canvasTestRuntime()
	chain := canvas.NewChain(
		canvas.Task(q.NewSignature("tasks.first")).Link(q.NewSignature("tasks.callback")),
		canvas.Task(q.NewSignature("tasks.second")).Immutable(),
	).Link(q.NewSignature("tasks.callback"))
	result, err := chain.ApplyAsync(ctx, canvas.ApplyOptions{App: app, Broker: broker, Backend: backend})
	if err != nil {
		t.Fatalf("ApplyAsync() error = %v", err)
	}
	if len(result.TaskIDs) != 3 {
		t.Fatalf("result IDs = %#v", result)
	}
	serialized := chain.Serialize()
	if serialized.Type != "chain" || len(serialized.Tasks) != 2 || !serialized.Tasks[1].Immutable || len(serialized.Callbacks) != 1 {
		t.Fatalf("serialized = %#v", serialized)
	}
	order := consumeNames(t, ctx, broker, "default", 3)
	if !reflect.DeepEqual(order, []string{"tasks.first", "tasks.second", "tasks.callback"}) {
		t.Fatalf("order = %#v", order)
	}

	bad := canvas.NewChain(canvas.Task(q.NewSignature("tasks.missing"))).LinkError(q.NewSignature("tasks.errback"))
	if _, err := bad.ApplyAsync(ctx, canvas.ApplyOptions{App: app, Broker: broker, Backend: backend}); err == nil {
		t.Fatal("missing task chain should fail")
	}
	errbacks := consumeNames(t, ctx, broker, "default", 1)
	if !reflect.DeepEqual(errbacks, []string{"tasks.errback"}) {
		t.Fatalf("errbacks = %#v", errbacks)
	}
}

func TestGroupResultsAndChordBodyExecution(t *testing.T) {
	ctx := context.Background()
	app, broker, backend := canvasTestRuntime()
	group := canvas.NewGroup(
		canvas.Task(q.NewSignature("tasks.first")),
		canvas.Task(q.NewSignature("tasks.second")),
	)
	groupResult, err := group.ApplyAsync(ctx, canvas.ApplyOptions{App: app, Broker: broker, Backend: backend, GroupID: "group-1"})
	if err != nil {
		t.Fatalf("Group ApplyAsync() error = %v", err)
	}
	if groupResult.GroupID != "group-1" || len(groupResult.TaskIDs) != 2 {
		t.Fatalf("group result = %#v", groupResult)
	}
	stored, err := backend.GroupResult(ctx, "group-1", groupResult.TaskIDs)
	if err != nil || len(stored.Children) != 2 {
		t.Fatalf("stored group result = %#v, %v", stored, err)
	}

	chord := canvas.NewChord(group, canvas.Task(q.NewSignature("tasks.body")))
	chordResult, err := chord.Complete(ctx, canvas.ApplyOptions{App: app, Broker: broker, Backend: backend, ChordID: "chord-1"}, []any{"a", "b"})
	if err != nil {
		t.Fatalf("Chord Complete() error = %v", err)
	}
	if chordResult.ChordID != "chord-1" || len(chordResult.TaskIDs) != 1 {
		t.Fatalf("chord result = %#v", chordResult)
	}
	names := consumeNames(t, ctx, broker, "default", 3)
	if !reflect.DeepEqual(names, []string{"tasks.first", "tasks.second", "tasks.body"}) {
		t.Fatalf("names = %#v", names)
	}
}

func TestMapStarmapAndChunks(t *testing.T) {
	mapped := canvas.NewMap("tasks.first", []any{"a", "b"})
	if len(mapped.Tasks) != 2 || mapped.Tasks[0].Signature.Args[0] != "a" || mapped.Tasks[1].Signature.Args[0] != "b" {
		t.Fatalf("mapped = %#v", mapped)
	}
	starmap := canvas.NewStarmap("tasks.first", [][]any{{"a", 1}, {"b", 2}})
	if len(starmap.Tasks) != 2 || len(starmap.Tasks[0].Signature.Args) != 2 {
		t.Fatalf("starmap = %#v", starmap)
	}
	chunks := canvas.NewChunks("tasks.first", []any{1, 2, 3, 4, 5}, 2)
	if len(chunks.Tasks) != 3 || !reflect.DeepEqual(chunks.Tasks[2].Signature.Args[0], []any{5}) {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func canvasTestRuntime() (*q.App, *brokers.MemoryBroker, *backends.MemoryBackend) {
	app := q.NewApp(q.AppOptions{})
	for _, name := range []string{"tasks.first", "tasks.second", "tasks.body", "tasks.callback", "tasks.errback"} {
		taskName := name
		_, _ = app.RegisterTask(taskName, func(context.Context, ...any) (any, error) {
			if taskName == "tasks.fail" {
				return nil, errors.New("fail")
			}
			return taskName, nil
		}, q.TaskOptions{AckPolicy: q.AckLate})
	}
	return app, brokers.NewMemoryBroker(brokers.MemoryOptions{}), backends.NewMemoryBackend(backends.MemoryOptions{})
}

func consumeNames(t *testing.T, ctx context.Context, broker *brokers.MemoryBroker, queue string, count int) []string {
	t.Helper()
	names := make([]string, 0, count)
	for i := 0; i < count; i++ {
		message, err := broker.Consume(ctx, queue, brokers.ConsumeOptions{})
		if err != nil {
			t.Fatalf("Consume(%d) error = %v", i, err)
		}
		names = append(names, message.Envelope.Name)
		_ = broker.Ack(ctx, message)
	}
	return names
}
