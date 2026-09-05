package redis_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

func inventoryGraph(id string) async.Graph {
	return async.Graph{ID: id, Kind: "group", State: async.Succeeded, Revision: 1, Members: map[string]async.Completion{}}
}

func TestRealRedisWorkflowInventoryStrictPagesAndAtomicMembership(t *testing.T) {
	ctx := context.Background()
	_, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	wanted := map[string]bool{}
	for index := range 203 {
		id := async.StableID("inventory", fmt.Sprint(index))
		wanted[id] = true
		if err := workflows.CreateGraph(ctx, inventoryGraph(id), nil); err != nil {
			t.Fatal(err)
		}
	}
	// Unrelated task-family and namespace keys cannot enter this inventory.
	foreign, _ := results.Connection.PartitionIndex("task", 0, "records-v1")
	if err := results.Connection.Client().ZAdd(ctx, foreign, redigo.Z{Score: 0, Member: "foreign"}).Err(); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	for round := range 300 {
		page, err := workflows.ListWorkflows(ctx, cursor, 3)
		if err != nil || len(page.IDs) > 3 {
			t.Fatal(page, err)
		}
		for _, id := range page.IDs {
			if seen[id] || !wanted[id] {
				t.Fatal("duplicate or foreign ID", id)
			}
			seen[id] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
		if round == 299 {
			t.Fatal("inventory never completed")
		}
	}
	if len(seen) != len(wanted) {
		t.Fatal("inventory lost graph membership", len(seen), len(wanted))
	}
	for _, cursor := range []string{"redis-v1:-1:", "redis-v1:64:", "redis-v1:01:", "redis-v1:1:not-an-id", "foreign:0:", "redis-v1:0:x:y"} {
		if _, err := workflows.ListWorkflows(ctx, cursor, 3); !errors.Is(err, async.ErrInvalid) {
			t.Fatal(cursor, err)
		}
	}
	for _, limit := range []int{0, 1001} {
		if _, err := workflows.ListWorkflows(ctx, "", limit); !errors.Is(err, async.ErrInvalid) {
			t.Fatal(err)
		}
	}
}

func TestRealRedisWorkflowInventoryWrongKeyTypeNeverPartiallyCommitsGraph(t *testing.T) {
	ctx := context.Background()
	_, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	id := async.StableID("inventory", "wrongtype")
	index, _ := results.Connection.PartitionIndex("workflow", connector.Partition(id), "records-v1")
	if err := results.Connection.Client().Set(ctx, index, "malformed fixture", 0).Err(); err != nil {
		t.Fatal(err)
	}
	graph := inventoryGraph(id)
	graph.State = async.Running
	if err := workflows.CreateGraph(ctx, graph, nil); err == nil {
		t.Fatal("wrong inventory type accepted")
	}
	if _, err := workflows.ReadGraph(ctx, id); !errors.Is(err, async.ErrNotFound) {
		t.Fatal("graph committed despite index error", err)
	}
	// This exact key belongs to this test's ephemeral namespace.
	if err := results.Connection.Client().Del(ctx, index).Err(); err != nil {
		t.Fatal(err)
	}
	if err := workflows.CreateGraph(ctx, graph, nil); err != nil {
		t.Fatal(err)
	}
	if err := results.Connection.Client().Del(ctx, index).Err(); err != nil {
		t.Fatal(err)
	}
	if err := results.Connection.Client().Set(ctx, index, "malformed fixture", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := workflows.CancelGraph(ctx, id, "", func(g async.Graph) (async.Graph, []async.Intent, error) { g.State = async.Revoked; return g, nil, nil }); err == nil {
		t.Fatal("wrong inventory type accepted during update")
	}
	after, err := workflows.ReadGraph(ctx, id)
	if err != nil || after.Revision != 1 || after.State != async.Running || after.CancelRequested {
		t.Fatal("graph partially changed before index failure", err)
	}
}

func TestRealRedisWorkflowReconcilerReportsMissingGraphAndContinues(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	registry := async.NewRegistry()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	missing := async.StableID("inventory", "missing")
	healthy := async.StableID("inventory", "healthy")
	for _, id := range []string{missing, healthy} {
		if err := workflows.CreateGraph(ctx, inventoryGraph(id), nil); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate one unreplicated record loss while retaining its partition index.
	if err := results.Connection.Client().Del(ctx, results.Connection.PartitionKey("workflow", missing, "state")).Err(); err != nil {
		t.Fatal(err)
	}
	runner := &async.WorkflowReconciler{Client: client, InventoryLookups: 64}
	sawGap, sawHealthy, complete := false, false, false
	for range 5 {
		report, err := runner.Reconcile(ctx)
		if errors.Is(err, async.ErrWorkflowStateGap) {
			if report.WorkflowID != missing || !report.MissingWorkflow {
				t.Fatal(report)
			}
			sawGap = true
		} else if err != nil {
			t.Fatal(err)
		}
		if report.WorkflowID == healthy {
			if report.Repair.State != async.Succeeded {
				t.Fatal(report)
			}
			sawHealthy = true
		}
		if report.PassComplete {
			complete = true
			break
		}
	}
	if !sawGap || !sawHealthy || !complete {
		t.Fatal("gap starved later recovery", sawGap, sawHealthy, complete)
	}
	index, _ := results.Connection.PartitionIndex("workflow", connector.Partition(missing), "records-v1")
	if _, err := results.Connection.Client().ZScore(ctx, index, missing).Result(); err != nil {
		t.Fatal("reconciler silently deleted missing authority", err)
	}
	if _, err := workflows.ReadGraph(ctx, missing); !errors.Is(err, async.ErrNotFound) {
		t.Fatal("reconciler invented missing workflow", err)
	}
}

func TestRealRedisWorkflowIntentPreflightPreventsPartialCreateAndUpdate(t *testing.T) {
	ctx := context.Background()
	_, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	for _, operation := range []string{"create", "update"} {
		for _, invalid := range []string{"missing source", "wrong source", "invalid id", "duplicate id"} {
			t.Run(operation+"/"+invalid, func(t *testing.T) {
				id := async.StableID("intent-preflight", operation+invalid)
				graph := inventoryGraph(id)
				graph.State = async.Running
				if operation == "update" {
					if err := workflows.CreateGraph(ctx, graph, nil); err != nil {
						t.Fatal(err)
					}
				}
				pending, _ := results.Connection.PartitionIndex("workflow", connector.Partition(id), "pending-intents")
				inventory, _ := results.Connection.PartitionIndex("workflow", connector.Partition(id), "records-v1")
				keys := []string{results.Connection.PartitionKey("workflow", id, "state"), results.Connection.PartitionKey("workflow", id, "intents"), pending, inventory}
				snapshot := func() []string {
					out := []string{}
					for _, key := range keys {
						raw, err := results.Connection.Client().Dump(ctx, key).Result()
						if err != nil && !errors.Is(err, redigo.Nil) {
							t.Fatal(err)
						}
						out = append(out, raw)
					}
					return out
				}
				before := snapshot()
				good := async.Intent{ID: async.StableID(id, "first"), SourceID: id, Kind: "unpin", TargetID: async.StableID(id, "target")}
				bad := good
				bad.ID = async.StableID(id, "second")
				switch invalid {
				case "missing source":
					bad.SourceID = ""
				case "wrong source":
					bad.SourceID = async.StableID(id, "foreign")
				case "invalid id":
					bad.ID = "invalid"
				case "duplicate id":
					bad.ID = good.ID
				}
				var err error
				if operation == "create" {
					err = workflows.CreateGraph(ctx, graph, []async.Intent{good, bad})
				} else {
					err = workflows.CancelGraph(ctx, id, "", func(g async.Graph) (async.Graph, []async.Intent, error) {
						g.State = async.Revoked
						return g, []async.Intent{good, bad}, nil
					})
				}
				if !errors.Is(err, async.ErrInvalid) {
					t.Fatal("malformed intent accepted", err)
				}
				after := snapshot()
				for i := range before {
					if before[i] != after[i] {
						t.Fatal("invalid second intent partially persisted", keys[i])
					}
				}
			})
		}
	}
}
