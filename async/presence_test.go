package async_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func presenceDeclaration(t *testing.T, id string) (async.WorkerLease, async.WorkerSnapshot) {
	t.Helper()
	token, _ := async.NewID()
	taskID, _ := async.NewID()
	now := time.Date(2026, 1, 2, 3, 4, 5, 987654321, time.UTC)
	return async.WorkerLease{WorkerID: id, Token: token}, async.WorkerSnapshot{ID: id, Queues: []string{"default"}, Registered: []string{"test.add@1"}, Concurrency: 2, Active: []async.TaskActivity{{ID: taskID, Task: "test.add", Scope: "tenant", StartedAt: now}}, At: now}
}

func TestWorkerPresenceOwnershipExpiryRecoveryAndRetention(t *testing.T) {
	ctx := context.Background()
	backend := fakes.NewMemory()
	lease, snapshot := presenceDeclaration(t, "worker")
	clock := snapshot.At
	backend.Clock = func() time.Time { return clock }
	if err := backend.ClaimWorker(ctx, lease, snapshot, time.Second); err != nil {
		t.Fatal(err)
	}
	other, _ := presenceDeclaration(t, snapshot.ID)
	if err := backend.ClaimWorker(ctx, other, snapshot, time.Second); !errors.Is(err, async.ErrConflict) {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Second)
	p, err := backend.LookupWorker(ctx, snapshot.ID)
	if err != nil || p.Status != async.PresenceLost || len(p.Snapshot.Active) != 1 {
		t.Fatal(p, err)
	}
	// Recovery by the same instance is allowed until another instance claims it.
	if err := backend.RenewWorker(ctx, lease, snapshot, time.Second); err != nil {
		t.Fatal(err)
	}
	p, _ = backend.LookupWorker(ctx, snapshot.ID)
	if p.Status != async.PresenceOnline || !p.ObservedAt.Equal(clock) {
		t.Fatal(p)
	}
	clock = clock.Add(2 * time.Second)
	if err := backend.ClaimWorker(ctx, other, snapshot, time.Second); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{backend.RenewWorker(ctx, lease, snapshot, time.Second), backend.ReleaseWorker(ctx, lease, snapshot)} {
		if !errors.Is(err, async.ErrLeaseLost) {
			t.Fatal("stale owner mutated replacement", err)
		}
	}
	if err := backend.ReleaseWorker(ctx, other, snapshot); err != nil {
		t.Fatal(err)
	}
	p, _ = backend.LookupWorker(ctx, snapshot.ID)
	if p.Status != async.PresenceOffline || len(p.Snapshot.Active) != 1 {
		t.Fatal("offline invented task completion", p)
	}
	clock = clock.Add(25 * time.Hour)
	if _, err := backend.LookupWorker(ctx, snapshot.ID); !errors.Is(err, async.ErrNotFound) {
		t.Fatal(err)
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%d", "%s", "%x", "%q"} {
		if strings.Contains(fmt.Sprintf(verb, lease), lease.Token) {
			t.Fatal("lease token leaked")
		}
	}
	if _, err := json.Marshal(lease); !errors.Is(err, async.ErrDenied) {
		t.Fatal(err)
	}
}

func TestWorkerPresenceConcurrentClaimsAndClonedSnapshots(t *testing.T) {
	backend := fakes.NewMemory()
	first, snapshot := presenceDeclaration(t, "worker")
	second, _ := presenceDeclaration(t, snapshot.ID)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for _, lease := range []async.WorkerLease{first, second} {
		wg.Go(func() {
			err := backend.ClaimWorker(context.Background(), lease, snapshot, time.Minute)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, async.ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal(accepted.Load())
	}
	snapshot.Queues[0] = "mutated"
	p, err := backend.LookupWorker(context.Background(), snapshot.ID)
	if err != nil || p.Snapshot.Queues[0] != "default" {
		t.Fatal(p, err)
	}
	p.Snapshot.Queues[0] = "mutated_again"
	p, _ = backend.LookupWorker(context.Background(), snapshot.ID)
	if p.Snapshot.Queues[0] != "default" {
		t.Fatal("lookup alias")
	}
}

func TestWorkerPresenceRunObservesActiveWorkAndGracefulOffline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, finish := make(chan struct{}), make(chan struct{})
	task, client, worker, backend := setup(t, func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
		close(started)
		select {
		case <-finish:
			return n + 1, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, async.TaskOptions{})
	worker.Presence, worker.Concurrency = backend, 1
	worker.Lease, worker.Heartbeat = 150*time.Millisecond, 10*time.Millisecond
	result, err := task.Delay(ctx, client, 41)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.RunOnce(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	deadline := time.Now().Add(time.Second)
	for {
		p, err := backend.LookupWorker(ctx, worker.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Snapshot.Active) == 1 {
			if p.Status != async.PresenceOnline || p.Snapshot.Active[0].ID != result.Receipt.ID {
				t.Fatal(p)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("active heartbeat missing")
		}
		time.Sleep(time.Millisecond)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	p, err := backend.LookupWorker(ctx, worker.ID)
	if err != nil || p.Status != async.PresenceOffline || len(p.Snapshot.Active) != 0 {
		t.Fatal(p, err)
	}
	if output, err := result.Get(ctx); err != nil || output != 42 {
		t.Fatal(output, err)
	}
}

func TestWorkerPresenceStartupFailureDoesNotReserveAndOutageDoesNotInventTaskState(t *testing.T) {
	ctx := context.Background()
	finish := make(chan struct{})
	task, client, worker, backend := setup(t, func(context.Context, async.TaskContext, int) (int, error) { <-finish; return 42, nil }, async.TaskOptions{})
	worker.Presence, worker.Concurrency = backend, 1
	worker.Lease, worker.Heartbeat = 150*time.Millisecond, 10*time.Millisecond
	result, err := task.Delay(ctx, client, 1)
	if err != nil {
		t.Fatal(err)
	}
	var failClaim atomic.Bool
	failClaim.Store(true)
	backend.Fail = func(operation string) error {
		if operation == "claim_worker" && failClaim.Load() || operation == "renew_worker" {
			return async.ErrUnavailable
		}
		return nil
	}
	if err := worker.RunOnce(ctx); !errors.Is(err, async.ErrUnavailable) {
		t.Fatal(err)
	}
	stats, _ := backend.Inspect(ctx, []string{"default"})
	if stats[0].Queued != 1 || stats[0].Pending != 0 {
		t.Fatal("unconfirmed identity reserved work", stats)
	}
	failClaim.Store(false)
	observed := make(chan error, 10)
	worker.OnError = func(err error) {
		select {
		case observed <- err:
		default:
		}
	}
	done := make(chan error, 1)
	go func() { done <- worker.RunOnce(ctx) }()
	select {
	case err := <-observed:
		if !errors.Is(err, async.ErrUnavailable) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("monitor outage not reported")
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if value, err := result.Get(ctx); err != nil || value != 42 {
		t.Fatal("monitor outage changed task outcome", value, err)
	}
}

func TestRemoteWorkerInspectionScopesPartialOutageAndUnknown(t *testing.T) {
	ctx := context.Background()
	backend := fakes.NewMemory()
	lease, snapshot := presenceDeclaration(t, "worker")
	if err := backend.ClaimWorker(ctx, lease, snapshot, time.Minute); err != nil {
		t.Fatal(err)
	}
	var denyQueue, denyWorker atomic.Bool
	client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: backend, Results: backend, Presence: backend, Authorize: func(_ context.Context, operation, scope, id string) error {
		if operation != "inspect" || scope == "tenant" || scope == "default" && denyQueue.Load() || id == "worker" && denyWorker.Load() {
			return async.ErrDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	control := async.Control{Client: client}
	out, err := control.InspectWorkers(ctx, []string{"worker", "missing"})
	if err != nil || len(out) != 2 || out[0].Status != async.PresenceOnline || len(out[0].Snapshot.Active) != 0 || out[1].Status != async.PresenceUnknown {
		t.Fatal(out, err)
	}
	denyQueue.Store(true)
	if out, err := control.InspectWorkers(ctx, []string{"worker"}); !errors.Is(err, async.ErrDenied) || len(out) != 0 {
		t.Fatal(out, err)
	}
	denyQueue.Store(false)
	denyWorker.Store(true)
	backend.Fail = func(operation string) error { t.Fatal("denied inspection read backend"); return nil }
	if out, err := control.InspectWorkers(ctx, []string{"worker"}); !errors.Is(err, async.ErrDenied) || len(out) != 0 {
		t.Fatal(out, err)
	}
	denyWorker.Store(false)
	reads := 0
	backend.Fail = func(operation string) error {
		if operation == "lookup_worker" {
			reads++
			if reads == 2 {
				return errors.New("private connection information")
			}
		}
		return nil
	}
	out, err = control.InspectWorkers(ctx, []string{"worker", "missing"})
	if !errors.Is(err, async.ErrUnavailable) || len(out) != 1 || strings.Contains(err.Error(), "private") {
		t.Fatal(out, err)
	}
	for _, ids := range [][]string{{}, {"worker", "worker"}, {"bad{slot}"}, make([]string, 65)} {
		if _, err := control.InspectWorkers(ctx, ids); !errors.Is(err, async.ErrInvalid) {
			t.Fatal(ids, err)
		}
	}
}

func TestRemoteWorkerInspectionDistinguishesHiddenActivityFromAuthorityOutage(t *testing.T) {
	backend := fakes.NewMemory()
	for _, id := range []string{"first", "second"} {
		lease, snapshot := presenceDeclaration(t, id)
		if id == "first" {
			snapshot.Active = nil
		}
		if err := backend.ClaimWorker(context.Background(), lease, snapshot, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	for _, outcome := range []string{"denied", "outage", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: backend, Results: backend, Presence: backend, Authorize: func(_ context.Context, operation, scope, id string) error {
				if scope != "tenant" {
					return nil
				}
				switch outcome {
				case "denied":
					return async.ErrDenied
				case "outage":
					return errors.New("private permission provider information")
				default:
					cancel()
					return context.Canceled
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			out, err := (async.Control{Client: client}).InspectWorkers(ctx, []string{"first", "second"})
			if outcome == "denied" {
				if err != nil || len(out) != 2 || len(out[1].Snapshot.Active) != 0 {
					t.Fatal(out, err)
				}
				return
			}
			if len(out) != 1 {
				t.Fatal("current worker fabricated empty activity", out, err)
			}
			if outcome == "outage" && (!errors.Is(err, async.ErrUnavailable) || strings.Contains(err.Error(), "private")) || outcome == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(out, err)
			}
		})
	}
}
