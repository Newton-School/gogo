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

func TestWorkerRetriesTaskAndStoresRetryState(t *testing.T) {
	ctx := context.Background()
	app := q.NewApp(q.AppOptions{})
	attempts := 0
	_, err := app.RegisterTask("jobs.retry", func(context.Context, ...any) (any, error) {
		attempts++
		if attempts == 1 {
			return nil, q.Retry(errors.New("temporary"), q.RetryCountdown(10*time.Millisecond))
		}
		return "ok", nil
	}, q.TaskOptions{AckPolicy: q.AckLate, MaxRetries: 2, DefaultRetryDelay: time.Minute})
	if err != nil {
		t.Fatalf("RegisterTask() error = %v", err)
	}
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})
	_, _ = broker.Publish(ctx, "default", q.Envelope{ID: "task-retry", Name: "jobs.retry"}, brokers.PublishOptions{})
	worker := q.NewWorker(app, broker, backend, q.WorkerOptions{Queues: []string{"default"}})

	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce(retry) error = %v", err)
	}
	retryResult, err := backend.GetResult(ctx, "task-retry")
	if err != nil || retryResult.State != q.StateRetry || !strings.Contains(retryResult.Error, "temporary") {
		t.Fatalf("retry result = %#v, %v", retryResult, err)
	}
	time.Sleep(15 * time.Millisecond)
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce(success) error = %v", err)
	}
	result, err := backend.GetResult(ctx, "task-retry")
	if err != nil || result.State != q.StateSuccess || result.Result != "ok" {
		t.Fatalf("success result = %#v, %v", result, err)
	}
}

func TestWorkerFailsWhenMaxRetriesExceeded(t *testing.T) {
	ctx := context.Background()
	app := q.NewApp(q.AppOptions{})
	_, err := app.RegisterTask("jobs.retry.max", func(context.Context, ...any) (any, error) {
		return nil, q.Retry(errors.New("still failing"))
	}, q.TaskOptions{AckPolicy: q.AckLate, MaxRetries: 1, DefaultRetryDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("RegisterTask() error = %v", err)
	}
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})
	_, _ = broker.Publish(ctx, "default", q.Envelope{ID: "task-max", Name: "jobs.retry.max", Retries: 1}, brokers.PublishOptions{})
	worker := q.NewWorker(app, broker, backend, q.WorkerOptions{Queues: []string{"default"}})
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	result, err := backend.GetResult(ctx, "task-max")
	if err != nil || result.State != q.StateFailure || !strings.Contains(result.Error, "still failing") {
		t.Fatalf("max retry result = %#v, %v", result, err)
	}
	queues, _ := broker.InspectQueues(ctx)
	if len(queues) != 1 || queues[0].Ready != 0 || queues[0].InFlight != 0 {
		t.Fatalf("queues = %#v", queues)
	}
}

func TestWorkerSoftAndHardTimeouts(t *testing.T) {
	ctx := context.Background()
	app := q.NewApp(q.AppOptions{})
	_, err := app.RegisterTask("jobs.soft", func(ctx context.Context, _ ...any) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, q.TaskOptions{AckPolicy: q.AckLate, SoftTimeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("RegisterTask(soft) error = %v", err)
	}
	release := make(chan struct{})
	_, err = app.RegisterTask("jobs.hard", func(_ context.Context, _ ...any) (any, error) {
		<-release
		return "late", nil
	}, q.TaskOptions{AckPolicy: q.AckLate, HardTimeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("RegisterTask(hard) error = %v", err)
	}
	defer close(release)
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})
	_, _ = broker.Publish(ctx, "default", q.Envelope{ID: "task-soft", Name: "jobs.soft"}, brokers.PublishOptions{})
	_, _ = broker.Publish(ctx, "default", q.Envelope{ID: "task-hard", Name: "jobs.hard"}, brokers.PublishOptions{})
	worker := q.NewWorker(app, broker, backend, q.WorkerOptions{Queues: []string{"default"}})
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce(soft) error = %v", err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce(hard) error = %v", err)
	}
	soft, _ := backend.GetResult(ctx, "task-soft")
	hard, _ := backend.GetResult(ctx, "task-hard")
	if soft.State != q.StateFailure || !strings.Contains(soft.Error, q.ErrSoftTimeout.Error()) {
		t.Fatalf("soft result = %#v", soft)
	}
	if hard.State != q.StateFailure || !strings.Contains(hard.Error, q.ErrHardTimeout.Error()) {
		t.Fatalf("hard result = %#v", hard)
	}
}

func TestWorkerRevokesTaskByIDAndStampedHeaders(t *testing.T) {
	ctx := context.Background()
	app := q.NewApp(q.AppOptions{})
	_, err := app.RegisterTask("jobs.run", func(context.Context, ...any) (any, error) {
		return "should-not-run", nil
	}, q.TaskOptions{AckPolicy: q.AckLate})
	if err != nil {
		t.Fatalf("RegisterTask() error = %v", err)
	}
	registry := q.NewRevocationRegistry()
	registry.RevokeTask("task-revoked")
	registry.RevokeStampedHeader("tenant", "blocked")
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})
	_, _ = broker.Publish(ctx, "default", q.Envelope{ID: "task-revoked", Name: "jobs.run"}, brokers.PublishOptions{})
	_, _ = broker.Publish(ctx, "default", q.Envelope{ID: "task-stamped", Name: "jobs.run", Headers: map[string]string{"tenant": "blocked"}}, brokers.PublishOptions{})
	worker := q.NewWorker(app, broker, backend, q.WorkerOptions{Queues: []string{"default"}, Revocations: registry})
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce(task) error = %v", err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce(stamped) error = %v", err)
	}
	first, _ := backend.GetResult(ctx, "task-revoked")
	second, _ := backend.GetResult(ctx, "task-stamped")
	if first.State != q.StateRevoked || second.State != q.StateRevoked {
		t.Fatalf("revoked results = %#v %#v", first, second)
	}
	stats := worker.Stats()
	if stats.Revoked != 2 || stats.Acked != 2 {
		t.Fatalf("stats = %#v", stats)
	}
}
