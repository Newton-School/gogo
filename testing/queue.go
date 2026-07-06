package testing

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/backends"
	"github.com/Newton-School/gogo/queue/brokers"
)

type QueueHarness struct {
	App      *queue.App
	Broker   *brokers.MemoryBroker
	Backend  *backends.MemoryBackend
	enqueued []queue.Envelope
	counter  int
}

func NewQueueHarness(app *queue.App) *QueueHarness {
	if app == nil {
		app = queue.NewApp(queue.AppOptions{})
	}
	return &QueueHarness{
		App:     app,
		Broker:  brokers.NewMemoryBroker(brokers.MemoryOptions{}),
		Backend: backends.NewMemoryBackend(backends.MemoryOptions{}),
	}
}

func (h *QueueHarness) Apply(ctx context.Context, signature queue.Signature) (queue.Result, error) {
	task, ok := h.App.Task(signature.Name)
	if !ok {
		return queue.Result{}, fmt.Errorf("%w: %s", queue.ErrTaskNotRegistered, signature.Name)
	}
	taskID := h.nextTaskID()
	resultValue, err := task.Func(ctx, signature.Args...)
	result := queue.Result{TaskID: taskID}
	if err != nil {
		result.State = queue.StateFailure
		result.Error = err.Error()
		_ = h.Backend.StoreResult(ctx, result)
		return result, err
	}
	result.State = queue.StateSuccess
	result.Result = resultValue
	if !task.Options.IgnoreResult {
		if storeErr := h.Backend.StoreResult(ctx, result); storeErr != nil {
			return queue.Result{}, storeErr
		}
	}
	return result, nil
}

func (h *QueueHarness) Enqueue(ctx context.Context, signature queue.Signature) (queue.Envelope, error) {
	queueName := signature.Options.Queue
	if queueName == "" {
		queueName = "default"
		signature = signature.WithQueue(queueName)
	}
	envelope := queue.NewEnvelope(signature, queue.EnvelopeOptions{ID: h.nextTaskID()})
	if _, err := h.Broker.Publish(ctx, queueName, envelope, brokers.PublishOptions{Priority: signature.Options.Priority}); err != nil {
		return queue.Envelope{}, err
	}
	h.enqueued = append(h.enqueued, envelope)
	return envelope, nil
}

func (h *QueueHarness) Enqueued() []queue.Envelope {
	return append([]queue.Envelope(nil), h.enqueued...)
}

func (h *QueueHarness) AssertTaskEnqueued(t TestHelper, name string) {
	t.Helper()
	for _, envelope := range h.enqueued {
		if envelope.Name == name {
			return
		}
	}
	t.Fatalf("task %q was not enqueued; enqueued=%#v", name, h.enqueued)
}

func (h *QueueHarness) nextTaskID() string {
	h.counter++
	return fmt.Sprintf("test-task-%d", h.counter)
}
