package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
)

func TestRealRedisGroupForgetPinsPayloadsAndReplayIdentities(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	broker, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	registry := async.NewRegistry()
	calls := 0
	task, err := async.Register(registry, "forget.member", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { calls++; return n + 1, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	deny := false
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows, Authorize: func(_ context.Context, action, _, _ string) error {
		if deny && action == "forget" {
			return async.ErrDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	one, _ := task.Signature(1)
	two, _ := task.Signature(2)
	one, two = one.Set(async.WithScope("tenant", "reader")), two.Set(async.WithScope("tenant", "reader"))
	group, err := client.ApplyCanvas(ctx, async.Group(one, two))
	if err != nil {
		t.Fatal(err)
	}
	if err := group.Forget(ctx); err != async.ErrPinned {
		t.Fatal("active group", err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{workflows}, ID: "forget-relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, ID: "forget-worker"}
	for range 2 {
		if err := worker.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Deliver completions without yet delivering workflow-local unpin intents.
	relay.Sources = []async.IntentStore{results}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := group.Snapshot(ctx)
	if err != nil || before.State != async.Succeeded || calls != 2 {
		t.Fatal(before.State, calls, err)
	}
	if err := group.Forget(ctx); err != async.ErrPinned {
		t.Fatal("pending coordination pin", err)
	}
	for _, child := range before.Children {
		r, err := results.Lookup(ctx, child.ID)
		if err != nil || !r.Pinned || len(r.Output) == 0 {
			t.Fatal("pin refusal changed child", err)
		}
	}
	relay.Sources = []async.IntentStore{workflows}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	deny = true
	if err := group.Forget(ctx); err != async.ErrDenied {
		t.Fatal("missing action grant", err)
	}
	deny = false

	// Keep unrelated pending work and exact task/workflow intent records alive.
	sibling, err := client.ApplyCanvas(ctx, async.Group(one))
	if err != nil {
		t.Fatal(err)
	}
	siblingBefore, err := sibling.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	intentKeys := []string{results.Connection.PartitionKey("workflow", group.ID, "intents"), results.Connection.PartitionKey("workflow", sibling.ID, "intents")}
	records := make(map[string]async.Record)
	for _, child := range before.Children {
		intentKeys = append(intentKeys, results.Connection.PartitionKey("task", child.ID, "intents"))
		r, err := results.Lookup(ctx, child.ID)
		if err != nil {
			t.Fatal(err)
		}
		records[child.ID] = r
	}
	intents := make([]map[string]string, len(intentKeys))
	for i, key := range intentKeys {
		intents[i], err = results.Connection.Client().HGetAll(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := group.Forget(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := group.Snapshot(ctx)
	if err != nil || !after.PayloadForgotten || len(after.Output) != 0 || after.State != before.State || len(after.Members) != 2 {
		t.Fatal(after, err)
	}
	for id, old := range records {
		r, err := results.Lookup(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		old.Output, old.Progress, old.Revision = nil, nil, old.Revision+1
		oldWire, _ := json.Marshal(old)
		newWire, _ := json.Marshal(r)
		if string(oldWire) != string(newWire) || r.TombstoneUntil.IsZero() {
			t.Fatal("forget changed non-payload task metadata")
		}
		if len(after.Members[id].Output) != 0 {
			t.Fatal("retained graph member output")
		}
	}
	for i, key := range intentKeys {
		current, err := results.Connection.Client().HGetAll(ctx, key).Result()
		if err != nil || !reflect.DeepEqual(current, intents[i]) {
			t.Fatal("forget changed durable intents", err)
		}
	}
	siblingAfter, err := sibling.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(siblingBefore, siblingAfter) {
		t.Fatal("forget changed sibling workflow", err)
	}
	for _, member := range before.Members {
		if err := workflows.RecordMember(ctx, group.ID, member, func(async.Graph) (async.Graph, []async.Intent, error) {
			t.Fatal("duplicate terminal completion invoked transition")
			return async.Graph{}, nil, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.ReconcileWorkflow(ctx, group.ID, async.WorkflowRepairOptions{}); err != nil {
		t.Fatal(err)
	}
	if values, err := group.Join(ctx); err != async.ErrResultExpired || len(values) != 0 {
		t.Fatal(values, err)
	}
	if values, err := group.JoinOutcomes(ctx); err != async.ErrResultExpired || len(values) != 0 {
		t.Fatal(values, err)
	}
	if err := group.Forget(ctx); err != nil {
		t.Fatal("idempotent retry", err)
	}
}

type groupForgetLostReply struct {
	async.WorkflowStore
	payload async.WorkflowPayloadStore
	lost    bool
}

func (s *groupForgetLostReply) ForgetGraphPayload(ctx context.Context, id, scope string, revision uint64) error {
	if err := s.payload.ForgetGraphPayload(ctx, id, scope, revision); err != nil {
		return err
	}
	if !s.lost {
		s.lost = true
		return errors.Join(async.ErrConflict, io.ErrUnexpectedEOF)
	}
	return nil
}

func TestRealRedisGroupForgetUnknownReplyAndConcurrentRevision(t *testing.T) {
	ctx := context.Background()
	broker, results := backends(t)
	workflows := &adapter.Workflows{Connection: results.Connection}
	lost := &groupForgetLostReply{WorkflowStore: workflows, payload: workflows}
	client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: broker, Results: results, Workflows: lost})
	if err != nil {
		t.Fatal(err)
	}
	group, err := client.ApplyCanvas(ctx, async.Group())
	if err != nil {
		t.Fatal(err)
	}
	if err := group.Forget(ctx); err != async.ErrUnavailable {
		t.Fatal("mixed lost acknowledgement became no-write outcome", err)
	}
	graph, err := group.Snapshot(ctx)
	if err != nil || !graph.PayloadForgotten {
		t.Fatal("actual applied write not exercised", err)
	}
	if err := group.Forget(ctx); err != nil {
		t.Fatal("unknown outcome retry", err)
	}

	other, err := client.ApplyCanvas(ctx, async.Group())
	if err != nil {
		t.Fatal(err)
	}
	graph, err = other.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflows.ForgetGraphPayload(ctx, graph.ID, "wrong-scope", graph.Revision); err != async.ErrDenied {
		t.Fatal(err)
	}
	if err := workflows.ForgetGraphPayload(ctx, graph.ID, graph.Scope, graph.Revision+1); err != async.ErrConflict {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	replies := make(chan error, 4)
	for range 4 {
		wg.Go(func() { replies <- workflows.ForgetGraphPayload(ctx, graph.ID, graph.Scope, graph.Revision) })
	}
	wg.Wait()
	close(replies)
	confirmed := 0
	for err := range replies {
		if err == nil {
			confirmed++
		} else if err != async.ErrConflict {
			t.Fatal(err)
		}
	}
	if confirmed != 1 {
		t.Fatal("revision fence", confirmed)
	}
	after, err := other.Snapshot(ctx)
	if err != nil || !after.PayloadForgotten || after.Revision != graph.Revision+1 {
		t.Fatal(after.Revision, err)
	}
}
