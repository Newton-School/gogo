package async_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func TestWorkerControlShutdownAcknowledgesThenDrainsWithoutRetargeting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started, finish := make(chan struct{}), make(chan struct{})
	var handlerCanceled atomic.Bool
	task, _, worker, store := setup(t, func(ctx context.Context, _ async.TaskContext, n int) (int, error) {
		if n == 1 {
			close(started)
			select {
			case <-finish:
			case <-ctx.Done():
				handlerCanceled.Store(true)
				return 0, ctx.Err()
			}
		}
		return n, nil
	}, async.TaskOptions{})
	worker.Controls = store
	worker.Concurrency = 1
	worker.Lease = time.Second
	worker.Heartbeat = 20 * time.Millisecond
	client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: store, Results: store, Controls: store, Authorize: func(_ context.Context, op, _, _ string) error {
		if op == "inspect" {
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
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("handler did not start")
	}
	control := async.Control{Client: client}
	requests, err := control.PrepareWorkerShutdown(ctx, []string{worker.ID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	report, err := control.DispatchWorkerControls(ctx, requests, time.Second)
	if err != nil || !report.Complete() || report.Targets[0].Reply.Outcome != async.ControlAccepted {
		t.Fatal("shutdown not acknowledged", report, err)
	}
	select {
	case err := <-done:
		t.Fatal("acknowledgment pretended worker finished", err)
	default:
	}
	if handlerCanceled.Load() {
		t.Fatal("remote drain canceled accepted handler")
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal("graceful drain failed", err)
	}
	if value, err := first.Get(ctx); err != nil || value != 1 {
		t.Fatal(value, err)
	}
	if snapshot, err := second.Snapshot(ctx); err != nil || snapshot.State != async.Queued {
		t.Fatal("draining runner executed queued successor", snapshot.State, err)
	}
	if retry, err := control.DispatchWorkerControls(ctx, requests, time.Second); err != nil || !retry.Complete() {
		t.Fatal("exact offline retry needed inspection grant or new instance", err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal("restart failed", err)
	}
	if value, err := second.Get(ctx); err != nil || value != 2 {
		t.Fatal("old control stopped replacement runner", value, err)
	}
}

func controlClient(t *testing.T, store *fakes.Memory, authorize func(context.Context, string, string, string) error) (*async.Client, []async.WorkerControlRequest) {
	t.Helper()
	registry := async.NewRegistry()
	if _, err := async.Register(registry, "test.control", 1, func(context.Context, async.TaskContext, int) (int, error) { return 0, nil }, async.TaskOptions{}); err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: store, Results: store, Controls: store, Authorize: authorize})
	if err != nil {
		t.Fatal(err)
	}
	requests := []async.WorkerControlRequest{}
	for _, id := range []string{"one", "two"} {
		lease, snapshot := presenceDeclaration(t, id)
		snapshot.InstanceID, _ = async.NewID()
		if err := store.ClaimWorker(context.Background(), lease, snapshot, time.Minute); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, controlRequest(t, snapshot, time.Now().UTC()))
	}
	requests[1].ID = requests[0].ID
	return client, requests
}

func TestWorkerControlClientAuthorizationMissingRepliesAndProviderErrors(t *testing.T) {
	t.Run("all targets authorized before mutation", func(t *testing.T) {
		store := fakes.NewMemory()
		client, requests := controlClient(t, store, func(_ context.Context, op, scope, id string) error {
			if op != "shutdown" || id == "two" && scope == "default" {
				return async.ErrDenied
			}
			return nil
		})
		writes := 0
		store.Fail = func(op string) error {
			if op == "submit_worker_control" {
				writes++
			}
			return nil
		}
		if report, err := (async.Control{Client: client}).DispatchWorkerControls(context.Background(), requests, time.Millisecond); !errors.Is(err, async.ErrDenied) || len(report.Targets) != 0 || writes != 0 {
			t.Fatal("denial mutated target", report, err, writes)
		}
	})
	t.Run("no implicit unscoped control authority", func(t *testing.T) {
		store := fakes.NewMemory()
		client, requests := controlClient(t, store, nil)
		if _, err := (async.Control{Client: client}).DispatchWorkerControls(context.Background(), requests, time.Millisecond); !errors.Is(err, async.ErrDenied) {
			t.Fatal(err)
		}
	})
	t.Run("missing replies remain incomplete", func(t *testing.T) {
		store := fakes.NewMemory()
		client, requests := controlClient(t, store, func(context.Context, string, string, string) error { return nil })
		report, err := (async.Control{Client: client}).DispatchWorkerControls(context.Background(), requests, 20*time.Millisecond)
		if !errors.Is(err, async.ErrControlReplyTimeout) || report.Complete() || len(report.Targets) != 2 || report.Targets[0].Submission != async.ControlSubmitted || report.Targets[1].Reply != nil {
			t.Fatal("missing replies invented success", report, err)
		}
	})
	t.Run("unknown partial submission is not rejection", func(t *testing.T) {
		store := fakes.NewMemory()
		client, requests := controlClient(t, store, func(context.Context, string, string, string) error { return nil })
		writes := 0
		private := errors.New("private backend detail")
		store.Fail = func(op string) error {
			if op == "submit_worker_control" {
				writes++
				if writes == 2 {
					return private
				}
			}
			return nil
		}
		report, err := (async.Control{Client: client}).DispatchWorkerControls(context.Background(), requests, time.Second)
		if !errors.Is(err, async.ErrUnavailable) || errors.Is(err, private) || len(report.Targets) != 2 || report.Targets[0].Submission != async.ControlSubmitted || report.Targets[1].Submission != async.ControlSubmissionUnknown {
			t.Fatal(report, err)
		}
	})
	t.Run("forged queue snapshot rejected before mutation", func(t *testing.T) {
		store := fakes.NewMemory()
		client, requests := controlClient(t, store, func(context.Context, string, string, string) error { return nil })
		requests[1].Queues = []string{"forged"}
		writes := 0
		store.Fail = func(op string) error {
			if op == "submit_worker_control" {
				writes++
			}
			return nil
		}
		if _, err := (async.Control{Client: client}).DispatchWorkerControls(context.Background(), requests, time.Millisecond); !errors.Is(err, async.ErrLeaseLost) || writes != 0 {
			t.Fatal(err, writes)
		}
	})
}

type unknownControlReplyStore struct {
	*fakes.Memory
	failed atomic.Bool
}

func (s *unknownControlReplyStore) ReplyWorkerControl(ctx context.Context, lease async.WorkerLease, request async.WorkerControlRequest, outcome async.WorkerControlOutcome) (async.WorkerControlReply, error) {
	reply, err := s.Memory.ReplyWorkerControl(ctx, lease, request, outcome)
	if err == nil && s.failed.CompareAndSwap(false, true) {
		return async.WorkerControlReply{}, errors.New("reply response lost after commit")
	}
	return reply, err
}

func TestWorkerControlRunnerRecoversUnknownReplyCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, worker, memory := setup(t, func(context.Context, async.TaskContext, int) (int, error) { return 0, nil }, async.TaskOptions{})
	store := &unknownControlReplyStore{Memory: memory}
	worker.Controls = store
	worker.Concurrency = 1
	worker.Lease = time.Second
	worker.Heartbeat = 20 * time.Millisecond
	client, err := async.NewClient(async.ClientConfig{Registry: worker.Registry, Broker: memory, Results: memory, Controls: store, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	// Presence owns the instance before the runner accepts any control.
	for worker.Snapshot().InstanceID == "" {
		select {
		case <-ctx.Done():
			t.Fatal("runner did not start")
		case <-time.After(time.Millisecond):
		}
	}
	control := async.Control{Client: client}
	var requests []async.WorkerControlRequest
	for {
		requests, err = control.PrepareWorkerShutdown(ctx, []string{worker.ID}, time.Minute)
		if err == nil {
			break
		}
		if !errors.Is(err, async.ErrNotFound) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("presence not claimed")
		case <-time.After(time.Millisecond):
		}
	}
	report, err := control.DispatchWorkerControls(ctx, requests, time.Second)
	if err != nil || !report.Complete() {
		t.Fatal(report, err)
	}
	if err := <-done; err != nil || !store.failed.Load() {
		t.Fatal("unknown acknowledgment prevented drain", err)
	}
}

type controlLookupStore struct {
	*fakes.Memory
	submitted map[string]bool
	lookup    func(context.Context, async.WorkerControlRequest) (async.WorkerControlReply, error)
}

func (s *controlLookupStore) SubmitWorkerControl(ctx context.Context, request async.WorkerControlRequest) error {
	if err := s.Memory.SubmitWorkerControl(ctx, request); err != nil {
		return err
	}
	s.submitted[request.WorkerID] = true
	return nil
}
func (s *controlLookupStore) LookupWorkerControl(ctx context.Context, request async.WorkerControlRequest) (async.WorkerControlReply, error) {
	if s.submitted[request.WorkerID] {
		return s.lookup(ctx, request)
	}
	return s.Memory.LookupWorkerControl(ctx, request)
}

func TestWorkerControlPartialRepliesReauthorizeAndDiscardErrorPayloads(t *testing.T) {
	for _, scenario := range []string{"provider outage", "permission revoked", "expired reply", "caller canceled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			memory := fakes.NewMemory()
			_, requests := controlClient(t, memory, func(context.Context, string, string, string) error { return nil })
			store := &controlLookupStore{Memory: memory, submitted: map[string]bool{}}
			firstRead := false
			private := errors.New("private unavailable backend detail")
			store.lookup = func(_ context.Context, request async.WorkerControlRequest) (async.WorkerControlReply, error) {
				reply := async.WorkerControlReply{RequestID: request.ID, WorkerID: request.WorkerID, InstanceID: request.InstanceID, Command: request.Command, Outcome: async.ControlAccepted, At: time.Now().UTC()}
				if request.WorkerID == "one" {
					firstRead = true
					return reply, nil
				}
				switch scenario {
				case "provider outage":
					return reply, private
				case "expired reply":
					reply.At = request.ExpiresAt.Add(time.Second)
					return reply, nil
				case "caller canceled":
					cancel()
					return reply, nil
				default:
					return reply, nil
				}
			}
			client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: memory, Results: memory, Controls: store, Authorize: func(_ context.Context, _, _, id string) error {
				if firstRead && id == "two" && scenario == "permission revoked" {
					return async.ErrDenied
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			report, err := (async.Control{Client: client}).DispatchWorkerControls(ctx, requests, time.Second)
			wanted := async.ErrUnavailable
			if scenario == "permission revoked" {
				wanted = async.ErrDenied
			}
			if scenario == "caller canceled" {
				wanted = context.Canceled
			}
			if !errors.Is(err, wanted) || errors.Is(err, private) || report.Complete() || len(report.Targets) != 2 || report.Targets[0].Reply == nil || report.Targets[1].Reply != nil {
				t.Fatal("invalid partial control snapshot", report, err)
			}
		})
	}
}

func TestWorkerControlInvalidRequestsNeverPanicOrTouchProvider(t *testing.T) {
	for _, variant := range []string{"unencodable expiry", "negative year", "invalid command", "duplicate target", "oversized queues"} {
		t.Run(variant, func(t *testing.T) {
			store := fakes.NewMemory()
			authCalls := 0
			client, requests := controlClient(t, store, func(context.Context, string, string, string) error { authCalls++; return nil })
			switch variant {
			case "unencodable expiry":
				requests[0].ExpiresAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			case "negative year":
				requests[0].ExpiresAt = time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)
			case "invalid command":
				requests[0].Command = "eval"
			case "duplicate target":
				requests[1].WorkerID = requests[0].WorkerID
			case "oversized queues":
				requests[0].Queues = make([]string, 65)
			}
			calls := 0
			store.Fail = func(string) error { calls++; return nil }
			report, err := (async.Control{Client: client}).DispatchWorkerControls(context.Background(), requests, time.Second)
			if !errors.Is(err, async.ErrInvalid) || len(report.Targets) != 0 || calls != 0 || authCalls != 0 {
				t.Fatal("invalid request reached provider or policy", report, err, calls, authCalls)
			}
		})
	}
}
