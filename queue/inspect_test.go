package queue_test

import (
	"context"
	"testing"
	"time"

	q "github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/backends"
	"github.com/Newton-School/gogo/queue/brokers"
)

func TestInspectorSnapshotsAndControls(t *testing.T) {
	ctx := context.Background()
	app := q.NewApp(q.AppOptions{})
	_, _ = app.RegisterTask("jobs.inspect", func(context.Context, ...any) (any, error) { return nil, nil }, q.TaskOptions{})
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})
	_, _ = broker.Publish(ctx, "default", q.Envelope{ID: "reserved", Name: "jobs.inspect"}, brokers.PublishOptions{})
	store := q.NewMemoryScheduleStore(q.MemoryScheduleStoreOptions{})
	_ = store.Save(ctx, q.ScheduleEntry{Name: "periodic", Signature: q.NewSignature("jobs.inspect"), Schedule: q.IntervalSchedule{Every: time.Minute}, Enabled: true})
	revocations := q.NewRevocationRegistry()
	events := q.NewEventRecorder()
	worker := q.NewWorker(app, broker, backend, q.WorkerOptions{Hostname: "worker-inspect", Revocations: revocations, Events: events})
	inspector := q.NewInspector(q.InspectOptions{
		App:         app,
		Broker:      broker,
		Store:       store,
		Workers:     []*q.Worker{worker},
		Revocations: revocations,
		Events:      events,
	})

	report, err := inspector.Report(ctx)
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if len(report.Registered) != 1 || len(report.Scheduled) != 1 || len(report.Queues) != 1 || report.Queues[0].Ready != 1 || len(report.Workers) != 1 {
		t.Fatalf("report = %#v", report)
	}
	if pong := inspector.Ping(ctx); pong.Hostname == "" || !pong.OK {
		t.Fatalf("Ping() = %#v", pong)
	}
	inspector.RevokeTask("task-id")
	if !revocations.IsRevoked(q.Envelope{ID: "task-id"}) {
		t.Fatal("RevokeTask() did not update registry")
	}
	inspector.RevokeByStampedHeaders("tenant", "blocked")
	if !revocations.IsRevoked(q.Envelope{ID: "other", Headers: map[string]string{"tenant": "blocked"}}) {
		t.Fatal("RevokeByStampedHeaders() did not update registry")
	}
	if err := inspector.RateLimit("jobs.inspect", q.RateLimit{Limit: 1, Period: time.Second}); err != nil {
		t.Fatalf("RateLimit() error = %v", err)
	}
	if err := inspector.TimeLimit("jobs.inspect", time.Second, 2*time.Second); err != nil {
		t.Fatalf("TimeLimit() error = %v", err)
	}
	task, _ := app.Task("jobs.inspect")
	if task.Options.RateLimit.Limit != 1 || task.Options.SoftTimeout != time.Second || task.Options.HardTimeout != 2*time.Second {
		t.Fatalf("task options = %#v", task.Options)
	}
	inspector.PoolGrow(worker, 2)
	if worker.Stats().Concurrency != 3 {
		t.Fatalf("grown worker stats = %#v", worker.Stats())
	}
	inspector.PoolShrink(worker, 1)
	if worker.Stats().Concurrency != 2 {
		t.Fatalf("shrunk worker stats = %#v", worker.Stats())
	}
	inspector.DisableEvents()
	events.EmitQueueEvent(ctx, q.Event{Type: q.EventWorkerOnline})
	if len(events.Events()) != 0 {
		t.Fatal("DisableEvents() should disable recorder")
	}
	inspector.EnableEvents()
	events.EmitQueueEvent(ctx, q.Event{Type: q.EventWorkerOnline})
	if len(events.Events()) != 1 {
		t.Fatal("EnableEvents() should enable recorder")
	}
}
