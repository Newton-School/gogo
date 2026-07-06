package brokers

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Newton-School/gogo/queue"
)

var (
	ErrQueueEmpty   = queue.ErrQueueEmpty
	ErrBrokerClosed = queue.ErrBrokerClosed
)

// Broker is the stable queue broker interface.
type Broker = queue.Broker

type PublishOptions = queue.BrokerPublishOptions

type ConsumeOptions = queue.BrokerConsumeOptions

type QueueOptions = queue.BrokerQueueOptions

type Message = queue.BrokerMessage

type QueueInfo = queue.BrokerQueueInfo

type MemoryOptions struct {
	VisibilityTimeout time.Duration
}

func init() {
	queue.RegisterBrokerFactory("memory", func(queue.RuntimeConfig) (queue.Broker, error) {
		return NewMemoryBroker(MemoryOptions{}), nil
	})
}

// MemoryBroker is a deterministic in-memory broker useful for tests and local development.
type MemoryBroker struct {
	mu                sync.Mutex
	visibilityTimeout time.Duration
	queues            map[string]*memoryQueue
	inFlight          map[string]Message
	counter           int64
	closed            bool
}

type memoryQueue struct {
	options QueueOptions
	ready   []Message
}

func NewMemoryBroker(options MemoryOptions) *MemoryBroker {
	if options.VisibilityTimeout == 0 {
		options.VisibilityTimeout = time.Minute
	}
	return &MemoryBroker{
		visibilityTimeout: options.VisibilityTimeout,
		queues:            map[string]*memoryQueue{},
		inFlight:          map[string]Message{},
	}
}

func (b *MemoryBroker) Publish(ctx context.Context, name string, envelope queue.Envelope, options PublishOptions) (Message, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureOpen(); err != nil {
		return Message{}, err
	}
	q := b.queue(name)
	message := Message{Queue: name, Envelope: envelope, Priority: options.Priority, VisibleAt: time.Now()}
	q.ready = append(q.ready, message)
	sortReady(q.ready)
	return message, nil
}

func (b *MemoryBroker) Consume(ctx context.Context, name string, options ConsumeOptions) (Message, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureOpen(); err != nil {
		return Message{}, err
	}
	q := b.queue(name)
	now := time.Now()
	for index, message := range q.ready {
		if message.VisibleAt.After(now) {
			continue
		}
		q.ready = append(q.ready[:index], q.ready[index+1:]...)
		b.counter++
		message.DeliveryID = fmt.Sprintf("%s-%d", message.Envelope.ID, b.counter)
		message.Attempts++
		timeout := options.VisibilityTimeout
		if timeout == 0 {
			timeout = q.options.VisibilityTimeout
		}
		if timeout == 0 {
			timeout = b.visibilityTimeout
		}
		message.Deadline = now.Add(timeout)
		b.inFlight[message.DeliveryID] = message
		return message, nil
	}
	return Message{}, ErrQueueEmpty
}

func (b *MemoryBroker) Ack(_ context.Context, message Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureOpen(); err != nil {
		return err
	}
	delete(b.inFlight, message.DeliveryID)
	return nil
}

func (b *MemoryBroker) Nack(ctx context.Context, message Message, requeue bool) error {
	if requeue {
		return b.Requeue(ctx, message, 0)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureOpen(); err != nil {
		return err
	}
	delete(b.inFlight, message.DeliveryID)
	return nil
}

func (b *MemoryBroker) Requeue(_ context.Context, message Message, delay time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureOpen(); err != nil {
		return err
	}
	delete(b.inFlight, message.DeliveryID)
	message.DeliveryID = ""
	message.VisibleAt = time.Now().Add(delay)
	q := b.queue(message.Queue)
	q.ready = append(q.ready, message)
	sortReady(q.ready)
	return nil
}

func (b *MemoryBroker) DeclareQueue(_ context.Context, name string, options QueueOptions) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureOpen(); err != nil {
		return err
	}
	if _, ok := b.queues[name]; !ok {
		b.queues[name] = &memoryQueue{options: options}
		return nil
	}
	b.queues[name].options = options
	return nil
}

func (b *MemoryBroker) PurgeQueue(_ context.Context, name string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureOpen(); err != nil {
		return 0, err
	}
	q := b.queue(name)
	count := len(q.ready)
	q.ready = nil
	return count, nil
}

func (b *MemoryBroker) InspectQueues(_ context.Context) ([]QueueInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureOpen(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(b.queues))
	for name := range b.queues {
		names = append(names, name)
	}
	sort.Strings(names)
	infos := make([]QueueInfo, len(names))
	for i, name := range names {
		q := b.queues[name]
		infos[i] = QueueInfo{Name: name, Ready: len(q.ready), Durable: q.options.Durable}
		for _, message := range b.inFlight {
			if message.Queue == name {
				infos[i].InFlight++
			}
		}
	}
	return infos, nil
}

func (b *MemoryBroker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func (b *MemoryBroker) queue(name string) *memoryQueue {
	if name == "" {
		name = "default"
	}
	q, ok := b.queues[name]
	if !ok {
		q = &memoryQueue{}
		b.queues[name] = q
	}
	return q
}

func (b *MemoryBroker) ensureOpen() error {
	if b.closed {
		return ErrBrokerClosed
	}
	return nil
}

func sortReady(messages []Message) {
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].Priority != messages[j].Priority {
			return messages[i].Priority > messages[j].Priority
		}
		return messages[i].VisibleAt.Before(messages[j].VisibleAt)
	})
}
