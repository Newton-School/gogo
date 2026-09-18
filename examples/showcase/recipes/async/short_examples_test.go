package async_recipes_test

import (
	"context"
	"fmt"
	"time"

	"github.com/Newton-School/gogo/async"
	simulator "github.com/Newton-School/gogo/async/testing"
)

func Example_smallTask() {
	// docs:begin task-options
	registry := async.NewRegistry()
	options := async.TaskOptions{
		Queue: "default", SoftLimit: 5 * time.Second, PerWorkerConcurrency: 2,
		Authorize: func(ctx context.Context, task async.TaskContext) error {
			if task.Scope != "" {
				return async.ErrDenied // This pure demo accepts no tenant scope.
			}
			return ctx.Err()
		},
	}
	// docs:end task-options
	// docs:begin task-register
	double, err := async.Register(registry, "demo.double", 1,
		func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if n < -1000 || n > 1000 {
				return 0, async.ErrInvalid
			}
			return n * 2, nil
		}, options)
	if err != nil {
		panic(err)
	}
	// docs:end task-register
	h := newHarness(registry)
	defer h.cancel()
	ctx, client := h.ctx, h.client
	// docs:begin task-delay
	result, err := double.Delay(ctx, client, 21, async.OnQueue("default"))
	if err != nil {
		panic(err)
	}
	// Admission succeeded; a worker still needs to execute the task.
	// docs:end task-delay
	h.drain()
	// docs:begin task-result
	value, err := result.Get(ctx) // Producer-side wait, bounded by ctx.
	if err != nil {
		panic(err)
	}
	fmt.Println(value) // 42
	// docs:end task-result
	// Output: 42
}

func Example_smallWorkflows() {
	registry := async.NewRegistry()
	addOne := increment(registry)
	h := newHarness(registry)
	defer h.cancel()
	ctx, client := h.ctx, h.client
	// docs:begin task-signature
	first, err := addOne.Signature(1)
	if err != nil {
		panic(err)
	}
	next, err := addOne.Signature(4)
	if err != nil {
		panic(err)
	}
	// docs:end task-signature
	// docs:begin task-chain
	chain, err := client.ApplyCanvas(ctx, async.Chain(first, next.FromParent("")))
	if err != nil {
		panic(err)
	}
	// first: 1 + 1 = 2. next receives 2, so its result is 3.
	// docs:end task-chain
	// docs:begin task-group
	group, err := client.ApplyCanvas(ctx, async.Group(first, next))
	if err != nil {
		panic(err)
	}
	// Independent inputs: 1 + 1 = 2, and 4 + 1 = 5.
	// docs:end task-group
	h.drain()
	chainValues, err := chain.Join(ctx)
	if err != nil {
		panic(err)
	}
	groupValues, err := group.Join(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(chainValues[0]))
	fmt.Println(string(groupValues[0]), string(groupValues[1]))
	// Output:
	// 3
	// 2 5
}

func Example_retryOptions() {
	// docs:begin task-retry
	policy := async.DefaultRetryPolicy()
	maximum := 5
	policy.MaxRetries = &maximum // Five retries after the initial attempt.
	policy.Backoff = true
	policy.BackoffFactor = time.Second
	policy.BackoffMax = time.Minute
	options := async.TaskOptions{Retry: policy}
	// docs:end task-retry
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	deadline, retry := options.Retry.Next(async.Retry(async.ErrUnavailable), 2, now, time.Time{}, 0.5)
	fmt.Println(retry, deadline.Sub(now))
	// Output: true 2s
}

func Example_clientAndWorkerOptions() {
	registry := async.NewRegistry()
	addOne := increment(registry)
	memory := simulator.NewMemory() // Test-only; use durable providers in production.
	// docs:begin client-options
	client, err := async.NewClient(async.ClientConfig{
		Registry: registry, Broker: memory, Results: memory,
		Workflows: memory, Schedules: memory,
		Queues:         []string{"default"},
		Routes:         []async.Route{{Pattern: "recipes.*", Queue: "default"}},
		AllowedHeaders: []string{"request-id"},
	})
	if err != nil {
		panic(err)
	}
	// docs:end client-options
	// docs:begin worker-options
	worker := &async.Worker{
		Registry: registry, Broker: memory, Results: memory,
		ID: "example-worker", Queues: []string{"default"}, Concurrency: 4,
		Lease: 60 * time.Second, Heartbeat: 20 * time.Second,
	}
	// In the worker process: return worker.Run(ctx).
	// This constructor does not start worker goroutines.
	// docs:end worker-options
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// docs:begin dispatch-options
	result, err := addOne.Delay(ctx, client, 41,
		async.OnQueue("default"), async.WithPriority(3),
		async.WithHeaders(map[string]string{"request-id": "example-request"}),
		async.WithStamps(map[string]string{"category": "example"}),
	)
	if err != nil {
		panic(err)
	}
	// docs:end dispatch-options
	delivery, err := memory.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: worker.ID})
	if err != nil {
		panic(err)
	}
	if err := worker.Process(ctx, delivery); err != nil {
		panic(err)
	}
	value, err := result.Get(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(value)
	// Output: 42
}
