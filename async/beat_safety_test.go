package async_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
)

func TestCrontabLargeStepCannotWrapIntoAdditionalMinutes(t *testing.T) {
	step := strconv.Itoa(int(^uint(0) >> 1))
	rule, err := async.Crontab("59/"+step+" * * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	next, err := rule.Next(after)
	if err != nil || !next.Equal(after.Add(59*time.Minute)) {
		t.Fatal("cron step overflow created extra occurrences", next, err)
	}
}

func TestScheduleRulesRejectUnrepresentableInstantsAndOversizedCron(t *testing.T) {
	if _, err := async.Crontab(strings.Repeat("0,", 512)+"0 * * * *", "UTC"); !errors.Is(err, async.ErrInvalid) {
		t.Fatal("oversized cron was accepted", err)
	}
	cron, err := async.Crontab("* * * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []async.ScheduleRule{async.Every(time.Minute), cron} {
		for _, after := range []time.Time{
			{}, time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(9999, 12, 31, 23, 59, 30, 0, time.UTC),
		} {
			next, err := rule.Next(after)
			if !errors.Is(err, async.ErrInvalid) || !next.IsZero() {
				t.Fatal(rule.Kind, after, next, err)
			}
		}
		after := time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
		next, err := rule.Next(after)
		if err != nil || !next.Equal(after.Add(time.Minute)) {
			t.Fatal("representable year zero rejected", rule.Kind, next, err)
		}
	}
}

type beatCalendarFunc func(async.ScheduleRule, time.Time) (time.Time, error)

func (f beatCalendarFunc) Next(rule async.ScheduleRule, after time.Time) (time.Time, error) {
	return f(rule, after)
}

type beatOccurrenceSpy struct {
	async.PeriodicStore // Unused provider methods must never be reached by this tick.
	schedule            async.PeriodicSchedule
	leases, commits     int
	next                time.Time
	intents             []async.Intent
}

func (s *beatOccurrenceSpy) LeaseSchedules(context.Context, string, int, time.Duration) ([]async.PeriodicSchedule, error) {
	s.leases++
	return []async.PeriodicSchedule{s.schedule}, nil
}

func (s *beatOccurrenceSpy) CommitOccurrence(_ context.Context, _ async.PeriodicSchedule, next time.Time, _ string, intents []async.Intent) error {
	s.commits++
	s.next, s.intents = next, intents
	return nil
}

func beatSafetyFixture(t *testing.T, authorize func(context.Context, string, string, string) error) (*async.Beat, *beatOccurrenceSpy) {
	t.Helper()
	client, _, signature := producerFixture(t, func(c *async.ClientConfig) { c.Authorize = authorize })
	rule := async.Every(time.Minute)
	rule.Kind, rule.Interval = "custom", 0
	due := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	store := &beatOccurrenceSpy{schedule: async.PeriodicSchedule{
		ID: async.StableID("beat-safety", t.Name()), Signature: signature, Rule: rule, Enabled: true,
		NextDue: due, ObservedAt: due, Misfire: "coalesce", CatchUpLimit: 1, Overlap: "allow",
		Owner: "beat", Revision: 1, Fence: 1,
	}}
	return &async.Beat{Client: client, Store: store, ID: "beat", Calendars: map[string]async.Calendar{}}, store
}

func TestBeatRejectsInvalidCalendarBeforeAuthorizationOrOccurrenceWrite(t *testing.T) {
	for _, scenario := range []string{"same", "earlier", "zero", "year_negative", "year_overflow", "error", "panic", "cancel", "cancel_panic", "missing", "observed_zero", "due_overflow"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			authorizations := 0
			beat, store := beatSafetyFixture(t, func(context.Context, string, string, string) error { authorizations++; return nil })
			want := async.ErrInvalid
			if scenario == "error" || scenario == "panic" || scenario == "missing" {
				want = async.ErrUnavailable
			}
			if scenario == "cancel" || scenario == "cancel_panic" {
				want = context.Canceled
			}
			beat.Calendars["custom"] = beatCalendarFunc(func(_ async.ScheduleRule, after time.Time) (time.Time, error) {
				switch scenario {
				case "same":
					return after, nil
				case "earlier":
					return after.Add(-time.Minute), nil
				case "zero":
					return time.Time{}, nil
				case "year_negative":
					return time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC), nil
				case "year_overflow":
					return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), nil
				case "error":
					return after.Add(time.Minute), async.ErrUnavailable
				case "panic":
					panic("private calendar failure")
				case "cancel", "cancel_panic":
					cancel()
					if scenario == "cancel_panic" {
						panic("private canceled calculation")
					}
				}
				return after.Add(time.Minute), nil
			})
			switch scenario {
			case "missing":
				delete(beat.Calendars, "custom")
			case "observed_zero":
				store.schedule.ObservedAt = time.Time{}
			case "due_overflow":
				store.schedule.NextDue = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			if err := beat.Tick(ctx); !errors.Is(err, want) || store.commits != 0 || authorizations != 0 {
				t.Fatal("unsafe calculation reached dispatch preparation", err, store.commits, authorizations)
			}
		})
	}
}

func TestBeatRejectsLaterInvalidCalendarCalculationWithoutPartialOccurrence(t *testing.T) {
	for _, mode := range []string{"fixed_delay", "coalesce", "skip", "catchup"} {
		t.Run(mode, func(t *testing.T) {
			beat, store := beatSafetyFixture(t, nil)
			store.schedule.ObservedAt = store.schedule.NextDue.Add(10 * time.Minute)
			store.schedule.CatchUpLimit = 10
			if mode == "fixed_delay" {
				store.schedule.Rule.FixedDelay = true
			} else {
				store.schedule.Misfire = mode
			}
			calls := 0
			beat.Calendars["custom"] = beatCalendarFunc(func(_ async.ScheduleRule, after time.Time) (time.Time, error) {
				calls++
				if calls == 1 {
					return after.Add(time.Minute), nil
				}
				return after, nil
			})
			if err := beat.Tick(context.Background()); !errors.Is(err, async.ErrInvalid) || calls != 2 || store.commits != 0 {
				t.Fatal(mode, err, calls, store.commits)
			}
		})
	}
}

func TestBeatNormalizesCalendarOccurrenceAndChecksFinalCancellation(t *testing.T) {
	for _, cancelOnAuthorization := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		beat, store := beatSafetyFixture(t, func(context.Context, string, string, string) error {
			if cancelOnAuthorization {
				cancel()
			}
			return nil
		})
		beat.Calendars["custom"] = beatCalendarFunc(func(_ async.ScheduleRule, after time.Time) (time.Time, error) {
			return after.Add(time.Hour).In(time.FixedZone("calendar", 19800)), nil
		})
		err := beat.Tick(ctx)
		cancel()
		if cancelOnAuthorization {
			if !errors.Is(err, context.Canceled) || store.commits != 0 {
				t.Fatal(err, store.commits)
			}
		} else if err != nil || store.commits != 1 || len(store.intents) != 1 || store.next.Location() != time.UTC || !store.next.Equal(store.schedule.NextDue.Add(time.Hour)) {
			t.Fatal(err, store.commits, store.next, len(store.intents))
		}
	}
	beat, store := beatSafetyFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := beat.Tick(ctx); !errors.Is(err, context.Canceled) || store.leases != 0 {
		t.Fatal(err, store.leases)
	}
	if err := beat.Tick(nil); !errors.Is(err, async.ErrInvalid) || store.leases != 0 {
		t.Fatal(err, store.leases)
	}
}
