package async_test

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func ExampleRegister() {
	ctx := context.Background()
	registry := async.NewRegistry()
	double, err := async.Register(registry, "math.double", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n * 2, nil }, async.TaskOptions{})
	if err != nil {
		panic(err)
	}
	// Eager execution is explicit; production never falls back to it on outage.
	value, err := double.Apply(ctx, 21)
	if err != nil {
		panic(err)
	}
	fmt.Println(value)
	// Output: 42
}

func ExampleWorker() {
	ctx := context.Background()
	registry := async.NewRegistry()
	add, err := async.Register(registry, "math.add", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n + 1, nil }, async.TaskOptions{})
	if err != nil {
		panic(err)
	}
	// Tests explicitly select this simulator; applications install a durable
	// adapter such as github.com/Newton-School/gogo/async/redis instead.
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend})
	if err != nil {
		panic(err)
	}
	result, err := add.Delay(ctx, client, 41)
	if err != nil {
		panic(err)
	}
	delivery, err := backend.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "example"})
	if err != nil {
		panic(err)
	}
	worker := async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "example"}
	if err := worker.Process(ctx, delivery); err != nil {
		panic(err)
	}
	value, err := result.Get(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(value)
	// Output: 42
}
