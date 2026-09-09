package async_recipes_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Newton-School/gogo/async"
)

func Example_callbacksAndFailureOutcomes() {
	registry := async.NewRegistry()
	parent := must(async.Register(registry, "recipes.parent", 1,
		func(_ context.Context, _ async.TaskContext, n int) (int, error) {
			if n < 0 {
				return 0, errors.New("example task failed")
			}
			return n + 1, nil
		}, async.TaskOptions{}))
	var callbacks []int
	errbacks := 0
	safeFailures := true
	callback := must(async.Register(registry, "recipes.callback", 1,
		func(_ context.Context, _ async.TaskContext, n int) (int, error) {
			callbacks = append(callbacks, n)
			return n, nil
		}, async.TaskOptions{}))
	errback := must(async.Register(registry, "recipes.errback", 1,
		func(_ context.Context, _ async.TaskContext, failure async.Failure) (bool, error) {
			errbacks++
			safeFailures = safeFailures && failure.Message != "example task failed"
			return true, nil
		}, async.TaskOptions{}))
	h := newHarness(registry)
	defer h.cancel()
	link, errorLink := must(callback.Signature(0)), must(errback.Signature(async.Failure{}))
	good, bad := must(parent.Signature(1)), must(parent.Signature(-1))
	must(h.client.Enqueue(h.ctx, good.Link(link).LinkError(errorLink)))
	must(h.client.Enqueue(h.ctx, bad.Link(link).LinkError(errorLink)))
	h.drain()
	fmt.Println("callbacks:", callbacks, "errbacks:", errbacks)
	fmt.Println("safe failures:", safeFailures)
	// Join propagates workflow failure. JoinOutcomes deliberately keeps actual
	// per-member outcomes so an application can inspect partial success.
	group := must(h.client.ApplyCanvas(h.ctx, async.Group(good, bad)))
	h.drain()
	_, joinErr := group.Join(h.ctx)
	fmt.Println("join failed:", joinErr != nil)
	for _, member := range must(group.JoinOutcomes(h.ctx)) {
		fmt.Println("outcome:", member.State)
	}
	// Output:
	// callbacks: [2] errbacks: 1
	// safe failures: true
	// join failed: true
	// outcome: SUCCEEDED
	// outcome: FAILED
}

func Example_retryAndDelayedDispatch() {
	registry := async.NewRegistry()
	addOne := increment(registry)
	var attempts []int
	maxRetries := 1
	retrying := must(async.Register(registry, "recipes.retry_once", 1,
		func(ctx context.Context, task async.TaskContext, n int) (int, error) {
			attempts = append(attempts, task.Retries)
			if err := task.Progress(ctx, map[string]int{"attempt": task.Retries}); err != nil {
				return 0, err
			}
			if task.Retries == 0 {
				return 0, async.RetryAfter(errors.New("temporary example failure"), time.Minute)
			}
			return n * 2, nil
		}, async.TaskOptions{Retry: async.RetryPolicy{MaxRetries: &maxRetries, DisableJitter: true}}))
	h := newHarness(registry)
	defer h.cancel()
	result := must(retrying.Delay(h.ctx, h.client, 21))
	later := must(addOne.Delay(h.ctx, h.client, 9, async.WithCountdown(30*time.Second), async.WithExpiry(h.now.Add(time.Hour))))
	h.drain()
	fmt.Println("retry state:", must(result.Snapshot(h.ctx)).State)
	fmt.Println("delayed state:", must(later.Snapshot(h.ctx)).State)
	h.now = h.now.Add(30 * time.Second)
	h.drain()
	fmt.Println("delayed result:", must(later.Get(h.ctx)))
	h.now = h.now.Add(30 * time.Second)
	h.drain()
	fmt.Println("attempts:", attempts, "result:", must(result.Get(h.ctx)))
	// Advancing a fake clock is only a test technique. Production uses durable
	// retry intents and the explicitly operated relay/delayed dispatcher.
	// Output:
	// retry state: RETRY_WAIT
	// delayed state: SCHEDULED
	// delayed result: 10
	// attempts: [0 1] result: 42
}

func Example_revokeRestoreAndForget() {
	registry := async.NewRegistry()
	calls := 0
	task := must(async.Register(registry, "recipes.result", 1,
		func(_ context.Context, _ async.TaskContext, n int) (int, error) {
			calls++
			return n + 1, nil
		}, async.TaskOptions{}))
	h := newHarness(registry)
	defer h.cancel()
	canceled := must(task.Delay(h.ctx, h.client, 1))
	check(canceled.Revoke(h.ctx)) // A request, not evidence that a running task stopped.
	h.drain()
	fmt.Println("canceled:", must(canceled.Snapshot(h.ctx)).State, "calls:", calls)
	group := must(h.client.ApplyCanvas(h.ctx, async.Group(must(task.Signature(5)))))
	check(group.Revoke(h.ctx))
	h.drain()
	fmt.Println("canceled group:", must(group.Snapshot(h.ctx)).State, "calls:", calls)
	result := must(task.Delay(h.ctx, h.client, 41))
	h.drain()
	restored := async.RestoreResult[int](h.client, result.Receipt.ID)
	fmt.Println("restored:", must(restored.Get(h.ctx)), "ready:", must(restored.Ready(h.ctx)))
	check(restored.Forget(h.ctx)) // Payload removal retains the replay tombstone.
	_, err := restored.Get(h.ctx)
	fmt.Println("forgotten payload:", errors.Is(err, async.ErrResultExpired))
	// Output:
	// canceled: REVOKED calls: 0
	// canceled group: REVOKED calls: 0
	// restored: 42 ready: true
	// forgotten payload: true
}
