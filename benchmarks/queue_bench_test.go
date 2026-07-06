package benchmarks

import (
	"context"
	"strconv"
	"testing"

	q "github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/backends"
	"github.com/Newton-School/gogo/queue/brokers"
)

func BenchmarkQueuePublish(b *testing.B) {
	ctx := context.Background()
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	if err := broker.DeclareQueue(ctx, "default", brokers.QueueOptions{Durable: true}); err != nil {
		b.Fatalf("DeclareQueue() error = %v", err)
	}
	envelope := q.Envelope{ID: "task", Name: "bench.noop"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := broker.Publish(ctx, "default", envelope, brokers.PublishOptions{}); err != nil {
			b.Fatalf("Publish() error = %v", err)
		}
		if i%1024 == 1023 {
			if _, err := broker.PurgeQueue(ctx, "default"); err != nil {
				b.Fatalf("PurgeQueue() error = %v", err)
			}
		}
	}
}

func BenchmarkQueueWorkerRunOnce(b *testing.B) {
	ctx := context.Background()
	app := q.NewApp(q.AppOptions{})
	if _, err := app.RegisterTask("bench.add", func(_ context.Context, args ...any) (any, error) {
		return args[0].(int) + args[1].(int), nil
	}, q.TaskOptions{AckPolicy: q.AckLate, TrackStarted: true}); err != nil {
		b.Fatalf("RegisterTask() error = %v", err)
	}
	broker := brokers.NewMemoryBroker(brokers.MemoryOptions{})
	backend := backends.NewMemoryBackend(backends.MemoryOptions{})
	worker := q.NewWorker(app, broker, backend, q.WorkerOptions{Queues: []string{"default"}, Concurrency: 1, Pool: q.NewSoloPool()})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := "task-" + strconv.Itoa(i)
		if _, err := broker.Publish(ctx, "default", q.Envelope{ID: id, Name: "bench.add", Args: []any{2, 3}}, brokers.PublishOptions{}); err != nil {
			b.Fatalf("Publish() error = %v", err)
		}
		if err := worker.RunOnce(ctx); err != nil {
			b.Fatalf("RunOnce() error = %v", err)
		}
		result, err := backend.GetResult(ctx, id)
		if err != nil {
			b.Fatalf("GetResult() error = %v", err)
		}
		if result.State != q.StateSuccess || result.Result != 5 {
			b.Fatalf("result = %#v", result)
		}
	}
}
