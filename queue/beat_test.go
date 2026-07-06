package queue_test

import (
	"context"
	"testing"
	"time"

	q "github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/brokers"
)

func TestBeatTickEnqueuesDueScheduleAndPersistsRunState(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	app := q.NewApp(q.AppOptions{})
	_, _ = app.RegisterTask("jobs.run", func(context.Context, ...any) (any, error) { return nil, nil }, q.TaskOptions{})
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	store := q.NewMemoryScheduleStore(q.MemoryScheduleStoreOptions{Now: func() time.Time { return now }})
	entry := q.ScheduleEntry{
		Name:      "every-minute",
		Signature: q.NewSignature("jobs.run").WithHeader("source", "beat"),
		Schedule:  q.IntervalSchedule{Every: time.Minute, StartAt: now.Add(-time.Minute)},
		Enabled:   true,
		Send:      q.SendOptions{ID: "scheduled-task"},
	}
	if err := store.Save(ctx, entry); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	beat := q.NewBeat(app, broker, store, q.BeatOptions{Now: func() time.Time { return now }})
	enqueued, err := beat.Tick(ctx)
	if err != nil || enqueued != 1 {
		t.Fatalf("Tick() = %d, %v", enqueued, err)
	}
	message, err := broker.Consume(ctx, "default", brokers.ConsumeOptions{})
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if message.Envelope.ID != "scheduled-task" || message.Envelope.Headers["source"] != "beat" {
		t.Fatalf("message = %#v", message)
	}
	entries, err := store.List(ctx)
	if err != nil || len(entries) != 1 || entries[0].LastRunAt == nil || entries[0].TotalRunCount != 1 {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
}

func TestBeatOneOffScheduleDisablesAfterRun(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	app := q.NewApp(q.AppOptions{})
	_, _ = app.RegisterTask("jobs.once", func(context.Context, ...any) (any, error) { return nil, nil }, q.TaskOptions{})
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	store := q.NewMemoryScheduleStore(q.MemoryScheduleStoreOptions{Now: func() time.Time { return now }})
	if err := store.Save(ctx, q.ScheduleEntry{
		Name:      "once",
		Signature: q.NewSignature("jobs.once"),
		Schedule:  q.ClockedSchedule{RunAt: now},
		Enabled:   true,
		OneOff:    true,
		Send:      q.SendOptions{ID: "once-task"},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	beat := q.NewBeat(app, broker, store, q.BeatOptions{Now: func() time.Time { return now }})
	if enqueued, err := beat.Tick(ctx); err != nil || enqueued != 1 {
		t.Fatalf("first Tick() = %d, %v", enqueued, err)
	}
	if enqueued, err := beat.Tick(ctx); err != nil || enqueued != 0 {
		t.Fatalf("second Tick() = %d, %v", enqueued, err)
	}
	entries, _ := store.List(ctx)
	if entries[0].Enabled {
		t.Fatalf("one-off entry should be disabled: %#v", entries[0])
	}
}
