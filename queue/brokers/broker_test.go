package brokers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Newton-School/gogo/queue"
)

func TestMemoryBrokerPublishConsumeAckPurgeInspectAndClose(t *testing.T) {
	ctx := context.Background()
	broker := NewMemoryBroker(MemoryOptions{VisibilityTimeout: time.Minute})
	if err := broker.DeclareQueue(ctx, "default", QueueOptions{Durable: true}); err != nil {
		t.Fatalf("DeclareQueue() error = %v", err)
	}
	envelope := queue.Envelope{ID: "task-1", Name: "blog.publish"}
	published, err := broker.Publish(ctx, "default", envelope, PublishOptions{Priority: 4})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if published.Queue != "default" || published.Envelope.ID != "task-1" || published.Priority != 4 {
		t.Fatalf("published = %#v", published)
	}

	queues, err := broker.InspectQueues(ctx)
	if err != nil || len(queues) != 1 || queues[0].Ready != 1 || queues[0].InFlight != 0 {
		t.Fatalf("InspectQueues(ready) = %#v, %v", queues, err)
	}

	message, err := broker.Consume(ctx, "default", ConsumeOptions{})
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if message.DeliveryID == "" || message.Attempts != 1 {
		t.Fatalf("message = %#v", message)
	}
	queues, _ = broker.InspectQueues(ctx)
	if queues[0].Ready != 0 || queues[0].InFlight != 1 {
		t.Fatalf("InspectQueues(inflight) = %#v", queues)
	}
	if err := broker.Ack(ctx, message); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	queues, _ = broker.InspectQueues(ctx)
	if queues[0].Ready != 0 || queues[0].InFlight != 0 {
		t.Fatalf("InspectQueues(acked) = %#v", queues)
	}

	if _, err := broker.Publish(ctx, "default", envelope, PublishOptions{}); err != nil {
		t.Fatalf("Publish(second) error = %v", err)
	}
	purged, err := broker.PurgeQueue(ctx, "default")
	if err != nil || purged != 1 {
		t.Fatalf("PurgeQueue() = %d, %v", purged, err)
	}
	if err := broker.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := broker.Publish(ctx, "default", envelope, PublishOptions{}); !errors.Is(err, ErrBrokerClosed) {
		t.Fatalf("Publish(closed) error = %v, want ErrBrokerClosed", err)
	}
}

func TestMemoryBrokerNackRequeueAndEmptyQueue(t *testing.T) {
	ctx := context.Background()
	broker := NewMemoryBroker(MemoryOptions{VisibilityTimeout: time.Minute})
	_ = broker.DeclareQueue(ctx, "default", QueueOptions{})
	if _, err := broker.Consume(ctx, "default", ConsumeOptions{}); !errors.Is(err, ErrQueueEmpty) {
		t.Fatalf("Consume(empty) error = %v, want ErrQueueEmpty", err)
	}
	envelope := queue.Envelope{ID: "task-2", Name: "blog.publish"}
	if _, err := broker.Publish(ctx, "default", envelope, PublishOptions{}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	message, err := broker.Consume(ctx, "default", ConsumeOptions{})
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if err := broker.Nack(ctx, message, true); err != nil {
		t.Fatalf("Nack(requeue) error = %v", err)
	}
	again, err := broker.Consume(ctx, "default", ConsumeOptions{})
	if err != nil {
		t.Fatalf("Consume(requeued) error = %v", err)
	}
	if again.Envelope.ID != "task-2" || again.Attempts != 2 {
		t.Fatalf("requeued message = %#v", again)
	}
	if err := broker.Requeue(ctx, again, 0); err != nil {
		t.Fatalf("Requeue() error = %v", err)
	}
	third, err := broker.Consume(ctx, "default", ConsumeOptions{})
	if err != nil || third.Attempts != 3 {
		t.Fatalf("Consume(requeue direct) = %#v, %v", third, err)
	}
	if err := broker.Nack(ctx, third, false); err != nil {
		t.Fatalf("Nack(drop) error = %v", err)
	}
	if _, err := broker.Consume(ctx, "default", ConsumeOptions{}); !errors.Is(err, ErrQueueEmpty) {
		t.Fatalf("Consume(after drop) error = %v, want ErrQueueEmpty", err)
	}
}
