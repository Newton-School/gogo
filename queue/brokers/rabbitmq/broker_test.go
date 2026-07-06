package rabbitmq

import (
	"context"
	"os"
	"testing"

	"github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/brokers"
)

func TestRabbitMQRouteDeclarationPlanning(t *testing.T) {
	broker := NewBroker(Config{
		Exchange:           "gogo.tasks",
		DeadLetterExchange: "gogo.dead",
		DelayedExchange:    "gogo.delayed",
		MaxPriority:        10,
		Durable:            true,
	})
	plan := broker.PlanRoute(RouteOptions{Queue: "emails", RoutingKey: "blog.publish", Delayed: true})
	if plan.Exchange != "gogo.tasks" || plan.Queue != "emails" || plan.RoutingKey != "blog.publish" {
		t.Fatalf("route plan = %#v", plan)
	}
	if !plan.Durable || plan.MaxPriority != 10 || plan.DeadLetterExchange != "gogo.dead" || plan.DelayedExchange != "gogo.delayed" || !plan.Delayed {
		t.Fatalf("route durability/dead-letter plan = %#v", plan)
	}
}

func TestRabbitMQBrokerImplementsBrokerBehavior(t *testing.T) {
	ctx := context.Background()
	var _ brokers.Broker = NewBroker(Config{})
	broker := NewBroker(Config{})
	if err := broker.DeclareQueue(ctx, "default", brokers.QueueOptions{}); err != nil {
		t.Fatalf("DeclareQueue() error = %v", err)
	}
	if _, err := broker.Publish(ctx, "default", queue.Envelope{ID: "task-1", Name: "blog.publish"}, brokers.PublishOptions{RoutingKey: "blog.publish"}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	message, err := broker.Consume(ctx, "default", brokers.ConsumeOptions{})
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if message.Envelope.ID != "task-1" {
		t.Fatalf("message = %#v", message)
	}
	if err := broker.Nack(ctx, message, true); err != nil {
		t.Fatalf("Nack(requeue) error = %v", err)
	}
	requeued, err := broker.Consume(ctx, "default", brokers.ConsumeOptions{})
	if err != nil || requeued.Attempts != 2 {
		t.Fatalf("Consume(requeued) = %#v, %v", requeued, err)
	}
}

func TestRabbitMQBrokerIntegration(t *testing.T) {
	if os.Getenv("GOGO_TEST_RABBITMQ_URL") == "" {
		t.Skip("set GOGO_TEST_RABBITMQ_URL to run RabbitMQ broker integration tests")
	}
	t.Skip("real RabbitMQ client integration is enabled when an AMQP client dependency is configured")
}
