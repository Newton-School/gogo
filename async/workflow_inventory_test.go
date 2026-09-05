package async_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func TestWorkflowInventoryPagesAreBoundedAndResumable(t *testing.T) {
	ctx := context.Background()
	store := fakes.NewMemory()
	wanted := map[string]bool{}
	for index := range 41 {
		id := async.StableID("inventory", string(rune(index)))
		wanted[id] = true
		if err := store.CreateGraph(ctx, async.Graph{ID: id, Revision: 1, State: async.Succeeded, Kind: "group", Members: map[string]async.Completion{}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	cursor := ""
	seen := map[string]bool{}
	for range 10 {
		page, err := store.ListWorkflows(ctx, cursor, 7)
		if err != nil || len(page.IDs) > 7 {
			t.Fatal(page, err)
		}
		for _, id := range page.IDs {
			if seen[id] || !wanted[id] {
				t.Fatal("duplicate or foreign inventory ID")
			}
			seen[id] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != len(wanted) || cursor != "" {
		t.Fatal("inventory pass skipped records")
	}
	for _, cursor := range []string{"arbitrary", "memory-v1:", "memory-v1:../../x"} {
		if _, err := store.ListWorkflows(ctx, cursor, 7); !errors.Is(err, async.ErrInvalid) {
			t.Fatal(err)
		}
	}
}

func TestWorkflowReconcilerAdvancesPastDeniedAndActiveWork(t *testing.T) {
	ctx := context.Background()
	task, _, worker, store := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{})
	mode := "setup"
	denied := ""
	client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: store, Results: store, Workflows: store, Authorize: func(_ context.Context, operation, scope, id string) error {
		if mode == "setup" {
			return nil
		}
		if operation == "reconcile_inventory" {
			if scope != "" || id != "" {
				t.Fatal("inventory grant received object scope")
			}
			return nil
		}
		if operation != "reconcile" {
			t.Fatal("unexpected grant", operation)
		}
		if id == denied {
			return async.ErrDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	one, _ := task.Signature(1)
	ids := []string{}
	for range 3 {
		group, err := client.ApplyCanvas(ctx, async.Group(one, one))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, group.ID)
	}
	sort.Strings(ids)
	denied = ids[0]
	// Publish every workflow, but leave the middle one's tasks active to
	// verify a bounded pass still reaches the final workflow's terminal tasks.
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{withoutCompletions{store}}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	last, err := store.ReadGraph(ctx, ids[2])
	if err != nil {
		t.Fatal(err)
	}
	lastIDs := map[string]bool{}
	for _, e := range last.Children {
		lastIDs[e.ID] = true
	}
	for {
		delivery, err := store.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
		if errors.Is(err, async.ErrNotFound) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		e, err := async.DecodeEnvelope(delivery.Body)
		if err != nil {
			t.Fatal(err)
		}
		if lastIDs[e.ID] {
			if err := worker.Process(ctx, delivery); err != nil {
				t.Fatal(err)
			}
		}
	}
	mode = "repair"
	runner := &async.WorkflowReconciler{Client: client, TaskBatchSize: 1}
	seen := map[string]int{}
	for range 8 {
		report, err := runner.Reconcile(ctx)
		if err != nil && !errors.Is(err, async.ErrDenied) {
			t.Fatal(report, err)
		}
		seen[report.WorkflowID]++
		if report.PassComplete {
			break
		}
	}
	if seen[ids[0]] != 1 || seen[ids[1]] != 2 || seen[ids[2]] != 2 || !runner.Position().EndOfPass {
		t.Fatal("workflow starvation", seen, runner.Position())
	}
	after, err := store.ReadGraph(ctx, ids[2])
	if err != nil || after.State != async.Succeeded {
		t.Fatal("later completed workflow not repaired", err)
	}
	active, err := store.ReadGraph(ctx, ids[1])
	if err != nil || active.State != async.Running || len(active.Members) != 0 {
		t.Fatal("active workflow fabricated completion", err)
	}
}

type inventoryStub struct {
	async.WorkflowStore
	list func(context.Context, string, int) (async.WorkflowInventoryPage, error)
}

func (s inventoryStub) ListWorkflows(ctx context.Context, cursor string, limit int) (async.WorkflowInventoryPage, error) {
	return s.list(ctx, cursor, limit)
}

func TestWorkflowReconcilerRequiresInventoryGrantBeforeProviderAndRejectsBadPages(t *testing.T) {
	for _, mode := range []string{"nil policy", "denied", "outage", "cancel then allow", "too many IDs", "invalid ID", "stuck cursor", "provider payload with error"} {
		t.Run(mode, func(t *testing.T) {
			worker, store, graph := completedRepairFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			backend := inventoryStub{WorkflowStore: store, list: func(context.Context, string, int) (async.WorkflowInventoryPage, error) {
				calls++
				page := async.WorkflowInventoryPage{IDs: []string{graph.ID}, NextCursor: "next"}
				switch mode {
				case "too many IDs":
					page.IDs = append(page.IDs, async.StableID(graph.ID, "other"))
				case "invalid ID":
					page.IDs = []string{"invalid"}
				case "stuck cursor":
					page.NextCursor = "current"
				case "provider payload with error":
					return page, errors.New("private outage")
				}
				return page, nil
			}}
			policy := func(context.Context, string, string, string) error {
				switch mode {
				case "denied":
					return async.ErrDenied
				case "outage":
					return errors.New("private policy")
				case "cancel then allow":
					cancel()
				}
				return nil
			}
			if mode == "nil policy" {
				policy = nil
			}
			client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: store, Results: store, Workflows: backend, Authorize: policy})
			if err != nil {
				t.Fatal(err)
			}
			runner := &async.WorkflowReconciler{Client: client, Cursor: async.WorkflowReconcileCursor{Inventory: "current"}}
			report, err := runner.Reconcile(ctx)
			want := async.ErrUnavailable
			if mode == "nil policy" || mode == "denied" {
				want = async.ErrDenied
			}
			if mode == "cancel then allow" {
				want = context.Canceled
			}
			if !errors.Is(err, want) || report.WorkflowID != "" || runner.Position().Inventory != "current" {
				t.Fatal(report, err)
			}
			if (mode == "nil policy" || mode == "denied" || mode == "outage" || mode == "cancel then allow") && calls != 0 {
				t.Fatal("unauthorized inventory read")
			}
		})
	}
}

func TestWorkflowReconcilerLimitsEmptyInventoryReadsAndRejectsConcurrentCalls(t *testing.T) {
	worker, store, _ := completedRepairFixture(t)
	calls := 0
	entered := make(chan struct{})
	release := make(chan struct{})
	backend := inventoryStub{WorkflowStore: store, list: func(ctx context.Context, _ string, _ int) (async.WorkflowInventoryPage, error) {
		calls++
		if calls == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return async.WorkflowInventoryPage{}, ctx.Err()
			}
		}
		return async.WorkflowInventoryPage{NextCursor: string(rune('a' + calls))}, nil
	}}
	client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: store, Results: store, Workflows: backend, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	runner := &async.WorkflowReconciler{Client: client, InventoryLookups: 3}
	done := make(chan error, 1)
	go func() { _, err := runner.Reconcile(context.Background()); done <- err }()
	<-entered
	if _, err := runner.Reconcile(context.Background()); !errors.Is(err, async.ErrBusy) {
		t.Fatal("concurrent cursor mutation allowed", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls != 3 || runner.Position().Inventory != "d" {
		t.Fatal("empty partitions exceeded bounded work", calls, runner.Position())
	}
}

func TestWorkflowReconcilerPersistsPartialCursorAcrossRunnerRestart(t *testing.T) {
	ctx := context.Background()
	task, _, worker, store := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{})
	client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: store, Results: store, Workflows: store, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	one, _ := task.Signature(1)
	group, err := client.ApplyCanvas(ctx, async.Group(one, one))
	if err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{withoutCompletions{store}}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := worker.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	runner := &async.WorkflowReconciler{Client: client, TaskBatchSize: 1}
	first, err := runner.Reconcile(ctx)
	if err != nil || first.Repair.Converged != 1 || first.PassComplete || first.Cursor.WorkflowID != group.ID || first.Cursor.AfterTaskID == "" {
		t.Fatal(first, err)
	}
	restarted := &async.WorkflowReconciler{Client: client, TaskBatchSize: 1, Cursor: runner.Position()}
	last, err := restarted.Reconcile(ctx)
	if err != nil || last.Repair.Converged != 1 || last.Repair.State != async.Succeeded || !last.PassComplete || last.InventoryPages != 0 {
		t.Fatal(last, err)
	}
}

func TestWorkflowReconcilerDiagnosticsMayReadPositionInsideCallbacks(t *testing.T) {
	worker, store, _ := completedRepairFixture(t)
	var runner *async.WorkflowReconciler
	callbacks := 0
	check := func() {
		callbacks++
		if got := runner.Position(); got.Inventory != "initial" {
			t.Error("callback observed unpublished cursor", got)
		}
	}
	backend := inventoryStub{WorkflowStore: store, list: func(context.Context, string, int) (async.WorkflowInventoryPage, error) {
		check()
		return async.WorkflowInventoryPage{}, nil
	}}
	client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: store, Results: store, Workflows: backend, Authorize: func(context.Context, string, string, string) error { check(); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	runner = &async.WorkflowReconciler{Client: client, Cursor: async.WorkflowReconcileCursor{Inventory: "initial"}}
	done := make(chan error, 1)
	go func() { _, err := runner.Reconcile(context.Background()); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Position deadlocked inside maintenance callback")
	}
	if callbacks < 3 || !runner.Position().EndOfPass {
		t.Fatal("callbacks or cursor publication missing", callbacks)
	}
}
