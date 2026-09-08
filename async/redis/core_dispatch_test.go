package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	"github.com/Newton-School/gogo/core/tasks"
)

func TestRealRedisCoreDispatchAuthorizedResultAndRetention(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	broker, results := backends(t)
	registry := async.NewRegistry()
	calls := 0
	_, err := async.Register(registry, "core.native", 1, func(_ context.Context, tc async.TaskContext, value json.RawMessage) (json.RawMessage, error) {
		calls++
		if tc.Scope != "tenant-a" || tc.Retries != 0 {
			t.Fatal("lost task metadata")
		}
		return value, nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	allowRead := true
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results,
		Authorize: func(_ context.Context, action, scope, _ string) error {
			if scope != "tenant-a" || action == "read" && !allowRead {
				return async.ErrDenied
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := async.NewCoreDispatcher(client)
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"amount":123456789012345678901234567890.123456789}`)
	handle, err := dispatcher.Enqueue(ctx, tasks.Request{Task: "core.native", Version: 1, Args: args, Scope: "tenant-a"})
	if err != nil || handle == nil {
		t.Fatal(handle, err)
	}
	record, err := results.Lookup(ctx, handle.ID())
	if err != nil || record.State != async.Queued || calls != 0 {
		t.Fatal("admission represented execution", record.State, calls, err)
	}
	worker := &async.Worker{ID: "core-native", Registry: registry, Broker: broker, Results: results}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	allowRead = false
	if value, err := handle.Get(ctx); len(value) != 0 || !errors.Is(err, tasks.ErrDenied) {
		t.Fatal("current read denial was bypassed", err)
	}
	allowRead = true
	value, err := handle.Get(ctx)
	if err != nil || string(value) != string(args) || calls != 1 {
		t.Fatal("wire precision or task identity changed", string(value), calls, err)
	}
	value[0] = 'x'
	if next, err := handle.Get(ctx); err != nil || string(next) != string(args) {
		t.Fatal("result alias", err)
	}
	if err := results.Forget(ctx, handle.ID()); err != nil {
		t.Fatal(err)
	}
	if value, err := handle.Get(ctx); len(value) != 0 || !errors.Is(err, tasks.ErrResultExpired) {
		t.Fatal("forgotten payload fabricated output", err)
	}
}

func TestRealRedisCoreDispatchLostReplyRetainsExactRecovery(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		name := "transport reply lost"
		if confirmed {
			name = "registration reply lost"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			broker, results := backends(t)
			registry := async.NewRegistry()
			executions := 0
			_, err := async.Register(registry, "core.recovery", 1, func(context.Context, async.TaskContext, int) (int, error) { executions++; return 7, nil }, async.TaskOptions{})
			if err != nil {
				t.Fatal(err)
			}
			transport := &producerRetryBroker{Broker: broker, lost: !confirmed}
			projection := &producerRetryResults{ResultStore: results}
			cfg := async.ClientConfig{Registry: registry, Broker: transport, Results: results}
			if confirmed {
				cfg.Results = projection
			}
			client, err := async.NewClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			dispatcher, err := async.NewCoreDispatcher(client)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := dispatcher.Enqueue(ctx, tasks.Request{Task: "core.recovery", Version: 1, Args: json.RawMessage("1")})
			var portable *tasks.AcceptanceError
			var original *async.AcceptanceError
			if handle == nil || !errors.As(err, &portable) || !errors.As(err, &original) || portable.ID != handle.ID() || original.ID != handle.ID() || portable.Confirmed != confirmed || !errors.Is(err, io.ErrUnexpectedEOF) || transport.calls != 1 {
				t.Fatal("lost portable acceptance outcome", err)
			}
			receipt, err := original.Retry(ctx, client)
			if err != nil || receipt.ID != handle.ID() {
				t.Fatal("exact recovery failed", receipt, err)
			}
			wantTransportCalls := 2
			if confirmed {
				wantTransportCalls = 1
			}
			if transport.calls != wantTransportCalls {
				t.Fatal("registration repair republished accepted task", transport.calls)
			}
			stats, err := broker.Inspect(ctx, []string{"default"})
			if err != nil || len(stats) != 1 || stats[0].Queued != 1 {
				t.Fatal("recovery duplicated queue identity", stats, err)
			}
			worker := &async.Worker{ID: "core-recovery", Registry: registry, Broker: broker, Results: results}
			if err := worker.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if value, err := handle.Get(ctx); err != nil || string(value) != "7" || executions != 1 {
				t.Fatal(string(value), executions, err)
			}
		})
	}
}
