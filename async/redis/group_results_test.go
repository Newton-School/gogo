package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
)

func TestRealRedisGroupIteratesPartialOrderedFailuresAndRevocation(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	registry := async.NewRegistry()
	task, err := async.Register(registry, "group.result", 1, func(_ context.Context, _ async.TaskContext, n int) (int64, error) {
		if n == 2 {
			return 0, errors.New("not public")
		}
		return 9007199254740993 + int64(n), nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows})
	if err != nil {
		t.Fatal(err)
	}
	one, _ := task.Signature(1)
	two, _ := task.Signature(2)
	three, _ := task.Signature(3)
	group, err := client.ApplyCanvas(ctx, async.Group(one, two, three))
	if err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{workflows, results}, ID: "group-relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	deliveries := map[int]async.Delivery{}
	for i := 0; i < 3; i++ {
		delivery, err := broker.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "group-worker"})
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := async.DecodeEnvelope(delivery.Body)
		if err != nil {
			t.Fatal(err)
		}
		var value int
		if err := json.Unmarshal(envelope.Args, &value); err != nil {
			t.Fatal(err)
		}
		deliveries[value] = delivery
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, ID: "group-worker"}
	complete := func(n int) {
		t.Helper()
		if err := worker.Process(ctx, deliveries[n]); err != nil {
			t.Fatal(err)
		}
		if err := relay.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	complete(3)
	var indexes []int
	for member, err := range group.Iterate(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		indexes = append(indexes, member.Index)
		if len(indexes) == 1 {
			complete(1)
			complete(2)
		}
	}
	if !reflect.DeepEqual(indexes, []int{2, 0, 1}) {
		t.Fatal(indexes)
	}
	outcomes, err := group.JoinOutcomes(ctx)
	if err != nil || len(outcomes) != 3 || string(outcomes[0].Output) != "9007199254740994" || outcomes[1].State != async.Failed || string(outcomes[2].Output) != "9007199254740996" {
		t.Fatal(outcomes, err)
	}
	if _, err := group.Join(ctx); err == nil {
		t.Fatal("ordinary join swallowed failure")
	}
	revoked, err := client.ApplyCanvas(ctx, async.Group(one, two))
	if err != nil {
		t.Fatal(err)
	}
	if err := revoked.Revoke(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := relay.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	outcomes, err = revoked.JoinOutcomes(ctx)
	if err != nil || len(outcomes) != 2 || outcomes[0].State != async.Revoked || outcomes[1].State != async.Revoked {
		t.Fatal(outcomes, err)
	}
}
