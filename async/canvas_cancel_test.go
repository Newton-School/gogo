package async_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
)

func TestCancelFlatAndNestedCanvasesBeforeAnyHandler(t *testing.T) {
	ctx := context.Background()
	task, client, worker, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) {
		t.Error("canceled workflow invoked handler")
		return 0, nil
	}, async.TaskOptions{})
	s, _ := task.Signature(1)
	for _, canvas := range []async.Canvas{async.Chain(s, s), async.Group(s, s), async.Parallel(async.Chain(s, s), s.Canvas()).Then(s.ImmutableSignature().Canvas())} {
		group, err := client.ApplyCanvas(ctx, canvas)
		if err != nil {
			t.Fatal(err)
		}
		if err := group.Revoke(ctx); err != nil {
			t.Fatal(err)
		}
		drain(t, client, worker, backend)
		graph, err := group.Snapshot(ctx)
		if err != nil || graph.State != async.Revoked {
			t.Fatal(graph.State, err)
		}
		for _, child := range graph.Children {
			record, err := backend.Lookup(ctx, child.ID)
			if err != nil || record.State != async.Revoked || record.Pinned {
				t.Fatal(record, err)
			}
		}
	}
}

func TestCancelFutureScheduleRetainsReplayHorizon(t *testing.T) {
	ctx := context.Background()
	task, client, worker, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) {
		t.Error("future canceled handler ran")
		return 0, nil
	}, async.TaskOptions{})
	eta := time.Now().Add(365 * 24 * time.Hour)
	s, _ := task.Signature(1)
	group, err := client.ApplyCanvas(ctx, async.Group(s.Set(async.WithETA(eta))))
	if err != nil {
		t.Fatal(err)
	}
	if err := group.Revoke(ctx); err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	graph, err := group.Snapshot(ctx)
	if err != nil || graph.State != async.Revoked {
		t.Fatal(graph, err)
	}
	record, err := backend.Lookup(ctx, graph.Children[0].ID)
	if err != nil || record.TombstoneUntil.Before(eta.Add(7*24*time.Hour)) {
		t.Fatal("tombstone ended before future replay horizon", record.TombstoneUntil, err)
	}
	backend.Clock = func() time.Time { return eta.Add(time.Second) }
	dispatcher := &async.DelayedDispatcher{Client: client, ID: "delayed"}
	if err := dispatcher.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"}); !errors.Is(err, async.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestWorkflowCancelDoesNotFinalizeLiveHandlerBeforeItStops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	observedCancel := make(chan struct{})
	allowExit := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(allowExit) })
	task, client, worker, backend := setup(t, func(ctx context.Context, _ async.TaskContext, _ int) (int, error) {
		close(started)
		<-ctx.Done()
		close(observedCancel)
		<-allowExit
		return 0, ctx.Err()
	}, async.TaskOptions{})
	worker.Heartbeat = 5 * time.Millisecond
	worker.Lease = time.Second
	s, _ := task.Signature(1)
	group, err := client.ApplyCanvas(ctx, async.Group(s))
	if err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{backend}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	delivery := take(t, backend)
	done := make(chan error, 1)
	go func() { done <- worker.Process(ctx, delivery) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := group.Revoke(ctx); err != nil {
		t.Fatal(err)
	}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observedCancel:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	graph, err := group.Snapshot(ctx)
	if err != nil || graph.State.Terminal() {
		t.Fatal("reported graph terminal while handler active", graph.State, err)
	}
	record, err := backend.Lookup(ctx, graph.Children[0].ID)
	if err != nil || record.State != async.Running || !record.CancelRequested {
		t.Fatal(record, err)
	}
	release.Do(func() { close(allowExit) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	graph, err = group.Snapshot(ctx)
	if err != nil || graph.State != async.Revoked {
		t.Fatal(graph.State, err)
	}
}
