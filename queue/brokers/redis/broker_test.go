package redis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/brokers"
)

func TestRedisKeyNamingAndEnvelopeEncoding(t *testing.T) {
	broker := NewBroker(Config{Prefix: "gogo", VisibilityTimeout: time.Minute, PriorityBuckets: 10})
	keys := broker.Keys("emails")
	if keys.Ready != "gogo:queue:emails:ready" || keys.Delayed != "gogo:queue:emails:delayed" || keys.Unacked != "gogo:queue:emails:unacked" || keys.DeadLetter != "gogo:queue:emails:dead" {
		t.Fatalf("keys = %#v", keys)
	}
	if got := keys.Priority(9); got != "gogo:queue:emails:priority:9" {
		t.Fatalf("priority key = %q", got)
	}

	envelope := queue.Envelope{ID: "task-1", Name: "blog.publish", Queue: "emails", Priority: 9}
	encoded, err := EncodeEnvelope(envelope)
	if err != nil {
		t.Fatalf("EncodeEnvelope() error = %v", err)
	}
	decoded, err := DecodeEnvelope(encoded)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	if decoded.ID != envelope.ID || decoded.Name != envelope.Name || decoded.Queue != envelope.Queue || decoded.Priority != envelope.Priority {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestRedisBrokerImplementsBrokerBehavior(t *testing.T) {
	if os.Getenv("GOGO_TEST_REDIS_ADDR") == "" {
		t.Skip("set GOGO_TEST_REDIS_ADDR to run Redis broker behavior tests")
	}
	ctx := context.Background()
	prefix := redisTestPrefix(t)
	broker := NewBroker(Config{URL: redisTestURL(), Prefix: prefix, VisibilityTimeout: 25 * time.Millisecond})
	defer broker.Close()
	if err := broker.DeclareQueue(ctx, "default", brokers.QueueOptions{Durable: true, VisibilityTimeout: 25 * time.Millisecond}); err != nil {
		t.Fatalf("DeclareQueue() error = %v", err)
	}
	if _, err := broker.Publish(ctx, "default", queue.Envelope{ID: "task-1", Name: "blog.publish"}, brokers.PublishOptions{Priority: 5}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	message, err := broker.Consume(ctx, "default", brokers.ConsumeOptions{})
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if message.Priority != 5 || message.Envelope.ID != "task-1" {
		t.Fatalf("message = %#v", message)
	}
	queues, err := broker.InspectQueues(ctx)
	if err != nil || len(queues) != 1 || queues[0].Name != "default" || queues[0].InFlight != 1 || !queues[0].Durable {
		t.Fatalf("InspectQueues(in-flight) = %#v, %v", queues, err)
	}
	if err := broker.Ack(ctx, message); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	if _, err := broker.Consume(ctx, "default", brokers.ConsumeOptions{}); !errors.Is(err, brokers.ErrQueueEmpty) {
		t.Fatalf("Consume(empty) error = %v, want ErrQueueEmpty", err)
	}

	eta := time.Now().Add(40 * time.Millisecond)
	if _, err := broker.Publish(ctx, "default", queue.Envelope{ID: "task-delayed", Name: "blog.publish", ETA: &eta}, brokers.PublishOptions{}); err != nil {
		t.Fatalf("Publish(delayed) error = %v", err)
	}
	if _, err := broker.Consume(ctx, "default", brokers.ConsumeOptions{}); !errors.Is(err, brokers.ErrQueueEmpty) {
		t.Fatalf("Consume(delayed before ETA) error = %v, want ErrQueueEmpty", err)
	}
	time.Sleep(50 * time.Millisecond)
	delayed, err := broker.Consume(ctx, "default", brokers.ConsumeOptions{})
	if err != nil || delayed.Envelope.ID != "task-delayed" {
		t.Fatalf("Consume(delayed after ETA) = %#v, %v", delayed, err)
	}
	if err := broker.Ack(ctx, delayed); err != nil {
		t.Fatalf("Ack(delayed) error = %v", err)
	}
}

func TestRedisBrokerReclaimsExpiredInFlightAcrossClients(t *testing.T) {
	if os.Getenv("GOGO_TEST_REDIS_ADDR") == "" {
		t.Skip("set GOGO_TEST_REDIS_ADDR to run Redis broker reclaim tests")
	}
	ctx := context.Background()
	prefix := redisTestPrefix(t)
	first := NewBroker(Config{URL: redisTestURL(), Prefix: prefix, VisibilityTimeout: 20 * time.Millisecond})
	defer first.Close()
	second := NewBroker(Config{URL: redisTestURL(), Prefix: prefix, VisibilityTimeout: 20 * time.Millisecond})
	defer second.Close()

	if _, err := first.Publish(ctx, "default", queue.Envelope{ID: "task-reclaim", Name: "blog.publish"}, brokers.PublishOptions{}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	message, err := first.Consume(ctx, "default", brokers.ConsumeOptions{VisibilityTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("first Consume() error = %v", err)
	}
	if message.Attempts != 1 {
		t.Fatalf("first attempts = %d, want 1", message.Attempts)
	}
	time.Sleep(30 * time.Millisecond)
	reclaimed, err := second.Consume(ctx, "default", brokers.ConsumeOptions{})
	if err != nil {
		t.Fatalf("second Consume() error = %v", err)
	}
	if reclaimed.Envelope.ID != "task-reclaim" || reclaimed.Attempts != 2 {
		t.Fatalf("reclaimed = %#v", reclaimed)
	}
}

func redisTestURL() string {
	value := strings.TrimSpace(os.Getenv("GOGO_TEST_REDIS_ADDR"))
	if strings.Contains(value, "://") {
		return value
	}
	return "redis://" + value + "/0"
}

func redisTestPrefix(t *testing.T) string {
	t.Helper()
	name := strings.NewReplacer("/", ":", " ", ":", "-", ":").Replace(t.Name())
	return fmt.Sprintf("gogo:test:%s:%d", name, time.Now().UnixNano())
}
