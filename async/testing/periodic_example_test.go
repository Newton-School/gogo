package testing_test

import (
	"context"
	"fmt"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func ExampleMemory_UpsertSchedule() {
	ctx := context.Background()
	now := time.Date(2026, 4, 5, 6, 7, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	memory := fakes.NewMemory() // Explicit test selection; never a production fallback.
	memory.Clock = clock
	registry := async.NewRegistry()
	task, err := async.Register(registry, "example.periodic", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n + 1, nil }, async.TaskOptions{})
	if err != nil {
		panic(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: memory, Results: memory, Clock: clock})
	if err != nil {
		panic(err)
	}
	signature, err := task.Signature(41)
	if err != nil {
		panic(err)
	}
	schedule := async.PeriodicSchedule{ID: async.StableID("example", "schedule"), Signature: signature, Rule: async.Every(time.Minute), Enabled: true,
		NextDue: now.Add(time.Minute), Misfire: "coalesce", CatchUpLimit: 1, Overlap: "allow", Revision: 1}
	if err := memory.UpsertSchedule(ctx, schedule, 0); err != nil {
		panic(err)
	}
	beat := async.Beat{Client: client, Store: memory, ID: "beat"}
	if err := beat.Tick(ctx); err != nil {
		panic(err)
	}
	intents, err := memory.ListIntents(ctx, 10)
	if err != nil {
		panic(err)
	}
	fmt.Println("before due:", len(intents))
	now = schedule.NextDue
	if err := beat.Tick(ctx); err != nil {
		panic(err)
	}
	relay := async.IntentRelay{Client: client, Sources: []async.IntentStore{memory}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		panic(err)
	}
	delivery, err := memory.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
	if err != nil {
		panic(err)
	}
	worker := async.Worker{Registry: registry, Broker: memory, Results: memory, ID: "worker", Clock: clock}
	if err := worker.Process(ctx, delivery); err != nil {
		panic(err)
	}
	id := async.StableID(schedule.ID, schedule.NextDue.Format(time.RFC3339Nano))
	value, err := async.RestoreResult[int](client, id).Get(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println("result:", value)
	// Output:
	// before due: 0
	// result: 42
}
