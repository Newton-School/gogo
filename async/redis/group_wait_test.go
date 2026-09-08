package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
)

type nativeGroupWaitResult struct {
	values   []json.RawMessage
	outcomes []async.Completion
	members  []async.GroupMember
	err      error
}

func startNativeGroupWait(t *testing.T, parent context.Context, group *async.GroupResult, method string) <-chan nativeGroupWaitResult {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	result := make(chan nativeGroupWaitResult, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		var out nativeGroupWaitResult
		switch method {
		case "join":
			out.values, out.err = group.Join(ctx)
		case "outcomes":
			out.outcomes, out.err = group.JoinOutcomes(ctx)
		default:
			for member, err := range group.Iterate(ctx) {
				if err != nil {
					out.err = err
					break
				}
				out.members = append(out.members, member)
			}
		}
		result <- out
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(time.Second):
			t.Error("group wait did not stop after its context canceled")
		}
	})
	return result
}

func TestRealRedisGroupWaitStartsBeforeTerminalCompletion(t *testing.T) {
	for _, method := range []string{"join", "outcomes", "iterate"} {
		for _, failed := range []bool{false, true} {
			t.Run(method+"/failed="+map[bool]string{false: "no", true: "yes"}[failed], func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				broker, results := backends(t)
				workflows := &adapter.Workflows{Connection: results.Connection}
				registry := async.NewRegistry()
				task, err := async.Register(registry, "wait.member", 1, func(_ context.Context, _ async.TaskContext, value int) (int64, error) {
					if failed && value == 2 {
						return 0, errors.New("private member failure")
					}
					return 9007199254740993 + int64(value), nil
				}, async.TaskOptions{})
				if err != nil {
					t.Fatal(err)
				}
				polled := make(chan struct{}, 1)
				var reads atomic.Int32
				var groupID string
				client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows,
					Authorize: func(_ context.Context, action, scope, id string) error {
						if scope != "tenant" {
							return async.ErrDenied
						}
						if action == "read" && id == groupID {
							reads.Add(1)
							select {
							case polled <- struct{}{}:
							default:
							}
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
				groupID = group.ID
				relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{workflows}, ID: "wait-relay"}
				if err := relay.Tick(ctx); err != nil {
					t.Fatal(err)
				}
				deliveries := map[int]async.Delivery{}
				for range 2 {
					delivery, err := broker.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "wait-worker"})
					if err != nil {
						t.Fatal(err)
					}
					envelope, err := async.DecodeEnvelope(delivery.Body)
					if err != nil {
						t.Fatal(err)
					}
					var value int
					if err := json.Unmarshal(envelope.Args, &value); err != nil {
						t.Fatal(err)
					}
					deliveries[value] = delivery
				}
				waiting := startNativeGroupWait(t, ctx, group, method)
				select {
				case <-polled:
				case <-ctx.Done():
					t.Fatal("wait never read the nonterminal group")
				}
				select {
				case <-waiting:
					t.Fatal("nonterminal group completed before any member ran")
				default:
				}
				worker := &async.Worker{Registry: registry, Broker: broker, Results: results, ID: "wait-worker"}
				relay.Sources = []async.IntentStore{results}
				for _, value := range []int{2, 1} {
					if err := worker.Process(ctx, deliveries[value]); err != nil {
						t.Fatal(err)
					}
					if err := relay.Tick(ctx); err != nil {
						t.Fatal(err)
					}
				}
				var out nativeGroupWaitResult
				select {
				case out = <-waiting:
				case <-ctx.Done():
					t.Fatal("wait did not observe durable terminal completions")
				}
				if reads.Load() < 2 {
					t.Fatal("wait did not reauthorize after the first observation")
				}
				if method == "join" {
					if failed {
						var failure async.Failure
						if !errors.As(out.err, &failure) || len(out.values) != 0 {
							t.Fatal("propagating join hid failure")
						}
					} else if out.err != nil || len(out.values) != 2 || string(out.values[0]) != "9007199254740994" || string(out.values[1]) != "9007199254740995" {
						t.Fatal("ordered join output changed", out.err)
					}
					return
				}
				if method == "iterate" {
					if len(out.members) != 2 || out.members[0].Index == out.members[1].Index {
						t.Fatal("missing or duplicate member")
					}
					out.outcomes = make([]async.Completion, 2)
					for _, member := range out.members {
						if member.Index < 0 || member.Index >= 2 {
							t.Fatal("invalid original position")
						}
						out.outcomes[member.Index] = member.Outcome
					}
				}
				if out.err != nil || len(out.outcomes) != 2 || string(out.outcomes[0].Output) != "9007199254740994" {
					t.Fatal("non-propagating result changed", out.err)
				}
				if failed {
					if out.outcomes[1].State != async.Failed || out.outcomes[1].Failure == nil {
						t.Fatal("member failure was not retained")
					}
				} else if string(out.outcomes[1].Output) != "9007199254740995" {
					t.Fatal("member precision or order changed")
				}
			})
		}
	}
}

func TestRealRedisGroupWaitTimeoutAndRevokedReadDoNotCancelWork(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "denied"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			broker, results := backends(t)
			workflows := &adapter.Workflows{Connection: results.Connection}
			registry := async.NewRegistry()
			task, err := async.Register(registry, "wait.retained", 1, func(_ context.Context, _ async.TaskContext, value int) (int, error) { return value, nil }, async.TaskOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var allowed atomic.Bool
			allowed.Store(true)
			polled := make(chan struct{}, 1)
			client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Workflows: workflows,
				Authorize: func(_ context.Context, action, _, _ string) error {
					if action == "read" {
						if !allowed.Load() {
							return async.ErrDenied
						}
						select {
						case polled <- struct{}{}:
						default:
						}
					}
					return nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			signature, _ := task.Signature(7)
			group, err := client.ApplyCanvas(ctx, async.Group(signature))
			if err != nil {
				t.Fatal(err)
			}
			relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{workflows}, ID: "wait-retained-relay"}
			if err := relay.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			waitContext, stop := context.WithCancel(ctx)
			if mode == "deadline" {
				stop()
				waitContext, stop = context.WithTimeout(ctx, time.Second)
			}
			defer stop()
			waiting := startNativeGroupWait(t, waitContext, group, "outcomes")
			select {
			case <-polled:
			case <-ctx.Done():
				t.Fatal("wait did not begin")
			}
			want := error(context.DeadlineExceeded)
			if mode == "cancel" {
				stop()
				want = context.Canceled
			} else if mode == "denied" {
				allowed.Store(false)
				want = async.ErrDenied
			}
			select {
			case out := <-waiting:
				if out.err != want || len(out.outcomes) != 0 {
					t.Fatal("stopped wait fabricated completion", len(out.outcomes), out.err)
				}
			case <-ctx.Done():
				t.Fatal("wait ignored cancellation or revoked read authority")
			}
			allowed.Store(true)
			graph, err := group.Snapshot(ctx)
			if err != nil || graph.State != async.Running || graph.CancelRequested {
				t.Fatal("waiting changed execution state", graph.State, err)
			}
			worker := &async.Worker{Registry: registry, Broker: broker, Results: results, ID: "wait-retained-worker"}
			if err := worker.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			relay.Sources = []async.IntentStore{results}
			if err := relay.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			values, err := group.Join(ctx)
			if err != nil || len(values) != 1 || string(values[0]) != "7" {
				t.Fatal("stopped wait canceled retained work", err)
			}
		})
	}
}
