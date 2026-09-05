package redis_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
)

type periodicCalendarFunc func(async.ScheduleRule, time.Time) (time.Time, error)

func (f periodicCalendarFunc) Next(rule async.ScheduleRule, after time.Time) (time.Time, error) {
	return f(rule, after)
}

type countedPeriodicStore struct {
	*adapter.Schedules
	commits int
}

func (s *countedPeriodicStore) CommitOccurrence(ctx context.Context, schedule async.PeriodicSchedule, next time.Time, id string, intents []async.Intent) error {
	s.commits++
	return s.Schedules.CommitOccurrence(ctx, schedule, next, id, intents)
}

func TestRealRedisBeatInvalidCalendarPreservesOccurrenceForLeaseRecovery(t *testing.T) {
	for _, scenario := range []string{"nonadvancing", "overflow", "panic", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			broker, results := backends(t)
			store := &countedPeriodicStore{Schedules: &adapter.Schedules{Connection: results.Connection}}
			registry := async.NewRegistry()
			task, err := async.Register(registry, "test.calendar", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{})
			if err != nil {
				t.Fatal(err)
			}
			client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Schedules: store})
			if err != nil {
				t.Fatal(err)
			}
			signature, err := task.Signature(7)
			if err != nil {
				t.Fatal(err)
			}
			rule := async.Every(time.Hour)
			rule.Kind, rule.Interval = "custom", 0
			due := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
			schedule := async.PeriodicSchedule{ID: async.StableID("beat-safety", t.Name()), Signature: signature, Rule: rule,
				Enabled: true, NextDue: due, Misfire: "coalesce", CatchUpLimit: 1, Overlap: "allow", Revision: 1}
			if err := store.UpsertSchedule(ctx, schedule, 0); err != nil {
				t.Fatal(err)
			}
			tickCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			beat := &async.Beat{Client: client, Store: store, ID: "first-beat", Lease: 50 * time.Millisecond,
				Calendars: map[string]async.Calendar{"custom": periodicCalendarFunc(func(_ async.ScheduleRule, after time.Time) (time.Time, error) {
					switch scenario {
					case "overflow":
						return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), nil
					case "panic":
						panic("private calendar panic")
					case "cancel":
						cancel()
						return after.Add(time.Hour), nil
					default:
						return after, nil
					}
				})}}
			want := async.ErrInvalid
			if scenario == "panic" {
				want = async.ErrUnavailable
			}
			if scenario == "cancel" {
				want = context.Canceled
			}
			if err := beat.Tick(tickCtx); !errors.Is(err, want) || store.commits != 0 {
				t.Fatal(err, store.commits)
			}
			list, err := store.List(ctx, 10)
			if err != nil || len(list) != 1 || !list[0].NextDue.Equal(due) || list[0].LastTaskID != "" {
				t.Fatal(list, err)
			}
			if intents, err := store.ListIntents(ctx, 10); err != nil || len(intents) != 0 {
				t.Fatal(intents, err)
			}
			// The failed calculation does not release its provider lease. A new
			// owner recovers the same occurrence after that short lease expires.
			beat.ID, beat.Lease = "replacement-beat", time.Second
			beat.Calendars["custom"] = periodicCalendarFunc(func(_ async.ScheduleRule, after time.Time) (time.Time, error) { return after.Add(time.Hour), nil })
			recoveryCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			defer stop()
			for store.commits == 0 {
				if err := beat.Tick(recoveryCtx); err != nil {
					t.Fatal(err)
				}
				if store.commits == 0 {
					select {
					case <-time.After(10 * time.Millisecond):
					case <-recoveryCtx.Done():
						t.Fatal(recoveryCtx.Err())
					}
				}
			}
			intents, err := store.ListIntents(ctx, 10)
			if err != nil || len(intents) != 1 || intents[0].Envelope.ID != async.StableID(schedule.ID, due.Format(time.RFC3339Nano)) {
				t.Fatal(intents, err)
			}
			list, err = store.List(ctx, 10)
			if err != nil || len(list) != 1 || !list[0].NextDue.Equal(due.Add(time.Hour)) {
				t.Fatal(list, err)
			}
		})
	}
}
