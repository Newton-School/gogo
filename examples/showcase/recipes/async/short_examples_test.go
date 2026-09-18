package async_recipes_test

import (
	"context"
	"fmt"
	"time"

	"github.com/Newton-School/gogo/async"
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
