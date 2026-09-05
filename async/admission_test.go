package async_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	"github.com/Newton-School/gogo/core/ratelimit"
)

func TestWorkerAndTaskConcurrencyBoundCustomConsumers(t *testing.T) {
	for _, taskQuota := range []int{0, 1} {
		t.Run(string(rune('0'+taskQuota)), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			gate := make(chan struct{})
			started := make(chan struct{}, 8)
			var active, highest atomic.Int64
			task, client, worker, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) {
				n := active.Add(1)
				defer active.Add(-1)
				for old := highest.Load(); n > old && !highest.CompareAndSwap(old, n); old = highest.Load() {
				}
				started <- struct{}{}
				<-gate
				return 1, nil
			}, async.TaskOptions{PerWorkerConcurrency: taskQuota})
			worker.Concurrency = 2
			var deliveries []async.Delivery
			for i := 0; i < 8; i++ {
				if _, err := task.Delay(ctx, client, i); err != nil {
					t.Fatal(err)
				}
				deliveries = append(deliveries, take(t, backend))
			}
			var wg sync.WaitGroup
			for _, delivery := range deliveries {
				wg.Go(func() {
					if err := worker.Process(ctx, delivery); err != nil {
						t.Error(err)
					}
				})
			}
			want := 2
			if taskQuota > 0 {
				want = taskQuota
			}
			for range want {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			select {
			case <-started:
				t.Error("handler started without an available quota")
			case <-time.After(20 * time.Millisecond):
			}
			close(gate)
			wg.Wait()
			if highest.Load() != int64(want) {
				t.Fatal("wrong maximum active handlers", highest.Load(), want)
			}
		})
	}
}

type unavailableLimiter struct{}

func (unavailableLimiter) Allow(context.Context, string, ratelimit.Limit, int) (ratelimit.Decision, error) {
	return ratelimit.Decision{}, async.ErrUnavailable
}

func TestRateAdmissionDoesNotClaimRetryOrHideProviderFailure(t *testing.T) {
	ctx := context.Background()
	for _, scope := range []string{"worker", "distributed"} {
		t.Run(scope, func(t *testing.T) {
			calls := 0
			task, client, worker, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) { calls++; return 1, nil }, async.TaskOptions{Rate: &async.TaskRate{Scope: scope, Limit: ratelimit.Limit{Rate: 1, Burst: 1, Period: time.Hour}}})
			if scope == "distributed" {
				worker.RateLimiter = unavailableLimiter{}
			} else {
				if _, err := task.Delay(ctx, client, 0); err != nil {
					t.Fatal(err)
				}
				if err := worker.Process(ctx, take(t, backend)); err != nil {
					t.Fatal(err)
				}
			}
			result, err := task.Delay(ctx, client, 1)
			if err != nil {
				t.Fatal(err)
			}
			delivery := take(t, backend)
			limited, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			defer cancel()
			err = worker.Process(limited, delivery)
			if scope == "worker" && !errors.Is(err, context.DeadlineExceeded) || scope == "distributed" && !errors.Is(err, async.ErrUnavailable) {
				t.Fatal(err)
			}
			record, err := result.Snapshot(ctx)
			if err != nil || record.State != async.Queued || record.Envelope.Retries != 0 || record.DeliveryCount != 0 {
				t.Fatal(record, err)
			}
			stats, err := backend.Inspect(ctx, []string{"default"})
			if err != nil || stats[0].Pending != 1 {
				t.Fatal(stats, err)
			}
			if err := result.Revoke(ctx); err != nil {
				t.Fatal(err)
			}
			if err := worker.Process(ctx, delivery); err != nil {
				t.Fatal("revoked delivery unnecessarily waited for quota", err)
			}
			want := 0
			if scope == "worker" {
				want = 1
			}
			if calls != want {
				t.Fatal("rate denied handler ran", calls, want)
			}
		})
	}
}
