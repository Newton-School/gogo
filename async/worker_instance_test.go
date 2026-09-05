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

func TestWorkerPresenceInstanceIdentityCannotChangeUnderLease(t *testing.T) {
	ctx := context.Background()
	store := fakes.NewMemory()
	lease, snapshot := presenceDeclaration(t, "instance")
	snapshot.InstanceID, _ = async.NewID()
	if snapshot.InstanceID == lease.Token {
		t.Fatal("instance reused secret lease")
	}
	if err := store.ClaimWorker(ctx, lease, snapshot, time.Minute); err != nil {
		t.Fatal(err)
	}
	before, err := store.LookupWorker(ctx, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{"", "invalid", lease.Token} {
		changed := snapshot
		changed.InstanceID = replacement
		for _, operation := range []func() error{
			func() error { return store.ClaimWorker(ctx, lease, changed, time.Minute) },
			func() error { return store.RenewWorker(ctx, lease, changed, time.Minute) },
			func() error { return store.ReleaseWorker(ctx, lease, changed) },
		} {
			err := operation()
			if !errors.Is(err, async.ErrConflict) && !errors.Is(err, async.ErrInvalid) {
				t.Fatal("instance mutation accepted", err)
			}
			after, err := store.LookupWorker(ctx, snapshot.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("rejected instance mutation changed presence", err)
			}
		}
	}
	if err := store.ReleaseWorker(ctx, lease, snapshot); err != nil {
		t.Fatal(err)
	}
	other, _ := presenceDeclaration(t, snapshot.ID)
	if err := store.ClaimWorker(ctx, other, snapshot, time.Minute); !errors.Is(err, async.ErrConflict) {
		t.Fatal("replacement owner reused public instance", err)
	}
}

func TestWorkerRunnerInstancesAreUniqueAndConcurrentRunRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, finish := make(chan struct{}), make(chan struct{})
	task, client, worker, store := setup(t, func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
		close(started)
		select {
		case <-finish:
			return n, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, async.TaskOptions{})
	worker.Presence = store
	if _, err := task.Delay(ctx, client, 1); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.RunOnce(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	first := worker.Snapshot().InstanceID
	if first == "" {
		t.Fatal("runner has no public instance")
	}
	if err := worker.RunOnce(ctx); !errors.Is(err, async.ErrConflict) {
		t.Fatal("concurrent runner not rejected", err)
	}
	if worker.Snapshot().InstanceID != first {
		t.Fatal("failed concurrent runner replaced instance")
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	presence, err := store.LookupWorker(ctx, worker.ID)
	if err != nil || presence.Snapshot.InstanceID != first || presence.Status != async.PresenceOffline || worker.Snapshot().InstanceID != "" {
		t.Fatal("offline instance identity lost", err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	presence, err = store.LookupWorker(ctx, worker.ID)
	if err != nil || presence.Snapshot.InstanceID == "" || presence.Snapshot.InstanceID == first {
		t.Fatal("restart reused instance", err)
	}
}

func TestWorkerPresenceFailedClaimDoesNotPinLocalInstance(t *testing.T) {
	_, _, worker, store := setup(t, func(context.Context, async.TaskContext, int) (int, error) { return 0, nil }, async.TaskOptions{})
	worker.Presence = store
	store.Fail = func(operation string) error {
		if operation == "claim_worker" {
			return async.ErrUnavailable
		}
		return nil
	}
	if err := worker.RunOnce(context.Background()); !errors.Is(err, async.ErrUnavailable) || worker.Snapshot().InstanceID != "" {
		t.Fatal("failed claim pinned instance", err)
	}
	store.Fail = nil
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal("retry after rejected claim failed", err)
	}
}
