package rabbitmq

import (
	"context"
	"time"

	"github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/brokers"
)

type Config struct {
	URL                string
	Exchange           string
	DeadLetterExchange string
	DelayedExchange    string
	MaxPriority        int
	Durable            bool
	VisibilityTimeout  time.Duration
}

type RouteOptions struct {
	Queue      string
	RoutingKey string
	Delayed    bool
}

type RoutePlan struct {
	Exchange           string
	Queue              string
	RoutingKey         string
	Durable            bool
	MaxPriority        int
	DeadLetterExchange string
	DelayedExchange    string
	Delayed            bool
}

type Broker struct {
	config Config
	memory *brokers.MemoryBroker
}

func NewBroker(config Config) *Broker {
	if config.Exchange == "" {
		config.Exchange = "gogo.tasks"
	}
	if config.DeadLetterExchange == "" {
		config.DeadLetterExchange = "gogo.dead"
	}
	if config.DelayedExchange == "" {
		config.DelayedExchange = "gogo.delayed"
	}
	return &Broker{
		config: config,
		memory: brokers.NewMemoryBroker(brokers.MemoryOptions{VisibilityTimeout: config.VisibilityTimeout}),
	}
}

func (b *Broker) PlanRoute(options RouteOptions) RoutePlan {
	queueName := options.Queue
	if queueName == "" {
		queueName = "default"
	}
	routingKey := options.RoutingKey
	if routingKey == "" {
		routingKey = queueName
	}
	return RoutePlan{
		Exchange:           b.config.Exchange,
		Queue:              queueName,
		RoutingKey:         routingKey,
		Durable:            b.config.Durable,
		MaxPriority:        b.config.MaxPriority,
		DeadLetterExchange: b.config.DeadLetterExchange,
		DelayedExchange:    b.config.DelayedExchange,
		Delayed:            options.Delayed,
	}
}

func (b *Broker) Publish(ctx context.Context, queueName string, envelope queue.Envelope, options brokers.PublishOptions) (brokers.Message, error) {
	return b.memory.Publish(ctx, queueName, envelope, options)
}

func (b *Broker) Consume(ctx context.Context, queueName string, options brokers.ConsumeOptions) (brokers.Message, error) {
	return b.memory.Consume(ctx, queueName, options)
}

func (b *Broker) Ack(ctx context.Context, message brokers.Message) error {
	return b.memory.Ack(ctx, message)
}

func (b *Broker) Nack(ctx context.Context, message brokers.Message, requeue bool) error {
	return b.memory.Nack(ctx, message, requeue)
}

func (b *Broker) Requeue(ctx context.Context, message brokers.Message, delay time.Duration) error {
	return b.memory.Requeue(ctx, message, delay)
}

func (b *Broker) DeclareQueue(ctx context.Context, queueName string, options brokers.QueueOptions) error {
	return b.memory.DeclareQueue(ctx, queueName, options)
}

func (b *Broker) PurgeQueue(ctx context.Context, queueName string) (int, error) {
	return b.memory.PurgeQueue(ctx, queueName)
}

func (b *Broker) InspectQueues(ctx context.Context) ([]brokers.QueueInfo, error) {
	return b.memory.InspectQueues(ctx)
}

func (b *Broker) Close() error {
	return b.memory.Close()
}
