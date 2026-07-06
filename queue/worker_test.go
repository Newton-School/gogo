package queue_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	q "github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/backends"
	"github.com/Newton-School/gogo/queue/brokers"
)

func TestWorkerExecutesTaskStoresSuccessAndAcksLate(t *testing.T) {
	ctx := context.Background()
	app := q.NewApp(q.AppOptions{})
	_, err := app.RegisterTask("math.add", func(_ context.Context, args ...any) (any, error) {
		return args[0].(int) + args[1].(int), nil
	}, q.TaskOptions{AckPolicy: q.AckLate, TrackStarted: true})
	if err != nil {
		t.Fatalf("RegisterTask() error = %v", err)
	}
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})
	if _, err := broker.Publish(ctx, "default", q.Envelope{ID: "task-1", Name: "math.add", Args: []any{2, 3}}, brokers.PublishOptions{}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	worker := q.NewWorker(app, broker, backend, q.WorkerOptions{Hostname: "worker-a", Queues: []string{"default"}, Concurrency: 1})
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	result, err := backend.GetResult(ctx, "task-1")
	if err != nil || result.State != q.StateSuccess || result.Result != 5 {
		t.Fatalf("result = %#v, %v", result, err)
	}
	stats := worker.Stats()
	if stats.Hostname != "worker-a" || stats.Acked != 1 || stats.Succeeded != 1 || stats.Running != 0 || stats.PoolStrategy != q.PoolGoroutine {
		t.Fatalf("stats = %#v", stats)
	}
	queues, _ := broker.InspectQueues(ctx)
	if len(queues) != 1 || queues[0].InFlight != 0 {
		t.Fatalf("queues = %#v", queues)
	}
}

func TestWorkerStoresFailureAndRespectsAckPolicies(t *testing.T) {
	ctx := context.Background()
	app := q.NewApp(q.AppOptions{})
	_, err := app.RegisterTask("jobs.fail", func(context.Context, ...any) (any, error) {
		return nil, errors.New("boom")
	}, q.TaskOptions{AckPolicy: q.AckEarly})
	if err != nil {
		t.Fatalf("RegisterTask(fail) error = %v", err)
	}
	_, err = app.RegisterTask("jobs.manual", func(context.Context, ...any) (any, error) {
		return "held", nil
	}, q.TaskOptions{AckPolicy: q.AckManual})
	if err != nil {
		t.Fatalf("RegisterTask(manual) error = %v", err)
	}
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})
	_, _ = broker.Publish(ctx, "default", q.Envelope{ID: "task-2", Name: "jobs.fail"}, brokers.PublishOptions{})
	_, _ = broker.Publish(ctx, "default", q.Envelope{ID: "task-3", Name: "jobs.manual"}, brokers.PublishOptions{})

	worker := q.NewWorker(app, broker, backend, q.WorkerOptions{Queues: []string{"default"}, Pool: q.NewSoloPool()})
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce(fail) error = %v", err)
	}
	failed, err := backend.GetResult(ctx, "task-2")
	if err != nil || failed.State != q.StateFailure || !strings.Contains(failed.Error, "boom") {
		t.Fatalf("failed result = %#v, %v", failed, err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce(manual) error = %v", err)
	}
	manual, err := backend.GetResult(ctx, "task-3")
	if err != nil || manual.State != q.StateSuccess || manual.Result != "held" {
		t.Fatalf("manual result = %#v, %v", manual, err)
	}
	queues, _ := broker.InspectQueues(ctx)
	if len(queues) != 1 || queues[0].InFlight != 1 {
		t.Fatalf("manual ack should leave one in-flight delivery, queues = %#v", queues)
	}
	stats := worker.Stats()
	if stats.Acked != 1 || stats.Failed != 1 || stats.Succeeded != 1 || stats.PoolStrategy != q.PoolSolo {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestWorkerRuntimeOptionsAutoscaleLimitsAndPrefetch(t *testing.T) {
	app := q.NewApp(q.AppOptions{})
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})
	worker := q.NewWorker(app, broker, backend, q.WorkerOptions{
		Concurrency:             2,
		PrefetchMultiplier:      3,
		Autoscale:               q.AutoscaleConfig{MinConcurrency: 1, MaxConcurrency: 4, ScaleUpReadyTasks: 5},
		MaxTasksPerWorkerChild:  2,
		MaxMemoryPerWorkerChild: 100,
		MemoryUsage:             func() uint64 { return 101 },
		RejectOnWorkerLost:      true,
		ShutdownTimeout:         time.Second,
		Pool:                    q.NewProcessPool(q.ProcessPoolOptions{}),
	})
	if worker.PrefetchLimit() != 6 {
		t.Fatalf("PrefetchLimit() = %d", worker.PrefetchLimit())
	}
	if got := worker.TargetConcurrency(0); got != 1 {
		t.Fatalf("TargetConcurrency(0) = %d", got)
	}
	if got := worker.TargetConcurrency(6); got != 4 {
		t.Fatalf("TargetConcurrency(6) = %d", got)
	}
	if !worker.MemoryLimitExceeded() {
		t.Fatal("MemoryLimitExceeded() = false, want true")
	}
	stats := worker.Stats()
	if !stats.RejectOnWorkerLost || stats.PoolStrategy != q.PoolProcessBacked || stats.MaxTasksPerWorkerChild != 2 || stats.MaxMemoryPerWorkerChild != 100 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestWorkerWarmShutdownCancelsRunningTask(t *testing.T) {
	app := q.NewApp(q.AppOptions{})
	started := make(chan struct{})
	_, err := app.RegisterTask("jobs.block", func(ctx context.Context, _ ...any) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, q.TaskOptions{AckPolicy: q.AckLate})
	if err != nil {
		t.Fatalf("RegisterTask() error = %v", err)
	}
	ctx := context.Background()
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})
	_, _ = broker.Publish(ctx, "default", q.Envelope{ID: "task-4", Name: "jobs.block"}, brokers.PublishOptions{})
	worker := q.NewWorker(app, broker, backend, q.WorkerOptions{Queues: []string{"default"}, ShutdownTimeout: time.Second})
	if err := worker.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Shutdown(shutdownCtx, q.WarmShutdown); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	stats := worker.Stats()
	if stats.Running != 0 || stats.Failed != 1 {
		t.Fatalf("stats after shutdown = %#v", stats)
	}
}
