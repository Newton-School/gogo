package async_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func TestGroupResultIteratesPartialOutcomesAndJoinsInInputOrder(t *testing.T) {
	ctx := context.Background()
	task, client, worker, backend := setup(t, func(_ context.Context, _ async.TaskContext, n int) (int, error) {
		if n == 2 {
			return 0, errors.New("private handler failure")
		}
		return n, nil
	}, async.TaskOptions{})
	one, _ := task.Signature(1)
	two, _ := task.Signature(2)
	three, _ := task.Signature(3)
	group, err := client.ApplyCanvas(ctx, async.Group(one, two, three))
	if err != nil {
		t.Fatal(err)
	}
	if ready, err := group.Ready(ctx); err != nil || ready {
		t.Fatal(ready, err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{backend}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	deliveries := map[int]async.Delivery{}
	for i := 0; i < 3; i++ {
		d := take(t, backend)
		e, _ := async.DecodeEnvelope(d.Body)
		var n int
		_ = json.Unmarshal(e.Args, &n)
		deliveries[n] = d
	}
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
	if ready, err := group.Ready(ctx); err != nil || !ready {
		t.Fatal(ready, err)
	}
	if successful, err := group.Successful(ctx); err != nil || successful {
		t.Fatal(successful, err)
	}
	if failed, err := group.Failed(ctx); err != nil || !failed {
		t.Fatal(failed, err)
	}
	if _, err := group.Join(ctx); err == nil {
		t.Fatal("propagating join ignored group failure")
	}
	outcomes, err := group.JoinOutcomes(ctx)
	if err != nil || len(outcomes) != 3 || string(outcomes[0].Output) != "1" || outcomes[1].State != async.Failed || outcomes[1].Failure == nil || string(outcomes[2].Output) != "3" {
		t.Fatal(outcomes, err)
	}
	if strings.Contains(outcomes[1].Failure.Message, "private") {
		t.Fatal("private failure leaked")
	}
}

type resultWorkflow struct {
	async.WorkflowStore
	read func(context.Context, string) (async.Graph, error)
}

func (s resultWorkflow) ReadGraph(ctx context.Context, id string) (async.Graph, error) {
	return s.read(ctx, id)
}

func outcomeGraph(t *testing.T, count int) async.Graph {
	t.Helper()
	return groupBoundaryGraph(t, count)
}

func outcomeClient(t *testing.T, workflow async.WorkflowStore, authorize func(context.Context, string, string, string) error) *async.Client {
	t.Helper()
	if authorize == nil {
		authorize = func(_ context.Context, operation, scope, _ string) error {
			if operation != "read" || scope != "tenant" {
				return async.ErrDenied
			}
			return nil
		}
	}
	backend := fakes.NewMemory()
	c, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: backend, Results: backend, Workflows: workflow, Authorize: authorize})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGroupIterationReauthorizesEveryYieldAndPreservesPartialErrors(t *testing.T) {
	graph := outcomeGraph(t, 3)
	allowed := true
	client := outcomeClient(t, resultWorkflow{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}, func(context.Context, string, string, string) error {
		if !allowed {
			return async.ErrDenied
		}
		return nil
	})
	group := async.RestoreGroup(client, graph.ID)
	seen := 0
	for _, err := range group.Iterate(context.Background()) {
		if seen == 0 {
			if err != nil {
				t.Fatal(err)
			}
			seen++
			allowed = false
			continue
		}
		if !errors.Is(err, async.ErrDenied) {
			t.Fatal("scope revocation leaked next member", err)
		}
		seen++
	}
	if seen != 2 {
		t.Fatal(seen)
	}
	allowed = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen = 0
	for _, err := range group.Iterate(ctx) {
		if seen == 0 {
			if err != nil {
				t.Fatal(err)
			}
			cancel()
		} else if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		seen++
	}
	if seen != 2 {
		t.Fatal(seen)
	}

	// An erroring backend may return an incomplete value: never expose that
	// value before authorization or treat the outage as an unfinished task.
	client = outcomeClient(t, resultWorkflow{read: func(context.Context, string) (async.Graph, error) { return graph, async.ErrUnavailable }}, func(context.Context, string, string, string) error {
		t.Fatal("outage reached authorization with data")
		return nil
	})
	if snapshot, err := async.RestoreGroup(client, graph.ID).Snapshot(context.Background()); !errors.Is(err, async.ErrUnavailable) || snapshot.ID != "" {
		t.Fatal(snapshot, err)
	}
}

func TestGroupJoinOutcomesReturnsObservedMembersOnOutage(t *testing.T) {
	graph := outcomeGraph(t, 2)
	graph.State = async.Running
	delete(graph.Members, graph.Children[0].ID)
	reads := 0
	client := outcomeClient(t, resultWorkflow{read: func(context.Context, string) (async.Graph, error) {
		reads++
		if reads > 1 {
			return async.Graph{}, async.ErrUnavailable
		}
		return graph, nil
	}}, nil)
	outcomes, err := async.RestoreGroup(client, graph.ID).JoinOutcomes(context.Background())
	if !errors.Is(err, async.ErrUnavailable) || len(outcomes) != 1 || outcomes[0].ID != graph.Children[1].ID {
		t.Fatal(outcomes, err)
	}
}

func TestGroupIterationBreakTimeoutEmptyAndUnsupportedShapes(t *testing.T) {
	graph := outcomeGraph(t, 2)
	reads := 0
	client := outcomeClient(t, resultWorkflow{read: func(context.Context, string) (async.Graph, error) { reads++; return graph, nil }}, nil)
	group := async.RestoreGroup(client, graph.ID)
	for range group.Iterate(context.Background()) {
		break
	}
	if reads != 1 {
		t.Fatal(reads)
	}
	graph.Kind = "chain"
	if _, err := group.JoinOutcomes(context.Background()); !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
	graph = outcomeGraph(t, 0)
	group = async.RestoreGroup(client, graph.ID)
	if out, err := group.JoinOutcomes(context.Background()); err != nil || len(out) != 0 || out == nil {
		t.Fatal(out, err)
	}
	graph = outcomeGraph(t, 1)
	graph.State = async.Running
	graph.Members = map[string]async.Completion{}
	group = async.RestoreGroup(client, graph.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if out, err := group.JoinOutcomes(ctx); !errors.Is(err, context.DeadlineExceeded) || len(out) != 0 || graph.CancelRequested {
		t.Fatal(out, err)
	}
	graph.State = async.Succeeded
	if _, err := group.JoinOutcomes(context.Background()); !errors.Is(err, async.ErrUnavailable) {
		t.Fatal("fabricated missing terminal member", err)
	}
	var absent *async.GroupResult
	if _, err := absent.JoinOutcomes(context.Background()); !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestGroupIterationWorkerDeadlockGuard(t *testing.T) {
	var group *async.GroupResult
	task, client, worker, backend := setup(t, func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
		if _, err := group.JoinOutcomes(ctx); !errors.Is(err, async.ErrWorkerJoin) {
			t.Fatal(err)
		}
		for _, err := range group.Iterate(ctx) {
			if !errors.Is(err, async.ErrWorkerJoin) {
				t.Fatal(err)
			}
		}
		return n, nil
	}, async.TaskOptions{})
	s, _ := task.Signature(1)
	var err error
	group, err = client.ApplyCanvas(context.Background(), async.Group(s))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
}

func TestGroupJoinCollectionBudgetDoesNotLimitStreaming(t *testing.T) {
	ctx := context.Background()
	graph := outcomeGraph(t, 40)
	graph.Output = nil
	payload, _ := json.Marshal(strings.Repeat("x", 240<<10))
	backend := fakes.NewMemory()
	for _, child := range graph.Children {
		outcome := graph.Members[child.ID]
		// The durable graph stays below its 8 MiB budget. Large copied
		// outcomes are recovered from individually authorized child results.
		outcome.Output = nil
		graph.Members[child.ID] = outcome
		if err := backend.Register(ctx, child, async.Queued); err != nil {
			t.Fatal(err)
		}
		claim, err := backend.Claim(ctx, child, "worker", time.Minute)
		if err != nil || !claim.Acquired {
			t.Fatal(claim, err)
		}
		if err := backend.Transition(ctx, async.Transition{ID: child.ID, Fence: claim.Record.Fence, Owner: "worker", State: async.Succeeded, Output: payload}); err != nil {
			t.Fatal(err)
		}
	}
	grants := map[string]int{}
	client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: backend, Results: backend, Workflows: resultWorkflow{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}, Authorize: func(_ context.Context, operation, scope, id string) error {
		if operation != "read" || scope != graph.Scope {
			return async.ErrDenied
		}
		if id != graph.ID {
			if _, exists := graph.Members[id]; !exists {
				return async.ErrDenied
			}
			grants[id]++
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	group := async.RestoreGroup(client, graph.ID)
	outcomes, err := group.JoinOutcomes(context.Background())
	if !errors.Is(err, async.ErrResultCollectionTooLarge) || len(outcomes) == 0 || len(outcomes) >= 40 {
		t.Fatal(len(outcomes), err)
	}
	count := 0
	for _, err := range group.Iterate(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 40 {
		t.Fatal(count)
	}
	for _, child := range graph.Children {
		if grants[child.ID] == 0 {
			t.Fatal("child read was not authorized", child.ID)
		}
	}
}

func TestGroupIterationRecoversCopiedOutputWithChildAuthorizationAndExpiry(t *testing.T) {
	ctx := context.Background()
	graph := outcomeGraph(t, 1)
	id := graph.Children[0].ID
	e := async.Envelope{ID: id, ProtocolVersion: 1, Task: "result.recover", Version: 1, Queue: "default", Scope: graph.Scope, WorkflowID: graph.ID, Args: json.RawMessage(`1`), CreatedAt: time.Now().UTC()}
	graph.Children[0] = e
	graph.Signatures[0] = async.Signature{Task: e.Task, Version: e.Version, Args: e.Args, Options: async.DispatchOptions{Scope: graph.Scope}}
	graph.Members[id] = async.Completion{ID: id, State: async.Succeeded}
	backend := fakes.NewMemory()
	if err := backend.Register(ctx, e, async.Queued); err != nil {
		t.Fatal(err)
	}
	claim, err := backend.Claim(ctx, e, "worker", time.Minute)
	if err != nil || !claim.Acquired {
		t.Fatal(claim, err)
	}
	if err := backend.Transition(ctx, async.Transition{ID: id, Fence: claim.Record.Fence, Owner: "worker", State: async.Succeeded, Output: json.RawMessage(`9007199254740993`)}); err != nil {
		t.Fatal(err)
	}
	childAllowed := true
	client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: backend, Results: backend, Workflows: resultWorkflow{read: func(context.Context, string) (async.Graph, error) { return graph, nil }}, Authorize: func(_ context.Context, operation, scope, target string) error {
		if operation != "read" || scope != graph.Scope || target != graph.ID && (!childAllowed || target != id) {
			return async.ErrDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	group := async.RestoreGroup(client, graph.ID)
	outcomes, err := group.JoinOutcomes(ctx)
	if err != nil || len(outcomes) != 1 || string(outcomes[0].Output) != `9007199254740993` {
		t.Fatal(outcomes, err)
	}
	childAllowed = false
	if _, err := group.JoinOutcomes(ctx); !errors.Is(err, async.ErrDenied) {
		t.Fatal("child scope bypassed", err)
	}
	childAllowed = true
	if err := backend.ReleasePin(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := backend.Forget(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := group.JoinOutcomes(ctx); !errors.Is(err, async.ErrResultExpired) {
		t.Fatal("expired child fabricated", err)
	}
}
