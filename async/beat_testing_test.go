package async_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

type memoryBeatFixture struct {
	now    time.Time
	memory *fakes.Memory
	client *async.Client
	worker *async.Worker
	beat   *async.Beat
	page   async.PeriodicSchedule
	calls  int
}

func newMemoryBeatFixture(t *testing.T) *memoryBeatFixture {
	t.Helper()
	f := &memoryBeatFixture{now: time.Date(2026, 4, 5, 6, 7, 0, 0, time.UTC)}
	f.memory = fakes.NewMemory()
	clock := func() time.Time { return f.now }
	f.memory.Clock = clock
	registry := async.NewRegistry()
	task, err := async.Register(registry, "test.memory_periodic", 1, func(_ context.Context, tc async.TaskContext, n int64) (int64, error) {
		f.calls++
		if tc.Retries != 0 {
			t.Error("schedule occurrence consumed task retry")
		}
		return n + 1, nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.client, err = async.NewClient(async.ClientConfig{Registry: registry, Broker: f.memory, Results: f.memory, Schedules: f.memory, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	sig, err := task.Signature(9007199254740993)
	if err != nil {
		t.Fatal(err)
	}
	f.page = async.PeriodicSchedule{ID: async.StableID(t.Name(), "schedule"), Signature: sig, Rule: async.Every(time.Minute), Enabled: true,
		NextDue: f.now.Add(time.Minute), Misfire: "coalesce", CatchUpLimit: 3, Overlap: "allow", Revision: 1}
	f.beat = &async.Beat{Client: f.client, Store: f.memory, ID: "beat", Lease: time.Minute}
	f.worker = &async.Worker{Registry: registry, Broker: f.memory, Results: f.memory, ID: "worker", Clock: clock}
	return f
}

func (f *memoryBeatFixture) upsert(t *testing.T) {
	t.Helper()
	if err := f.memory.UpsertSchedule(context.Background(), f.page, 0); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryBeatRelayWorkerResultEndToEnd(t *testing.T) {
	ctx := context.Background()
	f := newMemoryBeatFixture(t)
	f.upsert(t)
	if err := f.beat.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if pending, err := f.memory.ListIntents(ctx, 10); err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
	f.now = f.page.NextDue
	if err := f.beat.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := f.memory.ListIntents(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	id := async.StableID(f.page.ID, f.page.NextDue.Format(time.RFC3339Nano))
	if pending[0].Envelope.ID != id || pending[0].Envelope.Retries != 0 {
		t.Fatal(pending)
	}
	restarted := &async.Beat{Client: f.client, Store: f.memory, ID: "restart"}
	if err := restarted.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if pending, _ := f.memory.ListIntents(ctx, 10); len(pending) != 1 {
		t.Fatal(pending)
	}
	relay := &async.IntentRelay{Client: f.client, Sources: []async.IntentStore{f.memory}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	delivery, err := f.memory.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.worker.Process(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	result := async.RestoreResult[int64](f.client, id)
	if value, err := result.Get(ctx); err != nil || value != 9007199254740994 || f.calls != 1 {
		t.Fatal(value, err, f.calls)
	}
	// Disabling future occurrences does not revoke an accepted result or replay
	// the delivered outbox. Re-enable is an explicit revisioned update.
	list, _ := f.memory.List(ctx, 1)
	if err := f.memory.Disable(ctx, f.page.ID, list[0].Revision); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Minute)
	if err := restarted.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if value, err := result.Get(ctx); err != nil || value != 9007199254740994 || f.calls != 1 {
		t.Fatal(value, err, f.calls)
	}
}

func TestMemoryBeatMisfireFixedDelayAndOverlap(t *testing.T) {
	for _, mode := range []string{"skip", "coalesce", "catchup", "fixed-delay", "overlap"} {
		t.Run(mode, func(t *testing.T) {
			f := newMemoryBeatFixture(t)
			f.page.NextDue = f.now.Add(-10 * time.Minute)
			wantCount, wantNext := 1, f.now.Add(time.Minute)
			switch mode {
			case "skip":
				f.page.Misfire, wantCount = "skip", 0
			case "catchup":
				f.page.Misfire, wantCount, wantNext = "catchup", 3, f.page.NextDue.Add(3*time.Minute)
			case "fixed-delay":
				f.page.Rule.FixedDelay = true
			case "overlap":
				f.page.Overlap, f.page.LastTaskID, wantCount = "skip", async.StableID(t.Name(), "prior"), 0
				e := async.Envelope{ProtocolVersion: 1, ID: f.page.LastTaskID, Task: f.page.Signature.Task, Version: 1, Args: f.page.Signature.Args, Queue: "default", CreatedAt: f.now}
				if err := f.memory.Register(context.Background(), e, async.Queued); err != nil {
					t.Fatal(err)
				}
			}
			f.upsert(t)
			if err := f.beat.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			pending, err := f.memory.ListIntents(context.Background(), 10)
			list, listErr := f.memory.List(context.Background(), 1)
			if err != nil || listErr != nil || len(pending) != wantCount || !list[0].NextDue.Equal(wantNext) {
				t.Fatal(pending, list, err, listErr)
			}
			for _, intent := range pending {
				if intent.Envelope.Retries != 0 {
					t.Fatal(intent)
				}
			}
		})
	}
}

func TestMemoryBeatDSTOccurrenceIdentities(t *testing.T) {
	for _, mode := range []string{"fold", "gap"} {
		t.Run(mode, func(t *testing.T) {
			f := newMemoryBeatFixture(t)
			expression := "30 1 * * *"
			f.now = time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
			wantNext := f.now.Add(time.Hour)
			if mode == "gap" {
				expression = "30 2 * * *"
				f.now = time.Date(2026, 3, 7, 7, 30, 0, 0, time.UTC)
				wantNext = time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC)
			}
			rule, err := async.Crontab(expression, "America/New_York")
			if err != nil {
				t.Fatal(err)
			}
			f.page.Rule, f.page.NextDue = rule, f.now
			f.upsert(t)
			if err := f.beat.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			list, _ := f.memory.List(context.Background(), 1)
			if !list[0].NextDue.Equal(wantNext) {
				t.Fatal(list)
			}
			if mode == "fold" {
				f.now = wantNext
				if err := f.beat.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				pending, _ := f.memory.ListIntents(context.Background(), 10)
				if len(pending) != 2 || pending[0].Envelope.ID == pending[1].Envelope.ID {
					t.Fatal(pending)
				}
			}
		})
	}
}

type memoryPeriodicLostReply struct {
	async.PeriodicStore
	lost bool
}

func (s *memoryPeriodicLostReply) CommitOccurrence(ctx context.Context, p async.PeriodicSchedule, next time.Time, id string, intents []async.Intent) error {
	if err := s.PeriodicStore.CommitOccurrence(ctx, p, next, id, intents); err != nil {
		return err
	}
	if !s.lost {
		s.lost = true
		return async.ErrUnavailable
	}
	return nil
}

func TestMemoryBeatFailureAndUnknownCommitRecovery(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-write", true: "lost-reply"}[applied], func(t *testing.T) {
			f := newMemoryBeatFixture(t)
			f.page.NextDue = f.now
			f.upsert(t)
			if applied {
				f.beat.Store = &memoryPeriodicLostReply{PeriodicStore: f.memory}
			} else {
				f.memory.Fail = func(op string) error {
					if op == "commit_occurrence" {
						return async.ErrUnavailable
					}
					return nil
				}
			}
			if err := f.beat.Tick(context.Background()); !errors.Is(err, async.ErrUnavailable) {
				t.Fatal(err)
			}
			f.memory.Fail = nil
			pending, _ := f.memory.ListIntents(context.Background(), 10)
			want := 0
			if applied {
				want = 1
			}
			if len(pending) != want {
				t.Fatal(pending)
			}
			// Before-write failure retains the old lease; advance the fake clock
			// to its exact boundary. Lost-reply success already advanced the row.
			if !applied {
				f.now = f.now.Add(time.Minute)
			}
			f.beat = &async.Beat{Client: f.client, Store: f.memory, ID: "recovery"}
			if err := f.beat.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			pending, _ = f.memory.ListIntents(context.Background(), 10)
			if len(pending) != 1 || pending[0].Envelope.ID != async.StableID(f.page.ID, f.page.NextDue.Format(time.RFC3339Nano)) {
				t.Fatal(pending)
			}
		})
	}
}

func TestMemoryBeatCurrentGrantsAndTerminalTaskFailure(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 4, 5, 6, 7, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	memory := fakes.NewMemory()
	memory.Clock = clock
	registry := async.NewRegistry()
	calls := 0
	task, err := async.Register(registry, "test.periodic_failure", 1, func(context.Context, async.TaskContext, int) (int, error) {
		calls++
		return 0, errors.New("private handler detail")
	}, async.TaskOptions{Authorize: func(context.Context, async.TaskContext) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	allowed := false
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: memory, Results: memory, Clock: clock,
		Authorize: func(_ context.Context, action, scope, id string) error {
			if !allowed || scope != "tenant" {
				return async.ErrDenied
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	sig, err := task.Signature(1)
	if err != nil {
		t.Fatal(err)
	}
	sig = sig.Set(async.WithScope("tenant", ""))
	p := async.PeriodicSchedule{ID: async.StableID(t.Name(), "schedule"), Signature: sig, Rule: async.Every(time.Hour), Enabled: true, NextDue: now,
		Misfire: "coalesce", CatchUpLimit: 1, Overlap: "allow", Revision: 1}
	if err := memory.UpsertSchedule(ctx, p, 0); err != nil {
		t.Fatal(err)
	}
	beat := async.Beat{Client: client, Store: memory, ID: "beat", Lease: time.Minute}
	if err := beat.Tick(ctx); !errors.Is(err, async.ErrDenied) {
		t.Fatal(err)
	}
	if pending, _ := memory.ListIntents(ctx, 10); len(pending) != 0 || calls != 0 {
		t.Fatal(pending, calls)
	}
	allowed = true
	now = now.Add(time.Minute)
	if err := beat.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	relay := async.IntentRelay{Client: client, Sources: []async.IntentStore{memory}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	delivery, err := memory.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	worker := async.Worker{Registry: registry, Broker: memory, Results: memory, ID: "worker", Clock: clock}
	if err := worker.Process(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	id := async.StableID(p.ID, p.NextDue.Format(time.RFC3339Nano))
	result := async.RestoreResult[int](client, id)
	if snapshot, err := result.Snapshot(ctx); err != nil || snapshot.State != async.Failed || calls != 1 {
		t.Fatal(snapshot, err, calls)
	}
	if _, err := result.Get(ctx); err == nil {
		t.Fatal("failed periodic task returned success")
	}
	allowed = false
	if _, err := result.Get(ctx); !errors.Is(err, async.ErrDenied) {
		t.Fatal(err)
	}
}
