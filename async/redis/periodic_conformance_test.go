package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
	fakes "github.com/Newton-School/gogo/async/testing"
	redigo "github.com/redis/go-redis/v9"
)

type periodicConformanceBackend struct {
	store   async.PeriodicStore
	now     time.Time
	advance func(time.Time)
}

func newPeriodicConformanceBackend(t *testing.T, native bool) periodicConformanceBackend {
	t.Helper()
	if native {
		_, results := backends(t)
		now, err := results.Connection.Client().Time(context.Background()).Result()
		if err != nil {
			t.Fatal(err)
		}
		return periodicConformanceBackend{store: &adapter.Schedules{Connection: results.Connection}, now: now, advance: func(until time.Time) {
			deadline := time.Now().Add(5 * time.Second)
			for {
				now, err := results.Connection.Client().Time(context.Background()).Result()
				if err != nil {
					t.Fatal(err)
				}
				if !now.Truncate(time.Millisecond).Before(until) {
					return
				}
				if time.Now().After(deadline) {
					t.Fatal("Redis lease clock did not advance")
				}
				time.Sleep(5 * time.Millisecond)
			}
		}}
	}
	now := time.Date(2026, 4, 5, 6, 7, 8, 123456789, time.UTC)
	memory := fakes.NewMemory()
	memory.Clock = func() time.Time { return now }
	return periodicConformanceBackend{store: memory, now: now, advance: func(until time.Time) { now = until }}
}

func conformanceSchedule(t *testing.T, now time.Time) async.PeriodicSchedule {
	return async.PeriodicSchedule{ID: async.StableID(t.Name(), "periodic"), Signature: async.Signature{Task: "test.periodic", Version: 1, Args: json.RawMessage(`9007199254740993`)},
		Rule: async.Every(time.Minute + time.Nanosecond), Enabled: true, NextDue: now.Add(-time.Minute), Misfire: "coalesce", CatchUpLimit: 1, Overlap: "allow", Revision: 1}
}

func conformanceIntent(p async.PeriodicSchedule) async.Intent {
	id := async.StableID(p.ID, p.NextDue.UTC().Format(time.RFC3339Nano))
	e := async.Envelope{ProtocolVersion: 1, ID: id, Task: p.Signature.Task, Version: 1, Args: append(json.RawMessage(nil), p.Signature.Args...), CreatedAt: p.NextDue, Queue: "default"}
	return async.Intent{ID: async.StableID(p.ID, "dispatch-"+id), SourceID: p.ID, Kind: "publish", Envelope: &e}
}

func TestPeriodicStoreMemoryRedisConformance(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "redis"}[native], func(t *testing.T) {
			ctx := context.Background()
			f := newPeriodicConformanceBackend(t, native)
			p := conformanceSchedule(t, f.now)
			if err := f.store.UpsertSchedule(ctx, p, 0); err != nil {
				t.Fatal(err)
			}
			if err := f.store.UpsertSchedule(ctx, p, 0); !errors.Is(err, async.ErrConflict) {
				t.Fatal(err)
			}
			leased, err := f.store.LeaseSchedules(ctx, "first", 1, time.Second+900*time.Microsecond)
			if err != nil || len(leased) != 1 {
				t.Fatal(leased, err)
			}
			first := leased[0]
			if first.Revision != 2 || first.Fence != 1 || first.Rule.Interval != p.Rule.Interval || !first.NextDue.Equal(p.NextDue) || first.ObservedAt.Nanosecond()%1e6 != 0 || !first.LeaseUntil.Equal(first.ObservedAt.Add(time.Second)) {
				t.Fatal(first)
			}
			if leased, err := f.store.LeaseSchedules(ctx, "second", 1, time.Second); err != nil || len(leased) != 0 {
				t.Fatal(leased, err)
			}
			f.advance(first.LeaseUntil)
			leased, err = f.store.LeaseSchedules(ctx, "second", 1, 2*time.Second)
			if err != nil || len(leased) != 1 || leased[0].Revision != 3 || leased[0].Fence != 2 {
				t.Fatal(leased, err)
			}
			intent := conformanceIntent(p)
			next := f.now.Add(time.Hour)
			if err := f.store.CommitOccurrence(ctx, first, next, intent.Envelope.ID, []async.Intent{intent}); !errors.Is(err, async.ErrLeaseLost) {
				t.Fatal(err)
			}
			bad := intent
			bad.SourceID = async.StableID("foreign", "schedule")
			if err := f.store.CommitOccurrence(ctx, leased[0], next, intent.Envelope.ID, []async.Intent{intent, bad}); !errors.Is(err, async.ErrInvalid) {
				t.Fatal(err)
			}
			before, _ := f.store.List(ctx, 1)
			if before[0].Revision != 3 || !before[0].NextDue.Equal(p.NextDue) {
				t.Fatal(before)
			}
			if err := f.store.CommitOccurrence(ctx, leased[0], next, intent.Envelope.ID, []async.Intent{intent}); err != nil {
				t.Fatal(err)
			}
			list, err := f.store.List(ctx, 1)
			pending, pendingErr := f.store.ListIntents(ctx, 10)
			if err != nil || pendingErr != nil || len(list) != 1 || len(pending) != 1 || list[0].Revision != 4 || list[0].Owner != "" || !list[0].LeaseUntil.IsZero() || !list[0].ObservedAt.IsZero() || !list[0].NextDue.Equal(next) || string(pending[0].Envelope.Args) != `9007199254740993` {
				t.Fatal(list, pending, err, pendingErr)
			}
			// A retry of the stale commit is refused; the old durable occurrence
			// remains, and disabling future work cannot erase that intent.
			if err := f.store.CommitOccurrence(ctx, leased[0], next, intent.Envelope.ID, []async.Intent{intent}); !errors.Is(err, async.ErrLeaseLost) {
				t.Fatal(err)
			}
			if err := f.store.Disable(ctx, p.ID, 3); !errors.Is(err, async.ErrConflict) {
				t.Fatal(err)
			}
			if err := f.store.Disable(ctx, p.ID, 4); err != nil {
				t.Fatal(err)
			}
			if pending, err := f.store.ListIntents(ctx, 10); err != nil || len(pending) != 1 || pending[0].ID != intent.ID {
				t.Fatal(pending, err)
			}
		})
	}
}

func TestPeriodicStoreDocumentedSimulatorDifferences(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "redis"}[native], func(t *testing.T) {
			ctx := context.Background()
			f := newPeriodicConformanceBackend(t, native)
			p := conformanceSchedule(t, f.now)
			err := f.store.Disable(ctx, p.ID, 0)
			wantMissing := async.ErrNotFound
			if native {
				wantMissing = redigo.Nil
			}
			if !errors.Is(err, wantMissing) {
				t.Fatal(err, wantMissing)
			}
			if err := f.store.UpsertSchedule(ctx, p, 0); err != nil {
				t.Fatal(err)
			}
			_, err = f.store.LeaseSchedules(ctx, strings.Repeat("x", 257), 1, time.Second)
			if native {
				if err != nil {
					t.Fatal(err)
				}
				list, _ := f.store.List(ctx, 1)
				f.advance(list[0].LeaseUntil)
			} else if !errors.Is(err, async.ErrInvalid) {
				t.Fatal(err)
			}
			lease, err := f.store.LeaseSchedules(ctx, "beat", 1, 2*time.Second)
			if err != nil || len(lease) != 1 {
				t.Fatal(lease, err)
			}
			intent := conformanceIntent(p)
			if err := f.store.CommitOccurrence(ctx, lease[0], f.now.Add(-time.Second), intent.Envelope.ID, []async.Intent{intent}); err != nil {
				t.Fatal(err)
			}
			lease, err = f.store.LeaseSchedules(ctx, "beat", 1, 2*time.Second)
			if err != nil || len(lease) != 1 {
				t.Fatal(lease, err)
			}
			intent.Envelope.Args = json.RawMessage(`2`)
			err = f.store.CommitOccurrence(ctx, lease[0], f.now.Add(time.Hour), intent.Envelope.ID, []async.Intent{intent})
			if native {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, async.ErrConflict) {
				t.Fatal(err)
			}
			pending, err := f.store.ListIntents(ctx, 10)
			if err != nil || len(pending) != 1 || string(pending[0].Envelope.Args) != `9007199254740993` {
				t.Fatal("retained intent was overwritten", pending, err)
			}
		})
	}
}
