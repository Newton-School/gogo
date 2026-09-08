package async_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

type groupForgetStore struct {
	async.WorkflowStore
	forget func(context.Context, string, string, uint64) error
}

func (s *groupForgetStore) ForgetGraphPayload(ctx context.Context, id, scope string, revision uint64) error {
	return s.forget(ctx, id, scope, revision)
}

type groupForgetFixture struct {
	graph                    async.Graph
	memory                   *fakes.Memory
	records                  map[string]async.Record
	results                  *resultBoundaryStore
	workflows                *groupForgetStore
	client                   *async.Client
	group                    *async.GroupResult
	childWrites, graphWrites int
	grant                    func(context.Context, string, string, string) error
}

func newGroupForgetFixture(t *testing.T, count int) *groupForgetFixture {
	t.Helper()
	f := &groupForgetFixture{graph: groupBoundaryGraph(t, count), memory: fakes.NewMemory(), records: map[string]async.Record{}}
	if err := f.memory.CreateGraph(context.Background(), f.graph, nil); err != nil {
		t.Fatal(err)
	}
	for _, child := range f.graph.Children {
		f.records[child.ID] = async.Record{Envelope: child, Revision: 1, Digest: child.Digest(), State: async.Succeeded, Output: json.RawMessage("1"), Progress: json.RawMessage("1")}
	}
	f.results = &resultBoundaryStore{ResultStore: f.memory, lookup: func(_ context.Context, id string) (async.Record, error) {
		r, ok := f.records[id]
		if !ok {
			return async.Record{}, async.ErrNotFound
		}
		return r, nil
	}, forget: func(_ context.Context, id string) error {
		f.childWrites++
		r, ok := f.records[id]
		if !ok {
			return async.ErrNotFound
		}
		if r.Pinned || !r.State.Terminal() {
			return async.ErrPinned
		}
		r.Output, r.Progress = nil, nil
		r.Revision++
		f.records[id] = r
		return nil
	}}
	f.workflows = &groupForgetStore{WorkflowStore: f.memory, forget: func(ctx context.Context, id, scope string, revision uint64) error {
		f.graphWrites++
		return f.memory.ForgetGraphPayload(ctx, id, scope, revision)
	}}
	var err error
	f.client, err = async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: f.memory, Results: f.results, Workflows: f.workflows, Authorize: func(ctx context.Context, action, scope, id string) error {
		if f.grant != nil {
			return f.grant(ctx, action, scope, id)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.group = async.RestoreGroup(f.client, f.graph.ID)
	return f
}

func TestGroupForgetReleasesGraphCopiesAndChildren(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			f := newGroupForgetFixture(t, count)
			// A child-only delete demonstrably leaves the independently retained
			// aggregate readable. Group Forget must close both public paths.
			for _, child := range f.graph.Children {
				if err := async.RestoreResult[json.RawMessage](f.client, child.ID).Forget(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if values, err := f.group.Join(context.Background()); err != nil || len(values) != count {
				t.Fatal(values, err)
			}
			if err := f.group.Forget(context.Background()); err != nil {
				t.Fatal(err)
			}
			out, err := f.group.Snapshot(context.Background())
			if err != nil || !out.PayloadForgotten || out.State != async.Succeeded || len(out.Output) != 0 || len(out.Members) != count {
				t.Fatal(out, err)
			}
			for id, member := range out.Members {
				if len(member.Output) != 0 || member.State != async.Succeeded || len(f.records[id].Output) != 0 || len(f.records[id].Progress) != 0 {
					t.Fatal("retained payload")
				}
			}
			if values, err := f.group.Join(context.Background()); err != async.ErrResultExpired || len(values) != 0 {
				t.Fatal(values, err)
			}
			if values, err := f.group.JoinOutcomes(context.Background()); err != async.ErrResultExpired || len(values) != 0 {
				t.Fatal(values, err)
			}
			if ready, err := f.group.Ready(context.Background()); err != nil || !ready {
				t.Fatal(ready, err)
			}
			if err := f.group.Forget(context.Background()); err != nil {
				t.Fatal("retry", err)
			}
			for _, member := range f.graph.Members {
				if err := f.memory.RecordMember(context.Background(), f.graph.ID, member, func(async.Graph) (async.Graph, []async.Intent, error) {
					t.Fatal("duplicate completion advanced forgotten graph")
					return async.Graph{}, nil, nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if values, err := f.group.JoinOutcomes(context.Background()); err != async.ErrResultExpired || len(values) != 0 {
				t.Fatal(values, err)
			}
		})
	}
}

func TestGroupForgetPreflightDenialPinsAndInvalidMetadata(t *testing.T) {
	for _, mode := range []string{"group_read", "group_forget", "child_read", "child_forget", "pinned", "wrong_scope", "wrong_task", "wrong_workflow", "wrong_args", "wrong_state", "joined_missing", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			f := newGroupForgetFixture(t, 2)
			last := f.graph.Children[1].ID
			want := async.ErrDenied
			f.grant = func(_ context.Context, action, _, id string) error {
				if mode == "group_"+action && id == f.graph.ID || mode == "child_"+action && id == last {
					return async.ErrDenied
				}
				return nil
			}
			r := f.records[last]
			switch mode {
			case "pinned":
				r.Pinned, want = true, async.ErrPinned
			case "wrong_scope":
				r.Envelope.Scope, want = "other", async.ErrUnavailable
			case "wrong_task":
				r.Envelope.Task, want = "other.task", async.ErrUnavailable
			case "wrong_workflow":
				r.Envelope.WorkflowID, want = async.StableID(last, "other"), async.ErrUnavailable
			case "wrong_args":
				r.Envelope.Args, want = json.RawMessage("2"), async.ErrUnavailable
			case "wrong_state":
				r.State, want = async.Failed, async.ErrUnavailable
			case "joined_missing":
				want = async.ErrUnavailable
				lookup := f.results.lookup
				f.results.lookup = func(ctx context.Context, id string) (async.Record, error) {
					if id == last {
						return async.Record{}, errors.Join(async.ErrNotFound, errors.New("private unavailable"))
					}
					return lookup(ctx, id)
				}
			case "unsupported":
				want = async.ErrUnavailable
				client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: f.memory, Results: f.results, Workflows: struct{ async.WorkflowStore }{f.memory}, Authorize: f.grant})
				if err != nil {
					t.Fatal(err)
				}
				f.group = async.RestoreGroup(client, f.graph.ID)
			}
			r.Digest = r.Envelope.Digest()
			f.records[last] = r
			if err := f.group.Forget(context.Background()); err != want || f.childWrites != 0 || f.graphWrites != 0 {
				t.Fatal(err, f.childWrites, f.graphWrites)
			}
		})
	}
}

func TestGroupForgetPartialProgressAndUnknownFinalReply(t *testing.T) {
	for _, mode := range []string{"late_denial", "child_applied_error", "graph_applied_error", "graph_applied_panic", "confirmed_cancel", "revision_conflict"} {
		t.Run(mode, func(t *testing.T) {
			f := newGroupForgetFixture(t, 2)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			originalChild, originalGraph := f.results.forget, f.workflows.forget
			f.results.forget = func(ctx context.Context, id string) error {
				if err := originalChild(ctx, id); err != nil {
					return err
				}
				if mode == "child_applied_error" {
					return errors.Join(async.ErrDenied, errors.New("private acknowledgement lost"))
				}
				return nil
			}
			f.grant = func(_ context.Context, action, _, _ string) error {
				if mode == "late_denial" && f.childWrites == 1 && action == "forget" {
					return async.ErrDenied
				}
				return nil
			}
			f.workflows.forget = func(ctx context.Context, id, scope string, revision uint64) error {
				if mode == "revision_conflict" {
					return async.ErrConflict
				}
				if err := originalGraph(ctx, id, scope, revision); err != nil {
					return err
				}
				switch mode {
				case "graph_applied_error":
					return errors.Join(async.ErrDenied, errors.New("private acknowledgement lost"))
				case "graph_applied_panic":
					panic("private provider panic")
				case "confirmed_cancel":
					cancel()
				}
				return nil
			}
			want := async.ErrUnavailable
			if mode == "late_denial" {
				want = async.ErrDenied
			}
			if mode == "confirmed_cancel" {
				want = nil
			}
			if mode == "revision_conflict" {
				want = async.ErrConflict
			}
			if err := f.group.Forget(ctx); err != want {
				t.Fatal(err, want)
			}
			if f.childWrites == 0 || len(f.records[f.graph.Children[0].ID].Output) != 0 {
				t.Fatal("partial write not exercised")
			}
			f.grant, f.results.forget, f.workflows.forget = nil, originalChild, originalGraph
			if err := f.group.Forget(context.Background()); err != nil {
				t.Fatal("safe retry", err)
			}
		})
	}
}

func TestGroupForgetFrozenIdentityBeforeContextAndGrantCallbacks(t *testing.T) {
	for _, at := range []string{"context", "grant", "lookup", "child_write"} {
		t.Run(at, func(t *testing.T) {
			f := newGroupForgetFixture(t, 2)
			replacement := newGroupForgetFixture(t, 1)
			var ctx context.Context = context.Background()
			replace := func() { *f.client = *replacement.client; *f.group = *replacement.group }
			switch at {
			case "context":
				ctx = &producerCallbackContext{Context: ctx, callback: replace}
			case "grant":
				f.grant = func(context.Context, string, string, string) error { replace(); return nil }
			case "lookup":
				lookup := f.results.lookup
				f.results.lookup = func(ctx context.Context, id string) (async.Record, error) { replace(); return lookup(ctx, id) }
			case "child_write":
				forget := f.results.forget
				f.results.forget = func(ctx context.Context, id string) error { replace(); return forget(ctx, id) }
			}
			if err := f.group.Forget(ctx); err != nil || f.childWrites != 2 || f.graphWrites != 1 || replacement.childWrites != 0 || replacement.graphWrites != 0 {
				t.Fatal(err, f.childWrites, f.graphWrites, replacement.childWrites, replacement.graphWrites)
			}
		})
	}
}

func TestGroupForgetMarkerAndPureTransitionCompatibility(t *testing.T) {
	original := groupBoundaryGraph(t, 2)
	before, _ := json.Marshal(original)
	if strings.Contains(string(before), "payload_forgotten") {
		t.Fatal("false marker changed existing wire form")
	}
	forgotten, err := async.ForgetGroupPayload(original)
	if err != nil || !forgotten.PayloadForgotten || forgotten.Revision != original.Revision+1 || len(original.Output) == 0 || len(original.Members[original.Children[0].ID].Output) == 0 {
		t.Fatal("transition aliased input", err)
	}
	forgotten.Children[0].Args[0] = '9'
	if string(original.Children[0].Args) != "1" {
		t.Fatal("transition retains mutable source")
	}
	for _, mode := range []string{"running", "chain", "overflow", "marker_output", "marker_member"} {
		t.Run(mode, func(t *testing.T) {
			graph := groupBoundaryGraph(t, 1)
			want := async.ErrUnavailable
			switch mode {
			case "running":
				graph.State, want = async.Running, async.ErrPinned
			case "chain":
				graph.Kind, want = "chain", async.ErrInvalid
			case "overflow":
				graph.Revision, want = math.MaxUint64, async.ErrConflict
			case "marker_output":
				graph.PayloadForgotten = true
			case "marker_member":
				graph.PayloadForgotten, graph.Output = true, nil
			}
			if out, err := async.ForgetGroupPayload(graph); err != want || !reflect.DeepEqual(out, async.Graph{}) {
				t.Fatal(out.ID, err, want)
			}
		})
	}
}

func TestGroupForgetAbsentChildrenAndFinalAuthority(t *testing.T) {
	for _, mode := range []string{"absent", "final_denied", "scope_drift", "member_drift"} {
		t.Run(mode, func(t *testing.T) {
			f := newGroupForgetFixture(t, 2)
			if mode == "absent" {
				delete(f.records, f.graph.Children[1].ID)
			}
			f.grant = func(_ context.Context, action, _, id string) error {
				if mode == "final_denied" && f.childWrites == 2 && id == f.graph.Children[0].ID && action == "forget" {
					return async.ErrDenied
				}
				return nil
			}
			if mode == "scope_drift" || mode == "member_drift" {
				f.workflows.WorkflowStore = &groupBoundaryStore{WorkflowStore: f.memory, read: func(ctx context.Context, id string) (async.Graph, error) {
					g, err := f.memory.ReadGraph(ctx, id)
					if f.childWrites == 2 {
						if mode == "scope_drift" {
							g.Scope = "other"
							for i := range g.Children {
								g.Children[i].Scope = "other"
								g.Signatures[i].Options.Scope = "other"
							}
						} else {
							g.State = async.Failed
							g.Failure = &async.Failure{Code: "FAIL", Message: "Changed state"}
						}
					}
					return g, err
				}}
			}
			want := async.ErrUnavailable
			if mode == "absent" {
				want = nil
			}
			if mode == "final_denied" {
				want = async.ErrDenied
			}
			if err := f.group.Forget(context.Background()); err != want {
				t.Fatal(err, want)
			}
			if mode != "absent" && f.graphWrites != 0 {
				t.Fatal("final graph mutation bypassed current authority/layout")
			}
		})
	}
}

func TestGroupForgetInvalidContextsAndReadDetachment(t *testing.T) {
	f := newGroupForgetFixture(t, 1)
	var typedNil *producerCallbackContext
	for _, ctx := range []context.Context{nil, typedNil} {
		if err := f.group.Forget(ctx); err != async.ErrInvalid {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.group.Forget(ctx); err != context.Canceled {
		t.Fatal(err)
	}
	broken := &producerCallbackContext{Context: context.Background(), callback: func() { panic("private context panic") }}
	if err := f.group.Forget(broken); err != async.ErrUnavailable {
		t.Fatal(err)
	}
	if err := f.group.Forget(groupForgetInvalidContext{context.Background()}); err != async.ErrUnavailable {
		t.Fatal("private/nonconforming context error escaped", err)
	}
	if f.childWrites != 0 || f.graphWrites != 0 {
		t.Fatal("invalid context wrote")
	}
	for _, mode := range []string{"graph", "child"} {
		t.Run(mode, func(t *testing.T) {
			f := newGroupForgetFixture(t, 1)
			ctx := &producerCallbackContext{Context: context.Background()}
			if mode == "graph" {
				graph := f.graph
				f.workflows.WorkflowStore = &groupBoundaryStore{WorkflowStore: f.memory, read: func(context.Context, string) (async.Graph, error) {
					ctx.callback = func() { graph.Output[1] = '9'; member := graph.Members[graph.Children[0].ID]; member.Output[0] = '9' }
					return graph, nil
				}}
				out, err := f.group.Snapshot(ctx)
				if err != nil || string(out.Output) != "[1]" || string(out.Members[out.Children[0].ID].Output) != "1" {
					t.Fatal("context changed provider observation", out.Output, err)
				}
			} else {
				lookup := f.results.lookup
				f.results.lookup = func(ctxArg context.Context, id string) (async.Record, error) {
					r, err := lookup(ctxArg, id)
					ctx.callback = func() { r.Envelope.Args[0] = '9' }
					return r, err
				}
				// Reset the source after each context mutation so the second
				// lookup is still valid, not a deliberately malformed provider.
				f.grant = func(context.Context, string, string, string) error {
					f.records[f.graph.Children[0].ID].Envelope.Args[0] = '1'
					return nil
				}
				if err := f.group.Forget(ctx); err != nil {
					t.Fatal("context changed child observation before detach", err)
				}
			}
		})
	}
}

type groupForgetInvalidContext struct{ context.Context }

func (groupForgetInvalidContext) Err() error { return errors.New("private context detail") }

func TestGroupForgetFailureMetadataAndStreamExpiry(t *testing.T) {
	f := newGroupForgetFixture(t, 1)
	g := f.graph
	g.State, g.Output = async.Failed, nil
	g.Failure = &async.Failure{Code: "GROUP_FAILED", Message: "A member failed"}
	m := g.Members[g.Children[0].ID]
	m.State, m.Output, m.Failure = async.Failed, nil, &async.Failure{Code: "TASK_FAILED", Message: "Task failed"}
	g.Members[m.ID] = m
	next, err := async.ForgetGroupPayload(g)
	if err != nil || !reflect.DeepEqual(next.Failure, g.Failure) || !reflect.DeepEqual(next.Members[m.ID].Failure, m.Failure) {
		t.Fatal(err)
	}
	store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return next, nil }}
	group := async.RestoreGroup(outcomeClient(t, store, func(context.Context, string, string, string) error { return nil }), g.ID)
	if failed, err := group.Failed(context.Background()); err != nil || !failed {
		t.Fatal(failed, err)
	}
	if _, err := group.Join(context.Background()); err != async.ErrResultExpired {
		t.Fatal(err)
	}
	if outcomes, err := group.JoinOutcomes(context.Background()); err != async.ErrResultExpired || len(outcomes) != 0 {
		t.Fatal(outcomes, err)
	}

	// Genuine earlier yields stay genuine when a later poll observes Forget.
	f = newGroupForgetFixture(t, 2)
	f.graph.State = async.Running
	delete(f.graph.Members, f.graph.Children[1].ID)
	reads := 0
	store = &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) {
		reads++
		if reads == 1 {
			return f.graph, nil
		}
		terminal := f.graph
		terminal.State = async.Succeeded
		terminal.Members[f.graph.Children[1].ID] = async.Completion{ID: f.graph.Children[1].ID, State: async.Succeeded}
		return async.ForgetGroupPayload(terminal)
	}}
	group = async.RestoreGroup(outcomeClient(t, store, func(context.Context, string, string, string) error { return nil }), f.graph.ID)
	outcomes, err := group.JoinOutcomes(context.Background())
	if err != async.ErrResultExpired || len(outcomes) != 1 || outcomes[0].ID != f.graph.Children[0].ID {
		t.Fatal(outcomes, err)
	}
}

func TestGroupForgetRetryMetadataAndNoRehydration(t *testing.T) {
	f := newGroupForgetFixture(t, 1)
	id := f.graph.Children[0].ID
	r := f.records[id]
	r.Envelope.Retries, r.Envelope.ETA = 2, r.Envelope.CreatedAt.Add(time.Hour)
	f.records[id] = r // Stable identity digest deliberately excludes these.
	if err := f.group.Forget(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.results.lookup = func(context.Context, string) (async.Record, error) {
		t.Fatal("forgotten reader tried to recover child data")
		return async.Record{}, nil
	}
	if outcomes, err := f.group.JoinOutcomes(context.Background()); err != async.ErrResultExpired || len(outcomes) != 0 {
		t.Fatal(outcomes, err)
	}
}

func TestGroupForgetConcurrentMemoryCommands(t *testing.T) {
	ctx := context.Background()
	task, client, worker, backend := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n, nil }, async.TaskOptions{})
	signature, _ := task.Signature(1)
	group, err := client.ApplyCanvas(ctx, async.Group(signature))
	if err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{backend}, ID: "forget-relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(ctx, take(t, backend)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := relay.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := group.Forget(ctx); err != nil && err != async.ErrConflict {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := group.Forget(ctx); err != nil {
		t.Fatal(err)
	}
	if out, err := group.Join(ctx); err != async.ErrResultExpired || len(out) != 0 {
		t.Fatal(out, err)
	}
}

func FuzzForgetGroupPayload(f *testing.F) {
	f.Add([]byte(`{"id":"123e4567-e89b-42d3-a456-426614174000","kind":"group","members":{},"state":"SUCCEEDED","output":[],"revision":1}`))
	f.Add([]byte(`{"id":"123e4567-e89b-42d3-a456-426614174000","kind":"group","members":{},"state":"SUCCEEDED","payload_forgotten":true,"revision":2}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > async.MaxWorkflowDurableBytes {
			t.Skip()
		}
		var graph async.Graph
		if json.Unmarshal(raw, &graph) != nil {
			return
		}
		before, err := json.Marshal(graph)
		if err != nil {
			return
		}
		out, err := async.ForgetGroupPayload(graph)
		if err != nil {
			if !reflect.DeepEqual(out, async.Graph{}) {
				t.Fatal("partial transition failure")
			}
			return
		}
		if out.Kind != "group" || !out.State.Terminal() || !out.PayloadForgotten || len(out.Output) != 0 {
			t.Fatal("invalid forgotten shape")
		}
		for _, member := range out.Members {
			if len(member.Output) != 0 {
				t.Fatal("retained member output")
			}
		}
		after, _ := json.Marshal(graph)
		if string(before) != string(after) {
			t.Fatal("transition mutated input")
		}
		again, err := async.ForgetGroupPayload(out)
		if err != nil || !reflect.DeepEqual(again, out) {
			t.Fatal("transition retry changed outcome", err)
		}
	})
}

func TestGroupForgetTransitionBudgetAndOverflowPreflight(t *testing.T) {
	graph := groupBoundaryGraph(t, 1)
	graph.Output = nil
	member := graph.Members[graph.Children[0].ID]
	member.Output = nil
	graph.Members[member.ID] = member
	graph.Signatures[0].ParentField = "x"
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	graph.Signatures[0].ParentField = strings.Repeat("x", async.MaxWorkflowDurableBytes-len(raw))
	raw, err = json.Marshal(graph)
	if err != nil || len(raw) != async.MaxWorkflowDurableBytes-1 {
		t.Fatal(len(raw), err)
	}
	store := &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}
	group := async.RestoreGroup(outcomeClient(t, store, func(context.Context, string, string, string) error { return nil }), graph.ID)
	if _, err := group.Snapshot(context.Background()); err != nil {
		t.Fatal("valid thin graph fixture", err)
	}
	if out, err := async.ForgetGroupPayload(graph); err != async.ErrUnavailable || !reflect.DeepEqual(out, async.Graph{}) {
		t.Fatal("marker exceeded final wire budget", out.ID, err)
	}

	f := newGroupForgetFixture(t, 1)
	f.graph.Revision = math.MaxUint64
	f.workflows.WorkflowStore = &groupBoundaryStore{read: func(context.Context, string) (async.Graph, error) { return f.graph, nil }}
	if err := f.group.Forget(context.Background()); err != async.ErrConflict || f.childWrites != 0 || f.graphWrites != 0 {
		t.Fatal("overflow deleted children", err, f.childWrites, f.graphWrites)
	}
}
