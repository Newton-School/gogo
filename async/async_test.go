package async_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func setup(t *testing.T, handler async.Handler[int, int], options async.TaskOptions) (*async.Task[int, int], *async.Client, *async.Worker, *fakes.Memory) {
	t.Helper()
	registry := async.NewRegistry()
	task, err := async.Register(registry, "test.add", 1, handler, options)
	if err != nil {
		t.Fatal(err)
	}
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Schedules: backend, Workflows: backend})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "worker"}
	return task, client, worker, backend
}
func take(t *testing.T, b async.Broker) async.Delivery {
	t.Helper()
	d, err := b.Consume(context.Background(), async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestTaskTypedRoundTripAndDuplicate(t *testing.T) {
	ctx := context.Background()
	calls := 0
	task, client, worker, backend := setup(t, func(ctx context.Context, tc async.TaskContext, input int) (int, error) {
		calls++
		if tc.Retries != 0 || tc.Fence == 0 {
			t.Fatal(tc)
		}
		return input + 1, nil
	}, async.TaskOptions{})
	result, err := task.Delay(ctx, client, 41)
	if err != nil {
		t.Fatal(err)
	}
	delivery := take(t, backend)
	if err := worker.Process(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	output, err := result.Get(ctx)
	if err != nil || output != 42 {
		t.Fatalf("%v %v", output, err)
	}
	if err := worker.Process(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestRetryDefaultsAndBoundaries(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := async.DefaultRetryPolicy()
	if _, ok := p.Next(errors.New("ordinary"), 0, now, time.Time{}, .5); ok {
		t.Fatal("ordinary error auto-retried")
	}
	for i := 0; i < 4; i++ {
		eta, ok := p.Next(async.Retry(errors.New("retry")), i, now, time.Time{}, .5)
		if ok != (i < 3) {
			t.Fatal(i, ok)
		}
		if ok && eta.Sub(now) != 180*time.Second {
			t.Fatal(eta)
		}
	}
	if _, ok := p.Next(async.Retry(nil), 0, now, now.Add(180*time.Second), 0); ok {
		t.Fatal("retry admitted at expiry")
	}
	p.Backoff = true
	eta, ok := p.Next(async.Retry(nil), 2, now, time.Time{}, .5)
	if !ok || eta.Sub(now) != 2*time.Second {
		t.Fatal(eta, ok)
	}
	eta, ok = p.Next(async.RetryAfter(nil, 7*time.Second), 2, now, time.Time{}, .5)
	if !ok || eta.Sub(now) != 7*time.Second {
		t.Fatal(eta, ok)
	}
}

func TestRetryPersistsIntentBeforeACK(t *testing.T) {
	ctx := context.Background()
	task, client, worker, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) { return 0, async.Retry(nil) }, async.TaskOptions{})
	result, err := task.Delay(ctx, client, 1)
	if err != nil {
		t.Fatal(err)
	}
	d := take(t, backend)
	backend.Fail = func(operation string) error {
		if operation == "transition" {
			return async.ErrUnavailable
		}
		return nil
	}
	if err := worker.Process(ctx, d); !errors.Is(err, async.ErrUnavailable) {
		t.Fatal(err)
	}
	stats, _ := backend.Inspect(ctx, []string{"default"})
	if stats[0].Pending != 1 {
		t.Fatal(stats)
	}
	record, _ := result.Snapshot(ctx)
	if record.State != async.Running {
		t.Fatal(record.State)
	}
	backend.Fail = nil
	// Recovery is transport redelivery and preserves retries=0.
	future := time.Now().Add(2 * time.Minute)
	backend.Clock = func() time.Time { return future }
	worker.Clock = backend.Clock
	if err := worker.Process(ctx, d); err != nil {
		t.Fatal(err)
	}
	record, _ = result.Snapshot(ctx)
	if record.State != async.RetryWait || record.Envelope.Retries != 1 || record.DeliveryCount != 2 {
		t.Fatal(record)
	}
	intents, _ := backend.ListIntents(ctx, 10)
	if len(intents) != 1 || intents[0].Envelope.Retries != 1 {
		t.Fatal(intents)
	}
	stats, _ = backend.Inspect(ctx, []string{"default"})
	if stats[0].Pending != 0 {
		t.Fatal(stats)
	}
}

func TestClaimFencingAndLiveLease(t *testing.T) {
	ctx := context.Background()
	task, client, _, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) { return 0, nil }, async.TaskOptions{})
	_, err := task.Delay(ctx, client, 1)
	if err != nil {
		t.Fatal(err)
	}
	d := take(t, backend)
	e, err := async.DecodeEnvelope(d.Body)
	if err != nil {
		t.Fatal(err)
	}
	first, err := backend.Claim(ctx, e, "first", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := backend.Claim(ctx, e, "second", time.Second)
	if err != nil || second.Acquired {
		t.Fatal(second, err)
	}
	backend.Clock = func() time.Time { return time.Now().Add(2 * time.Second) }
	second, err = backend.Claim(ctx, e, "second", time.Second)
	if err != nil || !second.Acquired || second.Record.Fence <= first.Record.Fence {
		t.Fatal(second, err)
	}
	err = backend.Transition(ctx, async.Transition{ID: e.ID, Fence: first.Record.Fence, Owner: "first", State: async.Succeeded, Output: json.RawMessage(`1`)})
	if !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal(err)
	}
}

func TestAtomicClaimRace(t *testing.T) {
	ctx := context.Background()
	task, client, _, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) { return 0, nil }, async.TaskOptions{})
	_, err := task.Delay(ctx, client, 1)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := async.DecodeEnvelope(take(t, backend).Body)
	var winners atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := backend.Claim(ctx, e, "worker", time.Minute)
			if err != nil {
				t.Error(err)
			}
			if c.Acquired {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal(winners.Load())
	}
}

func TestProtocolAndRegistryValidation(t *testing.T) {
	registry := async.NewRegistry()
	_, err := async.Register[int, int](registry, "gogo.reserved", 1, nil, async.TaskOptions{})
	if !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
	_, err = async.Register[int, int](registry, "app.task", 1, nil, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(true); !errors.Is(err, async.ErrUnknownTask) {
		t.Fatal(err)
	}
	if err := registry.Freeze(false); err != nil {
		t.Fatal(err)
	}
	_, err = async.Register[int, int](registry, "app.other", 1, nil, async.TaskOptions{})
	if !errors.Is(err, async.ErrFrozen) {
		t.Fatal(err)
	}
	if _, err := async.DecodeEnvelope([]byte(strings.Repeat("x", async.MaxPayloadBytes+1))); err == nil {
		t.Fatal("oversize")
	}
}

func TestCancellationAndSafeFailures(t *testing.T) {
	ctx := context.Background()
	calls := 0
	task, client, worker, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) {
		calls++
		return 0, errors.New("password=private")
	}, async.TaskOptions{})
	r, err := task.Delay(ctx, client, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Revoke(ctx); err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(ctx, take(t, backend)); err != nil {
		t.Fatal(err)
	}
	record, _ := r.Snapshot(ctx)
	if calls != 0 || record.State != async.Revoked {
		t.Fatal(calls, record)
	}
	r, err = task.Delay(ctx, client, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(ctx, take(t, backend)); err != nil {
		t.Fatal(err)
	}
	record, _ = r.Snapshot(ctx)
	if strings.Contains(record.Failure.Error(), "private") {
		t.Fatal(record)
	}
}

func TestResultWaitGuardAndSignatureIsolation(t *testing.T) {
	ctx := context.Background()
	var result *async.Result[int]
	task, client, worker, backend := setup(t, func(ctx context.Context, _ async.TaskContext, input int) (int, error) {
		_, err := result.Get(ctx)
		if !errors.Is(err, async.ErrWorkerJoin) {
			t.Fatal(err)
		}
		return input, nil
	}, async.TaskOptions{})
	var err error
	result, err = task.Delay(ctx, client, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(ctx, take(t, backend)); err != nil {
		t.Fatal(err)
	}
	s, _ := task.Signature(1)
	headers := map[string]string{"trace": "a"}
	copy := s.Set(async.WithHeaders(headers))
	headers["trace"] = "b"
	if copy.Options.Headers["trace"] != "a" || s.Options.Headers != nil {
		t.Fatal(copy, s)
	}
}

func TestUncertainAcceptanceReplaysExactEnvelope(t *testing.T) {
	ctx := context.Background()
	task, client, _, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) { return 1, nil }, async.TaskOptions{})
	backend.Fail = func(operation string) error {
		if operation == "publish" {
			return async.ErrUnavailable
		}
		return nil
	}
	_, err := task.Delay(ctx, client, 1)
	var acceptance *async.AcceptanceError
	if !errors.As(err, &acceptance) || acceptance.ID == "" {
		t.Fatal(err)
	}
	backend.Fail = nil
	receipt, err := acceptance.Retry(ctx, client)
	if err != nil || receipt.ID != acceptance.ID {
		t.Fatal(receipt, err)
	}
}

func TestWorkerRenewsLeaseAndCooperativelyRevokes(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	observed := make(chan struct{})
	task, client, worker, backend := setup(t, func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
		close(started)
		<-ctx.Done()
		close(observed)
		return 0, ctx.Err()
	}, async.TaskOptions{})
	worker.Lease = 100 * time.Millisecond
	worker.Heartbeat = 10 * time.Millisecond
	result, err := task.Delay(ctx, client, 1)
	if err != nil {
		t.Fatal(err)
	}
	delivery := take(t, backend)
	done := make(chan error, 1)
	go func() { done <- worker.Process(ctx, delivery) }()
	<-started
	if err := result.Revoke(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("handler did not observe revocation")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	record, err := result.Snapshot(ctx)
	if err != nil || record.State != async.Revoked {
		t.Fatal(record, err)
	}
}
