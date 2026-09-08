package redis_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
)

type producerRetryBroker struct {
	async.Broker
	calls int
	lost  bool
}

func (b *producerRetryBroker) Publish(ctx context.Context, envelope async.Envelope) error {
	b.calls++
	if err := b.Broker.Publish(ctx, envelope); err != nil {
		return err
	}
	if b.lost && b.calls == 1 {
		return io.ErrUnexpectedEOF // Actual write applied; acknowledgement lost.
	}
	return nil
}

type producerRetryResults struct {
	async.ResultStore
	calls int
}

func (r *producerRetryResults) Register(ctx context.Context, envelope async.Envelope, state async.State) error {
	r.calls++
	if err := r.ResultStore.Register(ctx, envelope, state); err != nil {
		return err
	}
	if r.calls == 1 {
		return io.ErrUnexpectedEOF
	}
	return nil
}

type producerRetrySchedule struct {
	async.ScheduleStore
	calls int
	now   *time.Time
}

func (s *producerRetrySchedule) Schedule(ctx context.Context, envelope async.Envelope) error {
	s.calls++
	if err := s.ScheduleStore.Schedule(ctx, envelope); err != nil {
		return err
	}
	if s.calls == 1 {
		*s.now = envelope.ETA.Add(time.Second)
		return io.ErrUnexpectedEOF
	}
	return nil
}

func TestRealRedisProducerRetryAppliedWriteAndRegistrationRepair(t *testing.T) {
	for _, mode := range []string{"publish reply lost", "register reply lost", "manual confirmed repair"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			broker, results := backends(t)
			registry := async.NewRegistry()
			executions := 0
			task, err := async.Register(registry, "producer.native", 1, func(_ context.Context, tc async.TaskContext, value int) (int, error) {
				executions++
				if tc.Retries != 0 {
					t.Fatal("producer changed execution retry counter")
				}
				return value + 1, nil
			}, async.TaskOptions{})
			if err != nil {
				t.Fatal(err)
			}
			transport := &producerRetryBroker{Broker: broker, lost: mode == "publish reply lost"}
			projection := &producerRetryResults{ResultStore: results}
			cfg := async.ClientConfig{Registry: registry, Broker: transport, Results: results,
				PublishRetry: &async.PublishRetryPolicy{InitialDelay: time.Millisecond, MaxDelay: time.Millisecond, DisableJitter: true,
					Retryable: func(err error) bool { return errors.Is(err, io.ErrUnexpectedEOF) }}}
			if mode != "publish reply lost" {
				cfg.Results = projection
			}
			if mode == "manual confirmed repair" {
				cfg.PublishRetry = nil
			}
			client, err := async.NewClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			result, err := task.Delay(ctx, client, 4)
			if mode == "manual confirmed repair" {
				var acceptance *async.AcceptanceError
				if !errors.As(err, &acceptance) || !acceptance.Confirmed {
					t.Fatal(err)
				}
				receipt, retryErr := acceptance.Retry(ctx, client)
				if retryErr != nil || receipt.ID != acceptance.ID {
					t.Fatal(receipt, retryErr)
				}
				result = async.RestoreResult[int](client, receipt.ID)
			} else if err != nil {
				t.Fatal(err)
			}
			stats, err := broker.Inspect(ctx, []string{"default"})
			if err != nil || stats[0].Queued != 1 {
				t.Fatal("same-ID replay created another stream entry", stats, err)
			}
			wantCalls := 1
			if mode == "publish reply lost" {
				wantCalls = 2
			}
			if transport.calls != wantCalls || mode != "publish reply lost" && projection.calls != 2 {
				t.Fatal(transport.calls, projection.calls)
			}
			worker := &async.Worker{ID: "producer-native", Registry: registry, Broker: broker, Results: results}
			if err := worker.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if value, err := result.Get(ctx); err != nil || value != 5 || executions != 1 {
				t.Fatal(value, err, executions)
			}
		})
	}
}

func TestRealRedisProducerRetryScheduleRemainsScheduledAcrossETA(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	registry := async.NewRegistry()
	task, err := async.Register(registry, "producer.scheduled", 1, func(_ context.Context, _ async.TaskContext, value int) (int, error) { return value, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-2 * time.Second).UTC()
	schedules := &adapter.Schedules{Connection: results.Connection}
	transport := &producerRetryBroker{Broker: broker}
	delayed := &producerRetrySchedule{ScheduleStore: schedules, now: &now}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: transport, Results: results, Schedules: delayed,
		Clock: func() time.Time { return now }, PublishRetry: &async.PublishRetryPolicy{InitialDelay: time.Millisecond, MaxDelay: time.Millisecond,
			Retryable: func(err error) bool { return errors.Is(err, io.ErrUnexpectedEOF) }}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := task.Delay(ctx, client, 4, async.WithCountdown(time.Second))
	if err != nil || result.Receipt.State != async.Scheduled || delayed.calls != 2 || transport.calls != 0 {
		t.Fatal(result, err, delayed.calls, transport.calls)
	}
	items, err := schedules.LeaseDue(ctx, "producer-native", 2, time.Minute)
	if err != nil || len(items) != 1 || items[0].Envelope.ID != result.Receipt.ID || items[0].Envelope.Retries != 0 {
		t.Fatal(items, err)
	}
	stats, err := broker.Inspect(ctx, []string{"default"})
	if err != nil || stats[0].Queued != 0 {
		t.Fatal("retry bypassed delayed dispatcher", stats, err)
	}
}
