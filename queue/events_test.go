package queue_test

import (
	"context"
	"reflect"
	"testing"

	q "github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/backends"
	"github.com/Newton-School/gogo/queue/brokers"
)

func TestQueueEventsForSendReceiveSuccessAndLifecycle(t *testing.T) {
	ctx := context.Background()
	events := q.NewEventRecorder()
	app := q.NewApp(q.AppOptions{})
	_, _ = app.RegisterTask("jobs.ok", func(context.Context, ...any) (any, error) {
		return "ok", nil
	}, q.TaskOptions{AckPolicy: q.AckLate, TrackStarted: true})
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})
	if _, err := app.SendTask(ctx, broker, q.NewSignature("jobs.ok"), q.SendOptions{ID: "task-event", Events: events}); err != nil {
		t.Fatalf("SendTask() error = %v", err)
	}
	worker := q.NewWorker(app, broker, backend, q.WorkerOptions{Hostname: "worker-events", Events: events})
	worker.Heartbeat(ctx)
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	got := eventTypes(events.Events())
	want := []q.EventType{q.EventTaskSent, q.EventWorkerHeartbeat, q.EventTaskReceived, q.EventTaskStarted, q.EventTaskSucceeded}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event types = %#v", got)
	}
}

func TestEventRecorderEnableDisable(t *testing.T) {
	recorder := q.NewEventRecorder()
	recorder.Disable()
	recorder.EmitQueueEvent(context.Background(), q.Event{Type: q.EventWorkerOnline})
	if len(recorder.Events()) != 0 {
		t.Fatal("disabled recorder should not store events")
	}
	recorder.Enable()
	recorder.EmitQueueEvent(context.Background(), q.Event{Type: q.EventWorkerOnline})
	if len(recorder.Events()) != 1 {
		t.Fatal("enabled recorder should store events")
	}
}

func eventTypes(events []q.Event) []q.EventType {
	types := make([]q.EventType, len(events))
	for i, event := range events {
		types[i] = event.Type
	}
	return types
}
