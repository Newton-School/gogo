package redis_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
)

func TestRealRedisReclaimLimitCapsConfiguredBatch(t *testing.T) {
	ctx := context.Background()
	broker, _ := backends(t)
	broker.ReclaimBatch = 20
	for range 3 {
		if err := broker.Publish(ctx, taskEnvelope(t)); err != nil {
			t.Fatal(err)
		}
		delivery, err := broker.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "original"})
		if err != nil {
			t.Fatal(err)
		}
		stream, id, _ := strings.Cut(delivery.Receipt, "\x00")
		if err := broker.Connection.Client().Do(ctx, "XCLAIM", stream, "gogo-workers", "original", 0, id, "IDLE", 120000).Err(); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 8 && len(seen) < 3; i++ {
		deliveries, err := broker.Reclaim(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "new", ReclaimLimit: 1}, time.Minute)
		if err != nil || len(deliveries) > 1 {
			t.Fatal(len(deliveries), err)
		}
		for _, delivery := range deliveries {
			if seen[delivery.Receipt] {
				t.Fatal("reclaimed live repeated reservation")
			}
			seen[delivery.Receipt] = true
		}
	}
	if len(seen) != 3 {
		t.Fatal("bounded batches starved pending receipts", len(seen))
	}
	for _, limit := range []int{-1, 1001} {
		if _, err := broker.Reclaim(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "new", ReclaimLimit: limit}, time.Minute); !errors.Is(err, async.ErrInvalid) {
			t.Fatal("invalid limit accepted", err)
		}
	}
}

func TestRealRedisAutoscaleRunsWithinConfiguredConcurrency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	broker, results := backends(t)
	broker.ReclaimBatch = 20 // Runner-specific reclaim remains capped at one.
	registry := async.NewRegistry()
	var active, highest atomic.Int64
	started := make(chan struct{}, 3)
	gate := make(chan struct{})
	task, err := async.Register(registry, "pool.run", 1, func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
		count := active.Add(1)
		defer active.Add(-1)
		for old := highest.Load(); count > old && !highest.CompareAndSwap(old, count); old = highest.Load() {
		}
		started <- struct{}{}
		select {
		case <-gate:
			return n + 1, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results})
	if err != nil {
		t.Fatal(err)
	}
	var handles []*async.Result[int]
	for n := 0; n < 3; n++ {
		handle, err := task.Delay(ctx, client, n)
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, handle)
	}
	worker := &async.Worker{ID: "pool-worker", Registry: registry, Broker: broker, Results: results, Autoscale: &async.AutoscaleOptions{Min: 1, Max: 3, Interval: 10 * time.Millisecond, IdleTimeout: 30 * time.Millisecond}}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for range 3 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("pool failed to grow", ctx.Err())
		}
	}
	if highest.Load() != 3 {
		t.Fatal("unexpected active handler count", highest.Load())
	}
	close(gate)
	for i, result := range handles {
		out, err := result.Get(ctx)
		if err != nil || out != i+1 {
			t.Fatal(out, err)
		}
	}
}
