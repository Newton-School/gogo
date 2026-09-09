package async_recipes_test

import (
	"context"
	"errors"
	"time"

	"github.com/Newton-School/gogo/async"
	simulator "github.com/Newton-School/gogo/async/testing"
)

// This helper is deliberately confined to _test.go. Memory is a process-local
// simulator, never a durable queue or an outage fallback for the application.
type harness struct {
	ctx    context.Context
	cancel context.CancelFunc
	now    time.Time
	memory *simulator.Memory
	client *async.Client
	worker *async.Worker
}

func newHarness(registry *async.Registry) *harness {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	h := &harness{ctx: ctx, cancel: cancel, now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	h.memory = simulator.NewMemory()
	clock := func() time.Time { return h.now }
	h.memory.Clock = clock
	h.client = must(async.NewClient(async.ClientConfig{
		Registry: registry, Broker: h.memory, Results: h.memory,
		Workflows: h.memory, Schedules: h.memory, Clock: clock,
	}))
	h.worker = &async.Worker{Registry: registry, Broker: h.memory, Results: h.memory,
		ID: "recipe-worker", Clock: clock, Concurrency: 1}
	return h
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

// Drive the same public ports separately: scheduling/coordination does not
// implicitly start a worker. Stop at quiescence without advancing fake time.
func (h *harness) drain() {
	relay := async.IntentRelay{Client: h.client, Sources: []async.IntentStore{h.memory}, ID: "recipe-relay"}
	delayed := async.DelayedDispatcher{Client: h.client, ID: "recipe-delayed"}
	for turn := 0; turn < 64; turn++ {
		check(relay.Tick(h.ctx))
		check(delayed.Tick(h.ctx))
		for processed := 0; ; processed++ {
			if processed >= 1000 {
				panic("recipe exceeded its bounded task budget")
			}
			delivery, err := h.memory.Consume(h.ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: h.worker.ID})
			if errors.Is(err, async.ErrNotFound) {
				break
			}
			check(err)
			check(h.worker.Process(h.ctx, delivery))
		}
		if len(must(h.memory.ListIntents(h.ctx, 1000))) == 0 {
			return
		}
	}
	panic("recipe coordination did not become idle")
}

func increment(registry *async.Registry) *async.Task[int, int] {
	return must(async.Register(registry, "recipes.increment", 1,
		func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n + 1, nil }, async.TaskOptions{}))
}
