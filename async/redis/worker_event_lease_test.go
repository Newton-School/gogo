package redis

import (
	"context"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
)

type leaseEventSink func(context.Context, async.Event) error

func (f leaseEventSink) PublishEvent(ctx context.Context, event async.Event) error {
	return f(ctx, event)
}

func TestRealRedisWorkerRenewsShortLeaseDuringStartedObservation(t *testing.T) {
	ctx := context.Background()
	config := fixture.Start(t)
	config.Role = connector.TaskRole
	connection, err := connector.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	broker, results := &Broker{Connection: connection}, &Results{Connection: connection}
	registry := async.NewRegistry()
	calls := 0
	leaseValid := false
	task, err := async.Register(registry, "event.lease", 1, func(ctx context.Context, task async.TaskContext, value int) (int, error) {
		calls++
		record, err := results.Lookup(ctx, task.ID)
		if err != nil {
			return 0, err
		}
		now, err := connector.ServerTime(ctx, connection.Client())
		if err != nil {
			return 0, err
		}
		leaseValid = record.Owner == task.WorkerID && record.Fence == task.Fence && record.LeaseUntil.After(now)
		return value, nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := task.Delay(ctx, client, 42); err != nil {
		t.Fatal(err)
	}
	delivery, err := broker.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, ID: "worker", Lease: 150 * time.Millisecond, Heartbeat: 20 * time.Millisecond, Events: leaseEventSink(func(ctx context.Context, event async.Event) error {
		if event.Kind == "task_started" {
			timer := time.NewTimer(400 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})}
	err = worker.Process(ctx, delivery)
	if err != nil || calls != 1 || !leaseValid {
		t.Fatal("handler entered without a renewed lease", calls, leaseValid, err)
	}
}
