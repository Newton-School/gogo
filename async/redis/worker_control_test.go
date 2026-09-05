package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
)

func redisControlRequest(t *testing.T, snapshot async.WorkerSnapshot) async.WorkerControlRequest {
	t.Helper()
	id, _ := async.NewID()
	return async.WorkerControlRequest{ID: id, WorkerID: snapshot.ID, InstanceID: snapshot.InstanceID, Command: async.ShutdownWorker, Queues: append([]string(nil), snapshot.Queues...), ExpiresAt: time.Now().UTC().Add(time.Minute)}
}

func TestRealRedisWorkerControlsBindExactInstanceAndImmutableAcknowledgment(t *testing.T) {
	ctx := context.Background()
	_, results := backends(t)
	store := &adapter.Workers{Connection: results.Connection}
	lease, snapshot := redisPresence(t, "controlled")
	snapshot.InstanceID, _ = async.NewID()
	if err := store.ClaimWorker(ctx, lease, snapshot, time.Minute); err != nil {
		t.Fatal(err)
	}
	request := redisControlRequest(t, snapshot)
	if err := store.SubmitWorkerControl(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.SubmitWorkerControl(ctx, request); err != nil {
		t.Fatal("exact retry not accepted", err)
	}
	changed := request
	changed.Queues = []string{"forged"}
	if err := store.SubmitWorkerControl(ctx, changed); !errors.Is(err, async.ErrConflict) {
		t.Fatal("same ID rebound", err)
	}
	for repeat := 0; repeat < 2; repeat++ {
		requests, err := store.ReceiveWorkerControls(ctx, lease, 16)
		if err != nil || len(requests) != 1 || !reflect.DeepEqual(requests[0], request) {
			t.Fatal("opaque request round-trip changed", err)
		}
	}
	other := lease
	other.Token, _ = async.NewID()
	if _, err := store.ReceiveWorkerControls(ctx, other, 16); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal("unknown lease received command", err)
	}
	if reply, err := store.LookupWorkerControl(ctx, request); !errors.Is(err, async.ErrNotFound) || reply.RequestID != "" {
		t.Fatal("pending command invented acknowledgment", err)
	}
	reply, err := store.ReplyWorkerControl(ctx, lease, request, async.ControlAccepted)
	if err != nil || async.ValidateWorkerControlReply(request, reply) != nil {
		t.Fatal("acceptance not persisted", err)
	}
	if retried, err := store.ReplyWorkerControl(ctx, lease, request, async.ControlAccepted); err != nil || retried != reply {
		t.Fatal("reply retry changed receipt", err)
	}
	if _, err := store.ReplyWorkerControl(ctx, lease, request, async.ControlQueueChanged); !errors.Is(err, async.ErrConflict) {
		t.Fatal("immutable acknowledgment overwritten", err)
	}
	if requests, err := store.ReceiveWorkerControls(ctx, lease, 16); err != nil || len(requests) != 1 {
		t.Fatal("accepted control cannot recover an unknown acknowledgment write", err)
	}
	if err := store.ReleaseWorker(ctx, lease, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := store.SubmitWorkerControl(ctx, request); err != nil {
		t.Fatal("offline request retry failed", err)
	}
	if current, err := store.LookupWorkerControl(ctx, request); err != nil || current != reply {
		t.Fatal("offline removed original acknowledgment", err)
	}
	if _, err := store.LookupWorkerControl(ctx, changed); !errors.Is(err, async.ErrConflict) {
		t.Fatal("forged scope read acknowledgment", err)
	}
	newSnapshot := snapshot
	newSnapshot.InstanceID, _ = async.NewID()
	if err := store.ClaimWorker(ctx, other, newSnapshot, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.SubmitWorkerControl(ctx, redisControlRequest(t, snapshot)); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal("old instance targeted replacement", err)
	}
	if _, err := store.ReplyWorkerControl(ctx, lease, request, async.ControlAccepted); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal("old owner acknowledged after takeover", err)
	}
}

func TestRealRedisWorkerControlExpiryCorruptionAndBatchPreflight(t *testing.T) {
	ctx := context.Background()
	_, results := backends(t)
	store := &adapter.Workers{Connection: results.Connection}
	lease, snapshot := redisPresence(t, "expires")
	snapshot.InstanceID, _ = async.NewID()
	if err := store.ClaimWorker(ctx, lease, snapshot, time.Minute); err != nil {
		t.Fatal(err)
	}
	request := redisControlRequest(t, snapshot)
	if err := store.SubmitWorkerControl(ctx, request); err != nil {
		t.Fatal(err)
	}
	key := results.Connection.PartitionKey("worker", snapshot.ID, "controls")
	raw, err := results.Connection.Client().HGet(ctx, key, request.ID).Result()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]string
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		t.Fatal(err)
	}
	document["expires"] = "1"
	data, _ := json.Marshal(document)
	if err := results.Connection.Client().HSet(ctx, key, request.ID, data).Err(); err != nil {
		t.Fatal(err)
	}
	if requests, err := store.ReceiveWorkerControls(ctx, lease, 16); err != nil || len(requests) != 0 {
		t.Fatal("expired command received", err)
	}
	if _, err := store.ReplyWorkerControl(ctx, lease, request, async.ControlAccepted); !errors.Is(err, async.ErrNotFound) {
		t.Fatal("expired command accepted", err)
	}
	// All records are validated before pruning/writing. Redis Lua errors are
	// not transactions: malformed item two must not delete expired item one.
	document["retained"] = "1"
	data, _ = json.Marshal(document)
	if err := results.Connection.Client().HSet(ctx, key, request.ID, data, "corrupt", "not-json").Err(); err != nil {
		t.Fatal(err)
	}
	before, _ := results.Connection.Client().HGetAll(ctx, key).Result()
	if err := store.SubmitWorkerControl(ctx, redisControlRequest(t, snapshot)); err == nil {
		t.Fatal("corrupted control storage accepted write")
	}
	after, _ := results.Connection.Client().HGetAll(ctx, key).Result()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed batch partially pruned controls")
	}
	if requests, err := store.ReceiveWorkerControls(ctx, lease, 16); err == nil || len(requests) != 0 {
		t.Fatal("malformed control store returned executable partial batch")
	}
}

func TestRealRedisWorkerControlShutdownDrainsAndRetainsReplyAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	broker, results := backends(t)
	controls := &adapter.Workers{Connection: results.Connection}
	registry := async.NewRegistry()
	started, finish := make(chan struct{}), make(chan struct{})
	task, err := async.Register(registry, "test.controlled", 1, func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
		if n == 1 {
			close(started)
			select {
			case <-finish:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		return n, nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Controls: controls, Authorize: func(_ context.Context, operation, _, _ string) error {
		if operation == "inspect" {
			return async.ErrDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := task.Delay(ctx, client, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := task.Delay(ctx, client, 2)
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, Controls: controls, ID: "controlled-runner", Concurrency: 1, Lease: time.Second, Heartbeat: 50 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("worker did not start")
	}
	control := async.Control{Client: client}
	requests, err := control.PrepareWorkerShutdown(ctx, []string{worker.ID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	report, err := control.DispatchWorkerControls(ctx, requests, time.Second)
	if err != nil || !report.Complete() || report.Targets[0].Reply.Outcome != async.ControlAccepted {
		t.Fatal(report, err)
	}
	select {
	case err := <-done:
		t.Fatal("accepted reply pretended active handler stopped", err)
	default:
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal("worker drain failed", err)
	}
	if value, err := first.Get(ctx); err != nil || value != 1 {
		t.Fatal(value, err)
	}
	if state, err := second.Snapshot(ctx); err != nil || state.State != async.Queued {
		t.Fatal("worker reserved more after drain", state.State, err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if value, err := second.Get(ctx); err != nil || value != 2 {
		t.Fatal("stale request stopped replacement", value, err)
	}
	if report, err := control.DispatchWorkerControls(ctx, requests, time.Second); err != nil || !report.Complete() {
		t.Fatal("original reply missing after restart", report, err)
	}
}
