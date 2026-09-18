package redis_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
)

func TestRealRedisAsyncDatabaseIsolationWithoutApplicationPrefixes(t *testing.T) {
	ctx := context.Background()
	cfg := fixture.Start(t)
	a, b := quarantineBackend(t, 1, cfg), quarantineBackend(t, 2, cfg)
	peer := quarantineBackend(t, 1, cfg)
	e := taskEnvelope(t)
	if err := a.Publish(ctx, e); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		broker *adapter.Broker
		want   int64
	}{{a, 1}, {peer, 1}, {b, 0}} {
		stats, err := check.broker.Inspect(ctx, []string{"default"})
		if err != nil || len(stats) != 1 || stats[0].Queued != check.want {
			t.Fatal("queue database isolation failed", stats, err)
		}
	}
	resultsA, resultsB := &adapter.Results{Connection: a.Connection}, &adapter.Results{Connection: b.Connection}
	if err := resultsA.Register(ctx, e, async.Queued); err != nil {
		t.Fatal(err)
	}
	if _, err := resultsB.Lookup(ctx, e.ID); !errors.Is(err, async.ErrNotFound) {
		t.Fatal("task result leaked across databases", err)
	}
	workflowsA, workflowsB := &adapter.Workflows{Connection: a.Connection}, &adapter.Workflows{Connection: b.Connection}
	if err := workflowsA.CreateGraph(ctx, inventoryGraph(e.ID), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := workflowsB.ReadGraph(ctx, e.ID); !errors.Is(err, async.ErrNotFound) {
		t.Fatal("workflow leaked across databases", err)
	}
	e.ETA = time.Now().Add(-time.Millisecond)
	schedulesA, schedulesB := &adapter.Schedules{Connection: a.Connection}, &adapter.Schedules{Connection: b.Connection}
	if err := schedulesA.Schedule(ctx, e); err != nil {
		t.Fatal(err)
	}
	if due, err := schedulesB.LeaseDue(ctx, "worker", 10, time.Minute); err != nil || len(due) != 0 {
		t.Fatal("schedule leaked across databases", err)
	}
	if due, err := schedulesA.LeaseDue(ctx, "worker", 10, time.Minute); err != nil || len(due) != 1 {
		t.Fatal("schedule unavailable in its own database", err)
	}
	for _, key := range []string{
		"queue:{" + connector.Digest("default") + "}:published:" + e.ID + ":0",
		fmt.Sprintf("{task-p%02d}:%s:state", connector.Partition(e.ID), e.ID),
		fmt.Sprintf("{workflow-p%02d}:%s:state", connector.Partition(e.ID), e.ID),
		fmt.Sprintf("schedule:{schedule-p%02d}:items", connector.Partition(e.ID)),
	} {
		if exists, err := a.Connection.Client().Exists(ctx, key).Result(); err != nil || exists != 1 {
			t.Fatal("missing unprefixed async key", key, err)
		}
	}
	// Identical task IDs and queue names are independent in another database.
	e.ETA = time.Time{}
	if err := b.Publish(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := resultsB.Register(ctx, e, async.Queued); err != nil {
		t.Fatal(err)
	}
}
