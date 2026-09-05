package async_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

type withoutCompletions struct{ async.IntentStore }

func (s withoutCompletions) ListIntents(ctx context.Context, limit int) ([]async.Intent, error) {
	items, err := s.IntentStore.ListIntents(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := []async.Intent{}
	for _, item := range items {
		if item.Kind != "completion" {
			out = append(out, item)
		}
	}
	return out, nil
}

func TestWorkflowRepairCursorDoesNotStarveTerminalTasksBehindActiveTasks(t *testing.T) {
	ctx := context.Background()
	task, _, worker, store := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{})
	client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: store, Results: store, Workflows: store, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	one, _ := task.Signature(1)
	group, err := client.ApplyCanvas(ctx, async.Group(one, one, one))
	if err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{withoutCompletions{store}}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	graph, err := group.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, child := range graph.Children {
		ids = append(ids, child.ID)
	}
	sort.Strings(ids)
	for range ids {
		delivery := take(t, store)
		envelope, err := async.DecodeEnvelope(delivery.Body)
		if err != nil {
			t.Fatal(err)
		}
		if envelope.ID == ids[len(ids)-1] {
			if err := worker.Process(ctx, delivery); err != nil {
				t.Fatal(err)
			}
		}
	}
	cursor := ""
	total := 0
	for index := range ids {
		report, err := client.ReconcileWorkflow(ctx, group.ID, async.WorkflowRepairOptions{AfterTaskID: cursor, BatchSize: 1})
		if err != nil || report.Checked != 1 {
			t.Fatal(report, err)
		}
		total += report.Converged
		cursor = report.NextTaskID
		if index < len(ids)-1 && cursor == "" {
			t.Fatal("cursor ended before bounded pass reached last task")
		}
	}
	if total != 1 || cursor != "" {
		t.Fatal("later terminal task starved", total, cursor)
	}
	graph, err = group.Snapshot(ctx)
	if err != nil || len(graph.Members) != 1 || graph.State != async.Running {
		t.Fatal("active tasks fabricated as completed", err)
	}
}

func TestWorkflowRepairRebuildsVerifiedCompletionAcrossCanvasKinds(t *testing.T) {
	for _, kind := range []string{"group", "chain", "chord", "nested", "failed group"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			registry := async.NewRegistry()
			calls, bodyCalls := 0, 0
			task, err := async.Register(registry, "test.member", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) {
				calls++
				if kind == "failed group" && n == 2 {
					return 0, errors.New("private application failure")
				}
				return n + 1, nil
			}, async.TaskOptions{})
			if err != nil {
				t.Fatal(err)
			}
			body, err := async.Register(registry, "test.body", 1, func(_ context.Context, _ async.TaskContext, values []int) (int, error) {
				bodyCalls++
				total := 0
				for _, v := range values {
					total += v
				}
				return total, nil
			}, async.TaskOptions{})
			if err != nil {
				t.Fatal(err)
			}
			store := fakes.NewMemory()
			client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: store, Results: store, Workflows: store, Authorize: func(context.Context, string, string, string) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			worker := &async.Worker{Registry: registry, Broker: store, Results: store, ID: "repair-worker"}
			one, _ := task.Signature(1)
			two, _ := task.Signature(2)
			callback, _ := body.Signature(nil)
			canvas := async.Group(one, two)
			switch kind {
			case "chain":
				canvas = async.Chain(one, two)
			case "chord":
				canvas, err = async.Chord(canvas, callback)
				if err != nil {
					t.Fatal(err)
				}
			case "nested":
				canvas = async.Parallel(one.Canvas(), two.Canvas()).Then(callback.Canvas())
			}
			group, err := client.ApplyCanvas(ctx, canvas)
			if err != nil {
				t.Fatal(err)
			}
			relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{withoutCompletions{store}}, ID: "repair-relay"}
			converged := 0
			for turn := 0; turn < 6; turn++ {
				if err := relay.Tick(ctx); err != nil {
					t.Fatal(err)
				}
				for {
					delivery, err := store.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "repair-worker"})
					if errors.Is(err, async.ErrNotFound) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if err := worker.Process(ctx, delivery); err != nil {
						t.Fatal(err)
					}
				}
				report, err := client.ReconcileWorkflow(ctx, group.ID, async.WorkflowRepairOptions{})
				if err != nil {
					t.Fatal(report, err)
				}
				converged += report.Converged
				if report.State.Terminal() {
					break
				}
			}
			graph, err := group.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wanted := async.Succeeded
			if kind == "failed group" {
				wanted = async.Failed
			}
			if graph.State != wanted || calls != 2 || converged < 2 {
				t.Fatal("barrier repair failed or executed task", graph.State, calls, converged)
			}
			if (kind == "chord" || kind == "nested") && bodyCalls != 1 {
				t.Fatal("callback did not run once", bodyCalls)
			}
			// Pins are released only by the ordinary durable unpin intent,
			// never directly by the cross-store maintenance operation.
			last := graph.Children[len(graph.Children)-1]
			record, err := store.Lookup(ctx, last.ID)
			if err != nil || !record.Pinned {
				t.Fatal("repair released pin outside relay", record.Pinned, err)
			}
			relay.Sources = []async.IntentStore{store}
			if err := relay.Tick(ctx); err != nil {
				t.Fatal("duplicate original completion failed", err)
			}
			after, err := group.Snapshot(ctx)
			if err != nil || after.State != graph.State || calls != 2 {
				t.Fatal("original completion redelivery changed outcome", err)
			}
			if report, err := client.ReconcileWorkflow(ctx, group.ID, async.WorkflowRepairOptions{}); err != nil || report.Converged != 0 || report.State != wanted {
				t.Fatal("terminal repair was not a no-op", report, err)
			}
		})
	}
}

type repairResultStore struct {
	*fakes.Memory
	target  string
	mode    string
	lookups int
}

func (s *repairResultStore) Lookup(ctx context.Context, id string) (async.Record, error) {
	s.lookups++
	record, err := s.Memory.Lookup(ctx, id)
	if id != s.target {
		return record, err
	}
	switch s.mode {
	case "missing":
		return async.Record{}, async.ErrNotFound
	case "output":
		record.Output = nil
	case "identity":
		record.Digest = "wrong"
	case "outage":
		return record, errors.New("private backend detail")
	}
	return record, err
}

type repairWorkflowStore struct {
	async.WorkflowStore
	beforeMember func()
	mutateRead   func(*async.Graph)
}

func (s *repairWorkflowStore) ReadGraph(ctx context.Context, id string) (async.Graph, error) {
	graph, err := s.WorkflowStore.ReadGraph(ctx, id)
	if err == nil && s.mutateRead != nil {
		s.mutateRead(&graph)
	}
	return graph, err
}

func (s *repairWorkflowStore) RecordMember(ctx context.Context, id string, completion async.Completion, advance func(async.Graph) (async.Graph, []async.Intent, error)) error {
	if s.beforeMember != nil {
		s.beforeMember()
	}
	return s.WorkflowStore.RecordMember(ctx, id, completion, advance)
}

func completedRepairFixture(t *testing.T) (*async.Worker, *fakes.Memory, async.Graph) {
	t.Helper()
	ctx := context.Background()
	task, _, worker, store := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{})
	client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: store, Results: store, Workflows: store, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	signature, _ := task.Signature(1)
	signature = signature.Set(async.WithScope("tenant-a", "operator"))
	group, err := client.ApplyCanvas(ctx, async.Group(signature))
	if err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{withoutCompletions{store}}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	graph, err := store.ReadGraph(ctx, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	return worker, store, graph
}

func TestWorkflowRepairRequiresExplicitScopeAndRechecksAuthorityAtCAS(t *testing.T) {
	for _, mode := range []string{"nil", "workflow denied", "child denied", "policy outage", "canceled callback", "CAS denied", "CAS outage", "CAS canceled"} {
		t.Run(mode, func(t *testing.T) {
			worker, store, before := completedRepairFixture(t)
			results := &repairResultStore{Memory: store}
			workflows := &repairWorkflowStore{WorkflowStore: store}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			inCAS := false
			workflows.beforeMember = func() { inCAS = true }
			policy := func(_ context.Context, operation, scope, id string) error {
				if operation != "reconcile" || scope != "tenant-a" || id != before.ID && id != before.Children[0].ID {
					t.Fatal("wrong operation, scope or identity", operation, scope, id)
				}
				switch mode {
				case "workflow denied":
					return async.ErrDenied
				case "child denied":
					if id != before.ID {
						return async.ErrDenied
					}
				case "policy outage":
					return errors.New("private policy outage")
				case "canceled callback":
					cancel()
				case "CAS denied":
					if inCAS {
						return async.ErrDenied
					}
				case "CAS outage":
					if inCAS {
						return errors.New("private policy outage")
					}
				case "CAS canceled":
					if inCAS {
						cancel()
					}
				}
				return nil
			}
			if mode == "nil" {
				policy = nil
			}
			client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: store, Results: results, Workflows: workflows, Authorize: policy})
			if err != nil {
				t.Fatal(err)
			}
			report, err := client.ReconcileWorkflow(ctx, before.ID, async.WorkflowRepairOptions{})
			wanted := async.ErrDenied
			if mode == "policy outage" || mode == "CAS outage" {
				wanted = async.ErrUnavailable
			}
			if mode == "canceled callback" || mode == "CAS canceled" {
				wanted = context.Canceled
			}
			if !errors.Is(err, wanted) || report.Converged != 0 {
				t.Fatal(report, err)
			}
			after, err := store.ReadGraph(context.Background(), before.ID)
			if err != nil || after.Revision != before.Revision || len(after.Members) != 0 {
				t.Fatal("denied repair mutated graph", err)
			}
			if !inCAS && results.lookups != 0 {
				t.Fatal("denied operation read task state")
			}
		})
	}
}

func TestWorkflowRepairRejectsCorruptDurableGraphsBeforeTaskReads(t *testing.T) {
	mutations := map[string]func(*async.Graph){
		"nil members":       func(g *async.Graph) { g.Members = nil },
		"wrong child scope": func(g *async.Graph) { g.Children[0].Scope = "tenant-b" },
		"duplicate child":   func(g *async.Graph) { g.Children = append(g.Children, g.Children[0]) },
		"wrong workflow":    func(g *async.Graph) { g.Children[0].WorkflowID = async.StableID(g.ID, "other") },
		"bad state":         func(g *async.Graph) { g.State = async.Queued },
		"unknown member": func(g *async.Graph) {
			id := async.StableID(g.ID, "unknown")
			g.Members[id] = async.Completion{ID: id, State: async.Failed}
		},
		"chain bounds":           func(g *async.Graph) { g.Kind = "chain"; g.Next = 5000 },
		"chord missing callback": func(g *async.Graph) { g.Kind = "chord" },
		"member still running": func(g *async.Graph) {
			id := g.Children[0].ID
			g.Members[id] = async.Completion{ID: id, State: async.Running}
		},
		"member wrong identity": func(g *async.Graph) {
			id := g.Children[0].ID
			g.Members[id] = async.Completion{ID: async.StableID(id, "other"), State: async.Succeeded, Output: json.RawMessage(`1`)}
		},
		"member invalid JSON": func(g *async.Graph) {
			id := g.Children[0].ID
			g.Members[id] = async.Completion{ID: id, State: async.Succeeded, Output: json.RawMessage(`{`)}
		},
		"chord scope mismatch": func(g *async.Graph) {
			g.Kind = "chord"
			signature := g.Signatures[0]
			signature.Options.Scope = "tenant-b"
			g.Callback = &signature
			g.CallbackID = async.StableID(g.ID, "body")
		},
		"chord unclaimed completion": func(g *async.Graph) {
			g.Kind = "chord"
			signature := g.Signatures[0]
			g.Callback = &signature
			g.CallbackID = async.StableID(g.ID, "body")
			g.Members[g.CallbackID] = async.Completion{ID: g.CallbackID, State: async.Succeeded, Output: json.RawMessage(`1`)}
		},
		"chord claimed missing envelope": func(g *async.Graph) {
			g.Kind = "chord"
			signature := g.Signatures[0]
			g.Callback = &signature
			g.CallbackID = async.StableID(g.ID, "body")
			g.CallbackClaimed = true
		},
		"JSON marshal failure": func(g *async.Graph) { g.Children[0].ETA = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"DAG forward dependency": func(g *async.Graph) {
			g.Kind = "dag"
			g.RootNode = g.Children[0].ID
			g.Plan = []async.CanvasNode{{ID: g.RootNode, Kind: "task", Dependencies: []string{g.RootNode}}}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			worker, store, graph := completedRepairFixture(t)
			results := &repairResultStore{Memory: store}
			workflows := &repairWorkflowStore{WorkflowStore: store, mutateRead: mutate}
			client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: store, Results: results, Workflows: workflows, Authorize: func(context.Context, string, string, string) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			if report, err := client.ReconcileWorkflow(context.Background(), graph.ID, async.WorkflowRepairOptions{}); !errors.Is(err, async.ErrUnavailable) || report.Converged != 0 {
				t.Fatal(report, err)
			}
			after, err := store.ReadGraph(context.Background(), graph.ID)
			if err != nil || after.Revision != graph.Revision || results.lookups != 0 {
				t.Fatal("corrupt graph read tasks or mutated authority", err)
			}
		})
	}
}

func TestWorkflowRepairUsesConcurrentCancellationWithoutOverwritingActualOutcome(t *testing.T) {
	worker, store, before := completedRepairFixture(t)
	workflows := &repairWorkflowStore{WorkflowStore: store}
	client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: store, Results: store, Workflows: workflows, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	group := async.RestoreGroup(client, before.ID)
	workflows.beforeMember = func() {
		if err := group.Revoke(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	report, err := client.ReconcileWorkflow(context.Background(), group.ID, async.WorkflowRepairOptions{})
	if err != nil || report.State != async.Revoked || report.Converged != 1 {
		t.Fatal(report, err)
	}
	after, err := store.ReadGraph(context.Background(), group.ID)
	if err != nil || !after.CancelRequested || after.Members[before.Children[0].ID].State != async.Succeeded {
		t.Fatal("concurrent cancellation lost actual task outcome", err)
	}
	record, err := store.Lookup(context.Background(), before.Children[0].ID)
	if err != nil || record.State != async.Succeeded || !record.Pinned {
		t.Fatal("repair rewrote terminal result or unpinned directly", err)
	}
}

func TestWorkflowRepairReportsGapsAndRejectsWrongSourceWithoutFabrication(t *testing.T) {
	for _, mode := range []string{"missing", "output", "identity", "outage"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			task, _, worker, store := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{})
			results := &repairResultStore{Memory: store, mode: mode}
			client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: store, Results: results, Workflows: store, Authorize: func(context.Context, string, string, string) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			one, _ := task.Signature(1)
			group, err := client.ApplyCanvas(ctx, async.Group(one))
			if err != nil {
				t.Fatal(err)
			}
			relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{withoutCompletions{store}}, ID: "relay"}
			if err := relay.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if err := worker.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			graph, err := group.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			results.target = graph.Children[0].ID
			report, err := client.ReconcileWorkflow(ctx, group.ID, async.WorkflowRepairOptions{})
			wanted := async.ErrUnavailable
			if mode == "missing" || mode == "output" {
				wanted = async.ErrWorkflowStateGap
			}
			if !errors.Is(err, wanted) || report.Converged != 0 {
				t.Fatal(report, err)
			}
			after, err := group.Snapshot(ctx)
			if err != nil || after.Revision != graph.Revision || len(after.Members) != 0 || after.State != async.Running {
				t.Fatal("missing/untrusted source fabricated graph progress", err)
			}
			record, err := store.Lookup(ctx, results.target)
			if err != nil || !record.Pinned {
				t.Fatal("gap removed live pin", err)
			}
		})
	}
}
