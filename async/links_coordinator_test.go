package async_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func TestDelayedExpiryPersistsErrbackWithoutChangingOriginalOutcome(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	registry := async.NewRegistry()
	original, _ := async.Register(registry, "links.expired", 1, func(context.Context, async.TaskContext, int) (int, error) {
		t.Error("expired handler ran")
		return 0, nil
	}, async.TaskOptions{})
	var observed []string
	errback, _ := async.Register(registry, "links.expiry_error", 1, func(_ context.Context, _ async.TaskContext, failure async.Failure) (int, error) {
		observed = append(observed, failure.Code)
		return 0, errors.New("private errback failure")
	}, async.TaskOptions{})
	backend := fakes.NewMemory()
	backend.Clock = func() time.Time { return now }
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend, Schedules: backend, Clock: backend.Clock})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, Clock: backend.Clock, ID: "expiry-worker"}
	first, _ := original.Signature(0)
	failure, _ := errback.Signature(async.Failure{})
	receipt, err := client.Enqueue(ctx, first.LinkError(failure).Set(async.WithCountdown(time.Minute), async.WithExpiry(now.Add(2*time.Minute))))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Minute)
	dispatcher := &async.DelayedDispatcher{Client: client, ID: "expiry-dispatcher"}
	if err := dispatcher.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	record, err := backend.Lookup(ctx, receipt.ID)
	if err != nil || record.State != async.Expired || len(observed) != 1 || observed[0] != "EXPIRED" {
		t.Fatal(record.State, observed, err)
	}
	child, err := backend.Lookup(ctx, async.StableID(receipt.ID, "errback-0"))
	if err != nil || child.State != async.Failed {
		t.Fatal(child, err)
	}
}

func TestUndispatchedCanceledTaskRetainsErrback(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	original, _ := async.Register(registry, "links.cancel", 1, func(context.Context, async.TaskContext, int) (int, error) {
		t.Error("canceled handler ran")
		return 0, nil
	}, async.TaskOptions{})
	called := 0
	errback, _ := async.Register(registry, "links.cancel_error", 1, func(_ context.Context, _ async.TaskContext, failure async.Failure) (int, error) {
		if failure.Code != "REVOKED" {
			t.Error(failure.Code)
		}
		called++
		return 0, nil
	}, async.TaskOptions{})
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "cancel-worker"}
	s, _ := original.Signature(0)
	failure, _ := errback.Signature(async.Failure{})
	s = s.LinkError(failure)
	group, err := client.ApplyCanvas(ctx, async.Chain(s, s))
	if err != nil {
		t.Fatal(err)
	}
	if err := group.Revoke(ctx); err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	graph, err := group.Snapshot(ctx)
	if err != nil || graph.State != async.Revoked || called != 2 {
		t.Fatal(graph.State, called, err)
	}
}

func TestInvalidSuccessorInputRetainsErrback(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	first, _ := async.Register(registry, "links.first", 1, func(context.Context, async.TaskContext, int) (int, error) { return 42, nil }, async.TaskOptions{})
	next, _ := async.Register(registry, "links.next", 1, func(context.Context, async.TaskContext, int) (int, error) {
		t.Error("invalid input executed")
		return 0, nil
	}, async.TaskOptions{ValidatePayload: func(raw json.RawMessage) error {
		if string(raw) == "42" {
			return async.ErrInvalid
		}
		return nil
	}})
	called := 0
	errback, _ := async.Register(registry, "links.input_error", 1, func(_ context.Context, _ async.TaskContext, failure async.Failure) (int, error) {
		called++
		return 0, nil
	}, async.TaskOptions{})
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "input-worker"}
	start, _ := first.Signature(0)
	successor, _ := next.Signature(0)
	failure, _ := errback.Signature(async.Failure{})
	group, err := client.ApplyCanvas(ctx, async.Chain(start, successor.LinkError(failure)))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	graph, err := group.Snapshot(ctx)
	if err != nil || graph.State != async.Failed || called != 1 {
		t.Fatal(graph.State, called, err)
	}
}
