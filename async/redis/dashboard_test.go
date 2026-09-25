package redis_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
	connector "github.com/Newton-School/gogo/connectors/redis"
)

func TestRealDashboardDiscoveryAndBeatHealth(t *testing.T) {
	broker, results := backends(t)
	ctx := context.Background()
	workers := &adapter.Workers{Connection: results.Connection}
	schedules := &adapter.Schedules{Connection: results.Connection}
	beats := &adapter.Beats{Connection: results.Connection}
	workflows := &adapter.Workflows{Connection: results.Connection}
	catalog := &adapter.DashboardCatalog{Results: results, Workers: workers, Schedules: schedules, Beats: beats, Workflows: workflows}
	e := taskEnvelope(t)
	if err := results.Register(ctx, e, async.Queued); err != nil {
		t.Fatal(err)
	}
	reg := async.NewRegistry()
	task, err := async.Register(reg, "test.run", 1, func(context.Context, async.TaskContext, int) (int, error) { return 2, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sig, err := task.Signature(1)
	if err != nil {
		t.Fatal(err)
	}
	p := async.PeriodicSchedule{ID: async.StableID("dashboard", "periodic"), Signature: sig, Rule: async.Every(time.Minute), Enabled: true, NextDue: time.Now().Add(time.Minute), Misfire: "skip", Overlap: "allow", CatchUpLimit: 1, Revision: 1}
	if err := schedules.UpsertSchedule(ctx, p, 0); err != nil {
		t.Fatal(err)
	}
	if stored, err := catalog.ReadDashboardSchedule(ctx, p.ID); err != nil || stored.ID != p.ID {
		t.Fatal(stored, err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: reg, Broker: broker, Results: results, Workflows: workflows})
	if err != nil {
		t.Fatal(err)
	}
	group, err := client.ApplyCanvas(ctx, async.Group(sig))
	if err != nil {
		t.Fatal(err)
	}
	lease := async.WorkerLease{WorkerID: "dashboard-worker", Token: async.StableID("test", "lease")}
	snapshot := async.WorkerSnapshot{ID: lease.WorkerID, Concurrency: 1, Queues: []string{"default"}, At: time.Now().UTC()}
	if err := workers.ClaimWorker(ctx, lease, snapshot, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := beats.ObserveBeat(ctx, "dashboard-beat", "online", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for kind, want := range map[string]string{"tasks": e.ID, "workers": lease.WorkerID, "schedulers": "dashboard-beat", "beat": p.ID, "workflows": group.ID} {
		var ids []string
		cursor := ""
		for count := 0; count < 128; count++ {
			page, err := catalog.ListDashboard(ctx, kind, cursor, 1)
			if err != nil {
				t.Fatal(kind, err)
			}
			if page.Validate(kind, 1) != nil {
				t.Fatal(page)
			}
			ids = append(ids, page.IDs...)
			if page.NextCursor == "" {
				break
			}
			if page.NextCursor == cursor {
				t.Fatal("no cursor progress")
			}
			cursor = page.NextCursor
		}
		if !reflect.DeepEqual(ids, []string{want}) {
			t.Fatal(kind, ids)
		}
	}
	for _, cursor := range []string{"dash1/tasks/64/", "dash1/other/0/", "bad", "dash1/tasks/0/not-an-id"} {
		if _, err := catalog.ListDashboard(ctx, "tasks", cursor, 10); err == nil {
			t.Fatal(cursor)
		}
	}
	time.Sleep(5 * time.Millisecond)
	health, err := catalog.ReadDashboardBeat(ctx, "dashboard-beat")
	if err != nil || health.Status != "lost" {
		t.Fatal(health, err)
	}
	if err := beats.ObserveBeat(ctx, "dashboard-beat", "error", time.Second); err != nil {
		t.Fatal(err)
	}
	health, err = catalog.ReadDashboardBeat(ctx, "dashboard-beat")
	if err != nil || health.Status != "error" {
		t.Fatal(health, err)
	}
	// The presence index is part of the same write preflight, never a partial
	// successful claim followed by an index error.
	id := "corrupt-index-worker"
	index, _ := results.Connection.PartitionIndex("worker", connector.Partition(id), "dashboard-v1")
	if err := results.Connection.Client().Del(ctx, index).Err(); err != nil {
		t.Fatal(err)
	}
	if err := results.Connection.Client().Set(ctx, index, "wrong type", 0).Err(); err != nil {
		t.Fatal(err)
	}
	snapshot.ID = id
	if err := workers.ClaimWorker(ctx, async.WorkerLease{WorkerID: id, Token: lease.Token}, snapshot, time.Minute); err == nil {
		t.Fatal("wrong type accepted")
	}
	if _, err := workers.LookupWorker(ctx, id); err != async.ErrNotFound {
		t.Fatal("partial presence write", err)
	}
}
