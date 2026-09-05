package async_test

import (
	"context"
	"testing"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func TestClientFreezesCallerRoutingSlices(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	task, err := async.Register(registry, "routing.frozen", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backend := fakes.NewMemory()
	config := async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Queues: []string{"default", "background", "changed"}, Routes: []async.Route{{Pattern: "routing.*", Queue: "background"}}, AllowedHeaders: []string{"request-id"}}
	client, err := async.NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	config.Routes[0].Queue = "changed"
	config.Queues[1] = "untrusted"
	config.AllowedHeaders[0] = "authorization"
	if _, err := task.Delay(ctx, client, 1, async.WithHeaders(map[string]string{"request-id": "fixture"})); err != nil {
		t.Fatal(err)
	}
	delivery, err := backend.Consume(ctx, async.ConsumeOptions{Queues: []string{"background"}, Consumer: "test"})
	if err != nil {
		t.Fatal("caller mutation changed accepted route", err)
	}
	envelope, err := async.DecodeEnvelope(delivery.Body)
	if err != nil || envelope.Queue != "background" {
		t.Fatal(envelope, err)
	}
	if _, err := task.Delay(ctx, client, 1, async.WithHeaders(map[string]string{"authorization": "fixture"})); err == nil {
		t.Fatal("caller mutation expanded metadata allowlist")
	}
}
