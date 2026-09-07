package async_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

// This broker keeps empty Consume calls blocked, making reservation loops
// observable independently of accepted handlers and the global slot channel.
type observedPoolBroker struct {
	async.Broker
	consuming atomic.Int64
	highest   atomic.Int64
	calls     atomic.Int64
}

func (b *observedPoolBroker) Consume(ctx context.Context, options async.ConsumeOptions) (async.Delivery, error) {
	b.calls.Add(1)
	n := b.consuming.Add(1)
	defer b.consuming.Add(-1)
	for old := b.highest.Load(); n > old && !b.highest.CompareAndSwap(old, n); old = b.highest.Load() {
	}
	if options.Wait == 0 {
		return b.Broker.Consume(ctx, options)
	}
	timer := time.NewTimer(options.Wait)
	defer timer.Stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		delivery, err := b.Broker.Consume(ctx, options)
		if !errors.Is(err, async.ErrNotFound) {
			return delivery, err
		}
		select {
		case <-ctx.Done():
			return async.Delivery{}, ctx.Err()
		case <-timer.C:
			return async.Delivery{}, async.ErrNotFound
		case <-ticker.C:
		}
	}
}

func waitPool(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatal("worker pool did not reach expected state")
		case <-ticker.C:
		}
	}
}

func TestAutoscaleIdleReservationsStayAtMinimum(t *testing.T) {
	_, _, worker, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) {
		t.Error("empty queue executed")
		return 0, nil
	}, async.TaskOptions{})
	broker := &observedPoolBroker{Broker: backend}
	worker.Broker = broker
	worker.Autoscale = &async.AutoscaleOptions{Min: 2, Max: 5, Interval: 10 * time.Millisecond, IdleTimeout: 30 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	waitPool(t, func() bool { return broker.consuming.Load() == 2 })
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if broker.highest.Load() != 2 || broker.consuming.Load() != 2 || worker.Snapshot().Concurrency != 5 {
		t.Fatal("idle Consume calls were treated as processing load", broker.highest.Load(), broker.consuming.Load(), worker.Snapshot().Concurrency)
	}
}

func TestAutoscaleOptionsAreCopiedAndNilKeepsFixedPool(t *testing.T) {
	for _, scaled := range []bool{false, true} {
		name := "fixed"
		if scaled {
			name = "copied options"
		}
		t.Run(name, func(t *testing.T) {
			_, _, worker, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) { return 0, nil }, async.TaskOptions{})
			broker := &observedPoolBroker{Broker: backend}
			worker.Broker = broker
			wantLoops, wantCap := int64(2), 2
			if scaled {
				worker.Autoscale = &async.AutoscaleOptions{Min: 1, Max: 3, Interval: 10 * time.Millisecond, IdleTimeout: 30 * time.Millisecond}
				wantLoops, wantCap = 1, 3
			} else {
				worker.Concurrency = 2
			}
			if err := worker.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if scaled {
				// Initialization already copied these fields. No concurrent
				// mutation of live public worker configuration is performed.
				worker.Autoscale.Min = 2
				worker.Autoscale.Max = 1024
				worker.Autoscale.Interval = -time.Second
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- worker.Run(ctx) }()
			t.Cleanup(func() { cancel(); <-done })
			waitPool(t, func() bool { return broker.consuming.Load() == wantLoops })
			<-time.After(100 * time.Millisecond)
			if broker.highest.Load() != wantLoops || worker.Snapshot().Concurrency != wantCap {
				t.Fatal("configuration changed pool after initialization", broker.highest.Load(), worker.Snapshot().Concurrency)
			}
		})
	}
}

type canceledLateBroker struct {
	async.Broker
	cancel context.CancelFunc
	writes *atomic.Int64
}

func (b canceledLateBroker) Consume(ctx context.Context, options async.ConsumeOptions) (async.Delivery, error) {
	delivery, err := b.Broker.Consume(ctx, options)
	if err != nil {
		return delivery, err
	}
	b.cancel()
	delivery.Body = []byte("malformed late delivery")
	return delivery, nil
}

func (b canceledLateBroker) Ack(ctx context.Context, delivery async.Delivery) error {
	b.writes.Add(1)
	return b.Broker.Ack(ctx, delivery)
}

func (b canceledLateBroker) Reject(ctx context.Context, delivery async.Delivery, reason string, requeue bool) error {
	b.writes.Add(1)
	return b.Broker.Reject(ctx, delivery, reason, requeue)
}

type countedLateResults struct {
	async.ResultStore
	writes *atomic.Int64
}

func (r countedLateResults) Claim(ctx context.Context, envelope async.Envelope, owner string, lease time.Duration) (async.Claim, error) {
	r.writes.Add(1)
	return r.ResultStore.Claim(ctx, envelope, owner, lease)
}

func TestRunOnceCanceledLateDeliveryDoesNotRejectAckOrClaim(t *testing.T) {
	for _, scaled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		task, client, worker, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) {
			t.Error("canceled late delivery invoked handler")
			return 0, nil
		}, async.TaskOptions{})
		if scaled {
			worker.Autoscale = &async.AutoscaleOptions{Min: 1, Max: 2}
		}
		if _, err := task.Delay(ctx, client, 1); err != nil {
			cancel()
			t.Fatal(err)
		}
		var writes atomic.Int64
		worker.Broker = canceledLateBroker{Broker: backend, cancel: cancel, writes: &writes}
		worker.Results = countedLateResults{ResultStore: backend, writes: &writes}
		if err := worker.RunOnce(ctx); !errors.Is(err, context.Canceled) {
			cancel()
			t.Fatal(err)
		}
		cancel()
		stats, err := backend.Inspect(context.Background(), []string{"default"})
		if err != nil || writes.Load() != 0 || stats[0].Pending != 1 || stats[0].Quarantined != 0 {
			t.Fatal("canceled delivery mutated result or poison state", writes.Load(), stats, err)
		}
	}
}

func TestAutoscaleGrowsShrinksAndRegrowsWithinHardCap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var active, highest atomic.Int64
	started := make(chan struct{}, 16)
	release := make(chan struct{}, 16)
	task, client, worker, backend := setup(t, func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
		count := active.Add(1)
		defer active.Add(-1)
		for old := highest.Load(); count > old && !highest.CompareAndSwap(old, count); old = highest.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
			return n, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, async.TaskOptions{})
	broker := &observedPoolBroker{Broker: backend}
	worker.Broker = broker
	worker.Autoscale = &async.AutoscaleOptions{Min: 1, Max: 3, Interval: 10 * time.Millisecond, IdleTimeout: 40 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for wave := 0; wave < 2; wave++ {
		var results []*async.Result[int]
		for i := 0; i < 6; i++ {
			result, err := task.Delay(ctx, client, i)
			if err != nil {
				t.Fatal(err)
			}
			results = append(results, result)
		}
		waitPool(t, func() bool { return active.Load() == 3 })
		if len(started) != wave*6+3 {
			t.Fatal("handlers exceeded the hard cap while blocked", len(started))
		}
		for range 6 {
			release <- struct{}{}
		}
		for i, result := range results {
			readCtx, stop := context.WithTimeout(ctx, 3*time.Second)
			value, err := result.Get(readCtx)
			stop()
			if err != nil || value != i {
				t.Fatal(value, err)
			}
		}
		waitPool(t, func() bool { return broker.consuming.Load() == 1 && active.Load() == 0 })
	}
	if highest.Load() != 3 || broker.highest.Load() > 3 {
		t.Fatal("autoscale escaped its maximum", highest.Load(), broker.highest.Load())
	}
}

func TestAutoscaleAndCustomProcessShareCapacityBeforeReservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan struct{}, 4), make(chan struct{})
	task, client, worker, backend := setup(t, func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
		started <- struct{}{}
		select {
		case <-release:
			return n, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, async.TaskOptions{})
	worker.Autoscale = &async.AutoscaleOptions{Min: 1, Max: 2, Interval: 10 * time.Millisecond, IdleTimeout: 40 * time.Millisecond}
	broker := &observedPoolBroker{Broker: backend}
	worker.Broker = broker
	var callers sync.WaitGroup
	for i := 0; i < 2; i++ {
		if _, err := task.Delay(ctx, client, i); err != nil {
			t.Fatal(err)
		}
		delivery := take(t, backend)
		callers.Go(func() { _ = worker.Process(ctx, delivery) })
	}
	waitPool(t, func() bool { return len(started) == 2 })
	if _, err := task.Delay(ctx, client, 3); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); callers.Wait(); <-done })
	timer := time.NewTimer(80 * time.Millisecond)
	<-timer.C
	if broker.calls.Load() != 0 {
		t.Fatal("runner reserved while direct Process filled its capacity", broker.calls.Load())
	}
	stats, err := backend.Inspect(ctx, []string{"default"})
	if err != nil || stats[0].Queued != 1 || stats[0].Pending != 2 {
		t.Fatal(stats, err)
	}
	close(release)
	waitPool(t, func() bool { return len(started) == 3 })
}

func TestAutoscaleConfigurationFailsBeforeBrokerIO(t *testing.T) {
	for _, options := range []async.AutoscaleOptions{
		{}, {Min: 0, Max: 1}, {Min: 2, Max: 1}, {Min: 1, Max: 1025},
		{Min: 1, Max: 2, Interval: time.Millisecond}, {Min: 1, Max: 2, Interval: 2 * time.Minute},
		{Min: 1, Max: 2, Interval: time.Second, IdleTimeout: time.Millisecond},
		{Min: 1, Max: 2, IdleTimeout: 25 * time.Hour},
	} {
		_, _, worker, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) { return 0, nil }, async.TaskOptions{})
		worker.Autoscale = &options
		backend.Fail = func(string) error { t.Fatal("invalid configuration reached a provider"); return nil }
		if err := worker.Run(context.Background()); !errors.Is(err, async.ErrInvalid) {
			t.Fatal(options, err)
		}
	}
	_, _, worker, _ := setup(t, func(context.Context, async.TaskContext, int) (int, error) { return 0, nil }, async.TaskOptions{})
	worker.Concurrency, worker.Autoscale = 3, &async.AutoscaleOptions{Min: 1, Max: 2}
	if err := worker.Run(context.Background()); !errors.Is(err, async.ErrInvalid) {
		t.Fatal("conflicting hard cap accepted", err)
	}
}

type retirementDeliveryBroker struct {
	*observedPoolBroker
	late    async.Delivery
	returns atomic.Int64
}

func (b *retirementDeliveryBroker) Consume(ctx context.Context, options async.ConsumeOptions) (async.Delivery, error) {
	delivery, err := b.observedPoolBroker.Consume(ctx, options)
	if errors.Is(err, context.Canceled) {
		// Emulate a provider whose reservation won immediately before the
		// cancellation was observed and whose response arrived just afterward.
		b.returns.Add(1)
		return b.late, nil
	}
	return delivery, err
}

func TestAutoscaleRetirementKeepsLateReservationPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var started atomic.Int64
	gate := make(chan struct{})
	task, client, worker, backend := setup(t, func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
		started.Add(1)
		select {
		case <-gate:
			return n, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, async.TaskOptions{})
	late, err := task.Delay(ctx, client, 99)
	if err != nil {
		t.Fatal(err)
	}
	broker := &retirementDeliveryBroker{observedPoolBroker: &observedPoolBroker{Broker: backend}, late: take(t, backend)}
	worker.Broker = broker
	worker.Autoscale = &async.AutoscaleOptions{Min: 1, Max: 3, Interval: 10 * time.Millisecond, IdleTimeout: 30 * time.Millisecond}
	for range 3 {
		if _, err := task.Delay(ctx, client, 1); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	waitPool(t, func() bool { return started.Load() == 3 })
	close(gate)
	waitPool(t, func() bool { return broker.returns.Load() == 2 && broker.consuming.Load() == 1 })
	if started.Load() != 3 {
		t.Fatal("retired slot executed its late reservation")
	}
	record, err := late.Snapshot(ctx)
	if err != nil || record.State != async.Queued || record.DeliveryCount != 0 {
		t.Fatal("late delivery fabricated execution", record.State, err)
	}
	stats, err := backend.Inspect(ctx, []string{"default"})
	if err != nil || stats[0].Pending != 1 {
		t.Fatal("late reservation was lost instead of retained", stats, err)
	}
}

func TestAutoscaleRemoteShutdownDrainsAllAcceptedTasks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var started, canceled atomic.Int64
	gate := make(chan struct{})
	task, _, worker, backend := setup(t, func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
		started.Add(1)
		select {
		case <-gate:
			return n, nil
		case <-ctx.Done():
			canceled.Add(1)
			return 0, ctx.Err()
		}
	}, async.TaskOptions{})
	worker.Broker = &observedPoolBroker{Broker: backend}
	worker.Controls = backend
	worker.Lease, worker.Heartbeat = time.Second, 20*time.Millisecond
	worker.Autoscale = &async.AutoscaleOptions{Min: 1, Max: 3, Interval: 10 * time.Millisecond, IdleTimeout: 30 * time.Millisecond}
	client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: backend, Results: backend, Controls: backend, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	for range 9 {
		if _, err := task.Delay(ctx, client, 1); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	returned := false
	t.Cleanup(func() {
		cancel()
		if !returned {
			<-done
		}
	})
	waitPool(t, func() bool { return started.Load() == 3 })
	control := async.Control{Client: client}
	requests, err := control.PrepareWorkerShutdown(ctx, []string{worker.ID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	report, err := control.DispatchWorkerControls(ctx, requests, time.Second)
	if err != nil || !report.Complete() || report.Targets[0].Reply.Outcome != async.ControlAccepted {
		t.Fatal(report, err)
	}
	select {
	case <-done:
		returned = true
		t.Fatal("shutdown abandoned accepted handlers")
	default:
	}
	close(gate)
	if err := <-done; err != nil {
		returned = true
		t.Fatal(err)
	}
	returned = true
	if started.Load() != 3 || canceled.Load() != 0 {
		t.Fatal("remote stop canceled or accepted extra tasks", started.Load(), canceled.Load())
	}
	stats, err := backend.Inspect(ctx, []string{"default"})
	if err != nil || stats[0].Queued != 6 || stats[0].Pending != 0 {
		t.Fatal(stats, err)
	}
}

func TestAutoscalePreservesTaskAdmissionQuota(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var active, highest atomic.Int64
	gate := make(chan struct{})
	task, client, worker, backend := setup(t, func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
		count := active.Add(1)
		defer active.Add(-1)
		for old := highest.Load(); count > old && !highest.CompareAndSwap(old, count); old = highest.Load() {
		}
		select {
		case <-gate:
			return n, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, async.TaskOptions{PerWorkerConcurrency: 1})
	worker.Broker = &observedPoolBroker{Broker: backend}
	worker.Autoscale = &async.AutoscaleOptions{Min: 1, Max: 3, Interval: 10 * time.Millisecond, IdleTimeout: 30 * time.Millisecond}
	var results []*async.Result[int]
	for range 3 {
		result, err := task.Delay(ctx, client, 1)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	waitPool(t, func() bool {
		stats, err := backend.Inspect(ctx, []string{"default"})
		return err == nil && stats[0].Pending == 3
	})
	if active.Load() != 1 {
		t.Fatal("scaled pool bypassed task admission quota", active.Load())
	}
	close(gate)
	for _, result := range results {
		readCtx, stop := context.WithTimeout(ctx, time.Second)
		_, err := result.Get(readCtx)
		stop()
		if err != nil {
			t.Fatal(err)
		}
	}
	if highest.Load() != 1 {
		t.Fatal("concurrent task quota exceeded", highest.Load())
	}
}

type excessiveReclaimBroker struct {
	async.Broker
	deliveries []async.Delivery
	calls      atomic.Int64
}

func (b *excessiveReclaimBroker) Reclaim(_ context.Context, options async.ConsumeOptions, _ time.Duration) ([]async.Delivery, error) {
	b.calls.Add(1)
	if options.ReclaimLimit != 1 {
		return nil, errors.New("runner did not bound reclamation")
	}
	return b.deliveries, nil
}

func TestAutoscaleRejectsExcessiveReclaimWithoutAckOrExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var handled atomic.Int64
	task, client, worker, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) {
		handled.Add(1)
		return 0, nil
	}, async.TaskOptions{})
	broker := &excessiveReclaimBroker{Broker: backend}
	for range 2 {
		if _, err := task.Delay(ctx, client, 1); err != nil {
			t.Fatal(err)
		}
		broker.deliveries = append(broker.deliveries, take(t, backend))
	}
	worker.Broker = broker
	worker.Lease, worker.Heartbeat = time.Second, 20*time.Millisecond
	worker.Autoscale = &async.AutoscaleOptions{Min: 1, Max: 3, Interval: 10 * time.Millisecond, IdleTimeout: 30 * time.Millisecond}
	var ticks atomic.Int64
	base := time.Now()
	worker.Clock = func() time.Time { return base.Add(time.Duration(ticks.Add(1)) * time.Second) }
	reported := make(chan error, 16)
	worker.OnError = func(err error) { reported <- err }
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case err := <-reported:
		if !errors.Is(err, async.ErrUnavailable) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("oversize provider result was hidden")
	}
	if handled.Load() != 0 {
		t.Fatal("oversize reclaimed batch executed")
	}
	stats, err := backend.Inspect(ctx, []string{"default"})
	if err != nil || stats[0].Pending != 2 {
		t.Fatal("oversize reclaimed receipts were acknowledged", stats, err)
	}
}

func TestAutoscaleProviderFailureDoesNotGrowOrSpin(t *testing.T) {
	_, _, worker, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) { return 0, nil }, async.TaskOptions{})
	var calls atomic.Int64
	backend.Fail = func(op string) error {
		if op == "consume" {
			calls.Add(1)
			return async.ErrUnavailable
		}
		return nil
	}
	worker.Autoscale = &async.AutoscaleOptions{Min: 1, Max: 10, Interval: 10 * time.Millisecond, IdleTimeout: 30 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	waitPool(t, func() bool { return calls.Load() > 0 })
	<-time.After(250 * time.Millisecond)
	if count := calls.Load(); count < 2 || count > 4 {
		t.Fatal("provider failures grew the pool or spun", count)
	}
}

type queuedReclaimBroker struct {
	async.Broker
	deliveries chan async.Delivery
}

func (b *queuedReclaimBroker) Reclaim(ctx context.Context, options async.ConsumeOptions, _ time.Duration) ([]async.Delivery, error) {
	if options.ReclaimLimit != 1 {
		return nil, errors.New("missing reclaim cap")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case delivery := <-b.deliveries:
		return []async.Delivery{delivery}, nil
	default:
		return nil, nil
	}
}

func TestReservationLoopAttemptsFreshWorkAfterReclaim(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	order := make(chan int, 3)
	task, client, worker, backend := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) {
		order <- n
		return n, nil
	}, async.TaskOptions{})
	broker := &queuedReclaimBroker{Broker: backend, deliveries: make(chan async.Delivery, 2)}
	var results []*async.Result[int]
	for n := 1; n <= 3; n++ {
		result, err := task.Delay(ctx, client, n)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
		if n < 3 {
			broker.deliveries <- take(t, backend)
		}
	}
	worker.Broker = broker
	worker.Autoscale = &async.AutoscaleOptions{Min: 1, Max: 1, Interval: 10 * time.Millisecond}
	worker.Lease, worker.Heartbeat = time.Second, 20*time.Millisecond
	var ticks atomic.Int64
	base := time.Now()
	// Each loop observes more than a lease interval passing; result storage
	// keeps its ordinary independent clock and real fenced transitions.
	worker.Clock = func() time.Time { return base.Add(time.Duration(ticks.Add(1)) * time.Second) }
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for _, result := range results {
		readCtx, stop := context.WithTimeout(ctx, time.Second)
		_, err := result.Get(readCtx)
		stop()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []int{1, 3, 2} {
		if got := <-order; got != want {
			t.Fatal("fresh task starved behind reclamation", got, want)
		}
	}
}

func TestMemoryReclaimHonorsCallerBatchLimit(t *testing.T) {
	backend := fakes.NewMemory()
	now := time.Now()
	backend.Clock = func() time.Time { return now }
	registry := async.NewRegistry()
	task, err := async.Register(registry, "pool.reclaim", 1, func(context.Context, async.TaskContext, int) (int, error) { return 0, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	producer, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := task.Delay(context.Background(), producer, i); err != nil {
			t.Fatal(err)
		}
		_ = take(t, backend)
	}
	now = now.Add(time.Minute)
	for range 3 {
		deliveries, err := backend.Reclaim(context.Background(), async.ConsumeOptions{Queues: []string{"default"}, Consumer: "next", ReclaimLimit: 1}, time.Second)
		if err != nil || len(deliveries) != 1 {
			t.Fatal(len(deliveries), err)
		}
	}
	for _, limit := range []int{-1, 1001} {
		if _, err := backend.Reclaim(context.Background(), async.ConsumeOptions{ReclaimLimit: limit}, time.Second); !errors.Is(err, async.ErrInvalid) {
			t.Fatal("invalid reclaim limit accepted", err)
		}
	}
}
