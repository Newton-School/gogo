package async_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func TestWorkerCannotEnterHandlerAfterStartupObservationLosesAuthority(t *testing.T) {
	for _, scenario := range []string{"renewal_outage", "lease_lost", "observer_cancels", "authorize_cancels", "hook_cancels"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			backend := fakes.NewMemory()
			if scenario == "renewal_outage" || scenario == "lease_lost" {
				backend.Fail = func(op string) error {
					if op == "renew" {
						if scenario == "lease_lost" {
							return async.ErrLeaseLost
						}
						return async.ErrUnavailable
					}
					return nil
				}
			}
			options := async.TaskOptions{}
			if scenario == "authorize_cancels" {
				options.Authorize = func(context.Context, async.TaskContext) error { cancel(); return nil }
			}
			if scenario == "hook_cancels" {
				options.Hooks.BeforeStart = func(context.Context, async.TaskContext) error { cancel(); return nil }
			}
			registry := async.NewRegistry()
			calls := 0
			task, err := async.Register(registry, "lease.guard", 1, func(context.Context, async.TaskContext, int) (int, error) { calls++; return 42, nil }, options)
			if err != nil {
				t.Fatal(err)
			}
			client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := task.Delay(ctx, client, 1); err != nil {
				t.Fatal(err)
			}
			delivery, err := backend.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
			if err != nil {
				t.Fatal(err)
			}
			worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "worker", Lease: 150 * time.Millisecond, Heartbeat: 20 * time.Millisecond, Events: eventSinkFunc(func(ctx context.Context, event async.Event) error {
				if event.Kind == "task_started" {
					switch scenario {
					case "renewal_outage", "lease_lost":
						<-ctx.Done()
						return ctx.Err()
					case "observer_cancels":
						cancel()
					}
				}
				return nil
			})}
			err = worker.Process(ctx, delivery)
			if calls != 0 || err == nil {
				t.Fatal("handler started after lost startup authority", calls, err)
			}
			if scenario == "renewal_outage" && !errors.Is(err, async.ErrUnavailable) || scenario == "lease_lost" && !errors.Is(err, async.ErrLeaseLost) {
				t.Fatal(err)
			}
			stats, err := backend.Inspect(context.Background(), []string{"default"})
			if err != nil || stats[0].Pending != 1 {
				t.Fatal("uncertain execution was acknowledged", stats, err)
			}
		})
	}
}

func TestWorkerStartupObservationDoesNotConsumeSoftExecutionBudget(t *testing.T) {
	ctx := context.Background()
	backend := fakes.NewMemory()
	registry := async.NewRegistry()
	task, err := async.Register(registry, "lease.soft", 1, func(ctx context.Context, _ async.TaskContext, value int) (int, error) { return value, ctx.Err() }, async.TaskOptions{SoftLimit: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend})
	if err != nil {
		t.Fatal(err)
	}
	result, err := task.Delay(ctx, client, 42)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := backend.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "worker", Lease: time.Second, Heartbeat: 20 * time.Millisecond, Events: eventSinkFunc(func(ctx context.Context, event async.Event) error {
		if event.Kind == "task_started" {
			timer := time.NewTimer(80 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})}
	if err := worker.Process(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if value, err := result.Get(ctx); err != nil || value != 42 {
		t.Fatal(value, err)
	}
}
