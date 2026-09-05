package async_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func controlRequest(t *testing.T, snapshot async.WorkerSnapshot, now time.Time) async.WorkerControlRequest {
	t.Helper()
	id, err := async.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return async.WorkerControlRequest{ID: id, WorkerID: snapshot.ID, InstanceID: snapshot.InstanceID, Command: async.ShutdownWorker, Queues: append([]string(nil), snapshot.Queues...), ExpiresAt: now.Add(time.Minute)}
}

func TestWorkerControlTransportRequiresLiveExactInstanceAndImmutableReply(t *testing.T) {
	ctx := context.Background()
	store := fakes.NewMemory()
	lease, snapshot := presenceDeclaration(t, "controller")
	snapshot.InstanceID, _ = async.NewID()
	now := time.Now().UTC()
	store.Clock = func() time.Time { return now }
	if err := store.ClaimWorker(ctx, lease, snapshot, time.Minute); err != nil {
		t.Fatal(err)
	}
	request := controlRequest(t, snapshot, now)
	if err := store.SubmitWorkerControl(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.SubmitWorkerControl(ctx, request); err != nil {
		t.Fatal("exact retry not idempotent", err)
	}
	changed := request
	changed.Queues = []string{"forged"}
	if err := store.SubmitWorkerControl(ctx, changed); !errors.Is(err, async.ErrConflict) {
		t.Fatal("request ID rebound", err)
	}
	for repeat := 0; repeat < 2; repeat++ {
		requests, err := store.ReceiveWorkerControls(ctx, lease, 16)
		if err != nil || len(requests) != 1 || !reflect.DeepEqual(requests[0], request) {
			t.Fatal("non-destructive receive changed request", err)
		}
		requests[0].Queues[0] = "mutated"
	}
	wrong := lease
	wrong.Token, _ = async.NewID()
	if _, err := store.ReceiveWorkerControls(ctx, wrong, 16); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal("unknown lease received control", err)
	}
	if reply, err := store.LookupWorkerControl(ctx, request); !errors.Is(err, async.ErrNotFound) || reply.RequestID != "" {
		t.Fatal("pending request invented reply", err)
	}
	reply, err := store.ReplyWorkerControl(ctx, lease, request, async.ControlAccepted)
	if err != nil || async.ValidateWorkerControlReply(request, reply) != nil || !reply.At.Equal(now) {
		t.Fatal("reply not persisted", err)
	}
	now = now.Add(time.Second)
	retried, err := store.ReplyWorkerControl(ctx, lease, request, async.ControlAccepted)
	if err != nil || !reflect.DeepEqual(reply, retried) {
		t.Fatal("reply retry changed original acknowledgment", err)
	}
	if _, err := store.ReplyWorkerControl(ctx, lease, request, async.ControlQueueChanged); !errors.Is(err, async.ErrConflict) {
		t.Fatal("terminal reply overwritten", err)
	}
	if _, err := store.LookupWorkerControl(ctx, changed); !errors.Is(err, async.ErrConflict) {
		t.Fatal("forged request read reply", err)
	}
	if requests, err := store.ReceiveWorkerControls(ctx, lease, 16); err != nil || len(requests) != 1 {
		t.Fatal("accepted control cannot recover an unknown acknowledgment write", err)
	}
	if err := store.ReleaseWorker(ctx, lease, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := store.SubmitWorkerControl(ctx, request); err != nil {
		t.Fatal("reply receipt cannot be retried after offline", err)
	}
	if read, err := store.LookupWorkerControl(ctx, request); err != nil || !reflect.DeepEqual(reply, read) {
		t.Fatal("offline erased prior acknowledgment", err)
	}
	now = now.Add(25 * time.Hour)
	if _, err := store.LookupWorkerControl(ctx, request); !errors.Is(err, async.ErrNotFound) {
		t.Fatal("control reply retention unbounded", err)
	}
}

func TestWorkerControlTransportExpiryTakeoverAndBoundedQueue(t *testing.T) {
	ctx := context.Background()
	store := fakes.NewMemory()
	lease, snapshot := presenceDeclaration(t, "bounded")
	snapshot.InstanceID, _ = async.NewID()
	now := time.Now().UTC()
	store.Clock = func() time.Time { return now }
	if err := store.ClaimWorker(ctx, lease, snapshot, time.Second); err != nil {
		t.Fatal(err)
	}
	request := controlRequest(t, snapshot, now)
	request.ExpiresAt = now.Add(500 * time.Millisecond)
	if err := store.SubmitWorkerControl(ctx, request); err != nil {
		t.Fatal(err)
	}
	now = now.Add(600 * time.Millisecond)
	if entries, err := store.ReceiveWorkerControls(ctx, lease, 16); err != nil || len(entries) != 0 {
		t.Fatal("expired request executable", err)
	}
	if _, err := store.ReplyWorkerControl(ctx, lease, request, async.ControlAccepted); !errors.Is(err, async.ErrNotFound) {
		t.Fatal("expired request accepted", err)
	}
	now = now.Add(time.Second)
	other, changed := presenceDeclaration(t, snapshot.ID)
	changed.InstanceID, _ = async.NewID()
	if err := store.ClaimWorker(ctx, other, changed, time.Hour); err != nil {
		t.Fatal(err)
	}
	old := controlRequest(t, snapshot, now)
	if err := store.SubmitWorkerControl(ctx, old); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal("stale instance accepted new control", err)
	}
	if _, err := store.ReceiveWorkerControls(ctx, lease, 16); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal("old owner received replacement controls", err)
	}
	for index := 1; index < async.MaxRetainedWorkerControls; index++ {
		if err := store.SubmitWorkerControl(ctx, controlRequest(t, changed, now)); err != nil {
			t.Fatal(index, err)
		}
	}
	if err := store.SubmitWorkerControl(ctx, controlRequest(t, changed, now)); !errors.Is(err, async.ErrConflict) {
		t.Fatal("retained request bound ignored", err)
	}
	requests, err := store.ReceiveWorkerControls(ctx, other, 16)
	if err != nil || len(requests) != 16 {
		t.Fatal("receive bound ignored", err)
	}
}
