package async_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

func TestReplacementWaitsPreservesOriginalIdentityAndLinks(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	add, _ := async.Register(registry, "replace.add", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) { return n + 1, nil }, async.TaskOptions{})
	sum, _ := async.Register(registry, "replace.sum", 1, func(_ context.Context, _ async.TaskContext, ns []int) (int, error) {
		total := 0
		for _, n := range ns {
			total += n
		}
		return total, nil
	}, async.TaskOptions{})
	var calls, successes, after, callback atomic.Int64
	original, _ := async.Register(registry, "replace.original", 1, func(_ context.Context, tc async.TaskContext, n int) (int, error) {
		calls.Add(1)
		one, _ := add.Signature(n)
		two, _ := add.Signature(n + 1)
		body, _ := sum.Signature(nil)
		return -100, tc.Replace(async.Parallel(one.Canvas(), two.Canvas()).Then(body.Canvas()))
	}, async.TaskOptions{Hooks: async.Hooks{OnSuccess: func(context.Context, async.TaskContext, json.RawMessage) error {
		successes.Add(1)
		panic("observer failure")
	}, AfterReturn: func(context.Context, async.TaskContext, async.State) error { after.Add(1); return nil }}})
	observer, _ := async.Register(registry, "replace.callback", 1, func(_ context.Context, _ async.TaskContext, n int) (int, error) {
		callback.Store(int64(n))
		return n, nil
	}, async.TaskOptions{})
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, Client: client, ID: "worker"}
	first, _ := original.Signature(2)
	final, _ := add.Signature(0)
	link, _ := observer.Signature(0)
	group, err := client.ApplyCanvas(ctx, async.Chain(first.Link(link), final))
	if err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{backend}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	delivery, err := backend.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, _ := async.DecodeEnvelope(delivery.Body)
	backend.Fail = func(op string) error {
		if op == "ack" {
			return async.ErrUnavailable
		}
		return nil
	}
	if err := worker.Process(ctx, delivery); !errors.Is(err, async.ErrUnavailable) {
		t.Fatal(err)
	}
	backend.Fail = nil
	record, err := backend.Lookup(ctx, envelope.ID)
	if err != nil || record.State != async.Running || record.ReplacementID == "" || record.Owner != "" || !record.LeaseUntil.IsZero() || len(record.Output) != 0 || successes.Load() != 0 || after.Load() != 0 {
		t.Fatal(record, err)
	}
	if err := worker.Process(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("replayed yielded original", calls.Load())
	}
	drain(t, client, worker, backend)
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	output, err := group.Join(wait)
	if err != nil || len(output) != 1 || string(output[0]) != "8" {
		t.Fatal(output, err)
	}
	record, err = backend.Lookup(ctx, envelope.ID)
	if err != nil || record.State != async.Succeeded || string(record.Output) != "7" || record.Pinned || successes.Load() != 1 || after.Load() != 1 || callback.Load() != 7 {
		t.Fatal(record, successes.Load(), after.Load(), callback.Load(), err)
	}
}

func TestReplacementCancellationBeforeGraphPreventsAllHandlers(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	var calls int
	child, _ := async.Register(registry, "replace.child", 1, func(context.Context, async.TaskContext, int) (int, error) { calls++; return 1, nil }, async.TaskOptions{})
	original, _ := async.Register(registry, "replace.cancel", 1, func(context.Context, async.TaskContext, int) (int, error) {
		s, _ := child.Signature(0)
		s = s.Set(async.WithCountdown(time.Hour))
		return 0, async.Replace(async.Chain(s, s))
	}, async.TaskOptions{})
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend, Schedules: backend})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, Client: client, ID: "worker"}
	result, err := original.Delay(ctx, client, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := result.Revoke(ctx); err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	record, err := result.Snapshot(ctx)
	if err != nil || record.State != async.Revoked || calls != 0 {
		t.Fatal(record, calls, err)
	}
	graph, err := backend.ReadGraph(ctx, record.ReplacementID)
	if err != nil || graph.State != async.Revoked || !graph.CancelRequested {
		t.Fatal(graph, err)
	}
}

func TestReplacementBoundsOutputAndScope(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"type", "scope", "principal", "depth", "unconfigured"} {
		t.Run(mode, func(t *testing.T) {
			registry := async.NewRegistry()
			var calls int
			child, _ := async.Register(registry, "replace.leaf", 1, func(context.Context, async.TaskContext, int) (int, error) { calls++; return 1, nil }, async.TaskOptions{})
			var original *async.Task[int, int]
			original, _ = async.Register(registry, "replace.bound", 1, func(_ context.Context, tc async.TaskContext, _ int) (int, error) {
				var s async.Signature
				if mode == "depth" {
					s, _ = original.Signature(0)
				} else {
					s, _ = child.Signature(0)
				}
				if mode == "scope" {
					s = s.Set(async.WithScope("different", ""))
				}
				if mode == "principal" {
					s = s.Set(async.WithScope("", "different"))
				}
				if mode == "type" {
					return 0, async.Replace(async.Group(s))
				}
				return 0, tc.Replace(s.Canvas())
			}, async.TaskOptions{})
			backend := fakes.NewMemory()
			client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend})
			if err != nil {
				t.Fatal(err)
			}
			worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, Client: client, ID: "worker"}
			if mode == "unconfigured" {
				worker.Client = nil
			}
			result, err := original.Delay(ctx, client, 0)
			if err != nil {
				t.Fatal(err)
			}
			// Deep replacements unwind one completion boundary at a time.
			for range 8 {
				drain(t, client, worker, backend)
			}
			record, err := result.Snapshot(ctx)
			if err != nil || record.State != async.Failed || calls != 0 {
				t.Fatal(record, calls, err)
			}
		})
	}
}

func TestServeChildPreservesReplacementControl(t *testing.T) {
	registry := async.NewRegistry()
	leaf, _ := async.Register(registry, "replace.process_leaf", 1, func(context.Context, async.TaskContext, int) (int, error) { return 1, nil }, async.TaskOptions{})
	_, _ = async.Register(registry, "replace.process", 1, func(context.Context, async.TaskContext, int) (int, error) {
		s, _ := leaf.Signature(0)
		return 0, async.Replace(s.Canvas())
	}, async.TaskOptions{})
	id, _ := async.NewID()
	e := async.Envelope{ProtocolVersion: 1, ID: id, Task: "replace.process", Version: 1, Args: json.RawMessage(`0`), CreatedAt: time.Now(), Queue: "default", ReplacementDepth: 2}
	input, _ := json.Marshal(async.Execution{Envelope: e, TaskContext: async.TaskContext{ID: id, ReplacementDepth: 2}})
	var output bytes.Buffer
	if err := registry.ServeChild(context.Background(), bytes.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Replacement *async.Canvas `json:"replacement"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || response.Replacement == nil || response.Replacement.Kind != "task" {
		t.Fatal(output.String(), err)
	}
}

type cancellationAtYield struct{ *fakes.Memory }

func (b *cancellationAtYield) Transition(ctx context.Context, transition async.Transition) error {
	if transition.ReplacementID != "" {
		if err := b.Memory.RequestCancel(ctx, transition.ID, ""); err != nil {
			return err
		}
	}
	return b.Memory.Transition(ctx, transition)
}

func TestReplacementCancellationWinsBeforeOwnershipTransfer(t *testing.T) {
	ctx := context.Background()
	registry := async.NewRegistry()
	child, _ := async.Register(registry, "replace.race_child", 1, func(context.Context, async.TaskContext, int) (int, error) {
		t.Error("canceled replacement executed")
		return 1, nil
	}, async.TaskOptions{})
	original, _ := async.Register(registry, "replace.race_original", 1, func(context.Context, async.TaskContext, int) (int, error) {
		s, _ := child.Signature(0)
		return 0, async.Replace(s.Canvas())
	}, async.TaskOptions{})
	backend := &cancellationAtYield{fakes.NewMemory()}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, Client: client, ID: "race-worker"}
	result, err := original.Delay(ctx, client, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	record, err := result.Snapshot(ctx)
	if err != nil || record.State != async.Revoked || record.ReplacementID != "" {
		t.Fatal(record, err)
	}
	intents, err := backend.ListIntents(ctx, 100)
	if err != nil || len(intents) != 0 {
		t.Fatal(intents, err)
	}
}

func TestReplacementRetentionStartsAtLogicalCompletion(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	registry := async.NewRegistry()
	calls := 0
	child, _ := async.Register(registry, "replace.delayed_leaf", 1, func(context.Context, async.TaskContext, int) (int, error) { return 42, nil }, async.TaskOptions{})
	original, _ := async.Register(registry, "replace.delayed_original", 1, func(context.Context, async.TaskContext, int) (int, error) {
		calls++
		s, _ := child.Signature(0)
		return 0, async.Replace(s.Set(async.WithCountdown(49 * time.Hour)).Canvas())
	}, async.TaskOptions{})
	backend := fakes.NewMemory()
	backend.Clock = func() time.Time { return now }
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend, Schedules: backend, Clock: backend.Clock})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, Client: client, ID: "retention-worker", Clock: backend.Clock}
	result, err := original.Delay(ctx, client, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	now = now.Add(48 * time.Hour)
	waiting, err := result.Snapshot(ctx)
	if err != nil || waiting.State != async.Running || !waiting.PayloadExpiresAt.IsZero() {
		t.Fatal(waiting, err)
	}
	claim, err := backend.Claim(ctx, waiting.Envelope, "replayed", time.Minute)
	if err != nil || !claim.Duplicate || claim.Acquired {
		t.Fatal(claim, err)
	}
	dispatcher := &async.DelayedDispatcher{Client: client, ID: "retention-delayed"}
	if err := dispatcher.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	stillWaiting, _ := result.Snapshot(ctx)
	if stillWaiting.State != async.Running {
		t.Fatal("completed before child ETA", stillWaiting)
	}
	now = now.Add(time.Hour)
	if err := dispatcher.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	value, err := result.Get(ctx)
	if err != nil || value != 42 || calls != 1 {
		t.Fatal(value, calls, err)
	}
	finished, _ := result.Snapshot(ctx)
	if !finished.PayloadExpiresAt.Equal(now.Add(24*time.Hour)) || !finished.TombstoneUntil.Equal(now.Add(7*24*time.Hour)) {
		t.Fatal(finished)
	}
	now = now.Add(25 * time.Hour)
	if _, err := result.Get(ctx); !errors.Is(err, async.ErrResultExpired) {
		t.Fatal(err)
	}
	claim, err = backend.Claim(ctx, waiting.Envelope, "old-original", time.Minute)
	if err != nil || !claim.Duplicate || claim.Acquired {
		t.Fatal(claim, err)
	}
}

func replacementProcessRegistry() (*async.Registry, *async.Task[int, int]) {
	registry := async.NewRegistry()
	child, _ := async.Register(registry, "replace.isolated_child", 1, func(context.Context, async.TaskContext, int) (int, error) { return 42, nil }, async.TaskOptions{})
	original, _ := async.Register(registry, "replace.isolated_original", 1, func(context.Context, async.TaskContext, int) (int, error) {
		s, _ := child.Signature(0)
		return -100, async.Replace(s.Set(async.WithCountdown(time.Hour)).Canvas())
	}, async.TaskOptions{HardLimit: 5 * time.Second})
	return registry, original
}

func TestReplacementProcessChildHelper(t *testing.T) {
	if os.Getenv("GOGO_TEST_REPLACEMENT_CHILD") != "1" {
		return
	}
	registry, _ := replacementProcessRegistry()
	if err := registry.ServeChild(context.Background(), os.Stdin, os.Stdout); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestProcessExecutorYieldsWithoutPublishingPrematureSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	registry, original := replacementProcessRegistry()
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Workflows: backend, Schedules: backend})
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executor := &async.ProcessExecutor{Command: []string{binary, "-test.run=^TestReplacementProcessChildHelper$"}, Environment: append(os.Environ(), "GOGO_TEST_REPLACEMENT_CHILD=1")}
	if errors.Is(executor.Validate(), async.ErrUnavailable) {
		t.Skip("built-in process-tree isolation is unavailable on this platform")
	}
	worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, Client: client, Executor: executor, ID: "isolated-worker"}
	result, err := original.Delay(ctx, client, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	record, err := result.Snapshot(ctx)
	if err != nil || record.State != async.Running || record.ReplacementID == "" || len(record.Output) != 0 || record.Owner != "" {
		t.Fatal(record, err)
	}
	if err := result.Revoke(ctx); err != nil {
		t.Fatal(err)
	}
	drain(t, client, worker, backend)
	record, err = result.Snapshot(ctx)
	if err != nil || record.State != async.Revoked {
		t.Fatal(record, err)
	}
}
