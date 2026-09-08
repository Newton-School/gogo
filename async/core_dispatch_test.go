package async_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
	"github.com/Newton-School/gogo/core/tasks"
)

func coreDispatchFixture(t *testing.T, mutate func(*async.ClientConfig)) (tasks.Dispatcher, *async.Client, *async.Worker, *fakes.Memory) {
	t.Helper()
	cfg, _, memory := producerRetryFixture(t)
	cfg.PublishRetry = nil
	if mutate != nil {
		mutate(&cfg)
	}
	client := producerClient(t, cfg)
	dispatcher, err := async.NewCoreDispatcher(client)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher, client, &async.Worker{ID: "core-dispatch", Registry: cfg.Registry, Broker: memory, Results: memory}, memory
}

func coreDispatchRequest() tasks.Request {
	return tasks.Request{Task: "producer.test", Version: 1, Args: json.RawMessage("4")}
}

func TestCoreDispatchEnqueueAndResult(t *testing.T) {
	dispatcher, _, worker, memory := coreDispatchFixture(t, nil)
	ctx := context.Background()
	result, err := dispatcher.Enqueue(ctx, coreDispatchRequest())
	if err != nil || result == nil || result.ID() == "" {
		t.Fatal(result, err)
	}
	record, err := memory.Lookup(ctx, result.ID())
	if err != nil || record.State != async.Queued || record.Envelope.Retries != 0 {
		t.Fatal(record.State, err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	value, err := result.Get(ctx)
	if err != nil || string(value) != "5" {
		t.Fatal(string(value), err)
	}
	value[0] = '9'
	if next, err := result.Get(ctx); err != nil || string(next) != "5" {
		t.Fatal("result alias", string(next), err)
	}
}

func TestCoreDispatchInvalidAndDeniedNeverPublish(t *testing.T) {
	for name, mutate := range map[string]func(*tasks.Request){
		"version":        func(r *tasks.Request) { r.Version = 2 },
		"unknown task":   func(r *tasks.Request) { r.Task = "missing.task" },
		"invalid JSON":   func(r *tasks.Request) { r.Args = json.RawMessage("{") },
		"oversized JSON": func(r *tasks.Request) { r.Args = json.RawMessage(strings.Repeat(" ", tasks.MaxPayloadBytes+1)) },
		"UTF8":           func(r *tasks.Request) { r.Args = json.RawMessage{'"', 255, '"'} },
		"schema":         func(r *tasks.Request) { r.Args = json.RawMessage(`"not an integer"`) },
		"ID":             func(r *tasks.Request) { r.ID = "invalid" },
		"scope":          func(r *tasks.Request) { r.Scope = strings.Repeat("s", 257) },
		"queue":          func(r *tasks.Request) { r.Queue = "not-configured" },
		"denial":         func(r *tasks.Request) { r.Scope = "private" },
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			dispatcher, _, _, _ := coreDispatchFixture(t, func(c *async.ClientConfig) {
				c.Broker = producerBroker{Broker: c.Broker, call: func(context.Context, async.Envelope) error { calls++; return nil }}
			})
			request := coreDispatchRequest()
			mutate(&request)
			result, err := dispatcher.Enqueue(context.Background(), request)
			want := tasks.ErrInvalid
			if name == "version" || name == "unknown task" {
				want = tasks.ErrUnknownTask
			}
			if name == "denial" {
				want = tasks.ErrDenied
			}
			if result != nil || !errors.Is(err, want) || calls != 0 {
				t.Fatal("refusal published", result, err, calls)
			}
		})
	}
}

func TestCoreDispatchAcceptanceRetainsPortableAndExactIdentity(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(fmt.Sprint(confirmed), func(t *testing.T) {
			calls, registers := 0, 0
			dispatcher, client, _, memory := coreDispatchFixture(t, func(c *async.ClientConfig) {
				broker, results := c.Broker, c.Results
				c.Broker = producerBroker{Broker: broker, call: func(ctx context.Context, envelope async.Envelope) error {
					calls++
					if err := broker.Publish(ctx, envelope); err != nil {
						return err
					}
					if !confirmed && calls == 1 {
						return io.ErrUnexpectedEOF
					}
					return nil
				}}
				c.Results = producerResults{ResultStore: results, call: func(ctx context.Context, envelope async.Envelope, state async.State) error {
					registers++
					if confirmed && registers == 1 {
						return io.ErrUnexpectedEOF
					}
					return results.Register(ctx, envelope, state)
				}}
			})
			result, err := dispatcher.Enqueue(context.Background(), coreDispatchRequest())
			var portable *tasks.AcceptanceError
			var original *async.AcceptanceError
			if !errors.As(err, &portable) || !errors.As(err, &original) || result == nil || result.ID() != original.ID || portable.ID != original.ID || portable.Confirmed != confirmed || original.Confirmed != confirmed || !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, tasks.ErrUnavailable) || calls != 1 {
				t.Fatal("acceptance identity lost", result, err, calls)
			}
			if _, err := result.Get(context.Background()); !errors.Is(err, tasks.ErrNotFound) {
				t.Fatal("unknown admission fabricated a result", err)
			}
			receipt, err := original.Retry(context.Background(), client)
			if err != nil || receipt.ID != result.ID() || calls != 2-boolInt(confirmed) {
				t.Fatal("exact replay changed stage or identity", receipt, err, calls)
			}
			if record, err := memory.Lookup(context.Background(), result.ID()); err != nil || record.Envelope.Retries != 0 {
				t.Fatal(record.State, err)
			}
		})
	}
}

func TestCoreDispatchRejectsForeignAcceptanceBeforePublication(t *testing.T) {
	for _, source := range []string{"bare forged", "borrowed real"} {
		for _, stage := range []string{"validation", "ID callback"} {
			t.Run(source+"/"+stage, func(t *testing.T) {
				foreign := &async.AcceptanceError{ID: async.StableID("foreign", "task"), Confirmed: true, Cause: io.ErrUnexpectedEOF}
				if source == "borrowed real" {
					cfg, task, _ := producerRetryFixture(t)
					cfg.PublishRetry = nil
					cfg.Broker = producerBroker{Broker: cfg.Broker, call: func(context.Context, async.Envelope) error { return io.ErrUnexpectedEOF }}
					client := producerClient(t, cfg)
					signature, err := task.Signature(4)
					if err != nil {
						t.Fatal(err)
					}
					_, err = client.Enqueue(context.Background(), signature)
					if !errors.As(err, &foreign) {
						t.Fatal("missing real prior acceptance")
					}
				}
				registry, memory := async.NewRegistry(), fakes.NewMemory()
				_, err := async.Register[int, int](registry, "core.refusal", 1, nil, async.TaskOptions{
					ValidatePayload: func(json.RawMessage) error {
						if stage == "validation" {
							return foreign
						}
						return nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				config := async.ClientConfig{Registry: registry, Results: memory, Broker: producerBroker{Broker: memory, call: func(context.Context, async.Envelope) error { calls++; return nil }}}
				if stage == "ID callback" {
					config.NewID = func() (string, error) { return "", foreign }
				}
				dispatcher, err := async.NewCoreDispatcher(producerClient(t, config))
				if err != nil {
					t.Fatal(err)
				}
				handle, err := dispatcher.Enqueue(context.Background(), tasks.Request{Task: "core.refusal", Version: 1, Args: json.RawMessage("1")})
				var acceptance *tasks.AcceptanceError
				if handle != nil || err == nil || errors.As(err, &acceptance) || calls != 0 {
					t.Fatal("prepublication callback error forged current admission", handle != nil, err, calls)
				}
				var retained *async.AcceptanceError
				if !errors.As(err, &retained) || retained != foreign || !errors.Is(err, foreign) {
					t.Fatal("prepublication diagnostic cause was lost")
				}
			})
		}
	}
}

func TestCoreDispatchCancellationPreservesAttemptedOutcome(t *testing.T) {
	for _, stage := range []string{"validation", "publish", "unknown publish", "register"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			registry, memory := async.NewRegistry(), fakes.NewMemory()
			_, err := async.Register(registry, "core.cancel", 1, func(context.Context, async.TaskContext, int) (int, error) { return 1, nil }, async.TaskOptions{
				ValidatePayload: func(json.RawMessage) error {
					if stage == "validation" {
						cancel()
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			client := producerClient(t, async.ClientConfig{Registry: registry, Broker: producerBroker{Broker: memory, call: func(ctx context.Context, envelope async.Envelope) error {
				calls++
				if err := memory.Publish(ctx, envelope); err != nil {
					return err
				}
				if stage == "publish" || stage == "unknown publish" {
					cancel()
				}
				if stage == "unknown publish" {
					return io.ErrUnexpectedEOF
				}
				return nil
			}}, Results: producerResults{ResultStore: memory, call: func(ctx context.Context, e async.Envelope, state async.State) error {
				if err := memory.Register(ctx, e, state); err != nil {
					return err
				}
				if stage == "register" {
					cancel()
				}
				return nil
			}}})
			dispatcher, err := async.NewCoreDispatcher(client)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := dispatcher.Enqueue(ctx, tasks.Request{Task: "core.cancel", Version: 1, Args: json.RawMessage("1")})
			if !errors.Is(err, context.Canceled) {
				t.Fatal("joined producer cancellation was lost", err)
			}
			var portable *tasks.AcceptanceError
			var original *async.AcceptanceError
			if stage == "validation" {
				if handle != nil || errors.As(err, &portable) || calls != 0 {
					t.Fatal("pre-publication cancellation claimed a write", handle, err, calls)
				}
				return
			}
			if handle == nil || !errors.As(err, &portable) || !errors.As(err, &original) || portable.ID != handle.ID() || original.ID != handle.ID() || portable.Confirmed != (stage != "unknown publish") || calls != 1 {
				t.Fatal("cancellation discarded attempted acceptance", handle, err, calls)
			}
			if stage == "unknown publish" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal("lost uncertain transport cause")
			}
		})
	}
}

func TestCoreDispatchRespectsOnlyConfiguredProducerRetry(t *testing.T) {
	for _, mode := range []string{"default one attempt", "configured success", "configured exhausted"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			dispatcher, _, _, _ := coreDispatchFixture(t, func(c *async.ClientConfig) {
				if mode != "default one attempt" {
					c.PublishRetry = &async.PublishRetryPolicy{MaxAttempts: 2, InitialDelay: time.Millisecond, MaxDelay: time.Millisecond, DisableJitter: true}
				}
				broker := c.Broker
				c.Broker = producerBroker{Broker: broker, call: func(ctx context.Context, envelope async.Envelope) error {
					calls++
					if mode == "configured success" && calls == 2 {
						return broker.Publish(ctx, envelope)
					}
					return async.ErrUnavailable
				}}
			})
			handle, err := dispatcher.Enqueue(context.Background(), coreDispatchRequest())
			wantCalls := 2
			if mode == "default one attempt" {
				wantCalls = 1
			}
			if handle == nil || calls != wantCalls || (err == nil) != (mode == "configured success") {
				t.Fatal("bridge added or removed a retry layer", handle, err, calls)
			}
			if err != nil {
				var acceptance *tasks.AcceptanceError
				if !errors.As(err, &acceptance) || acceptance.Confirmed {
					t.Fatal("incorrect exhausted acceptance", err)
				}
			}
		})
	}
}

type coreDispatchPrivateError struct{ calls *int }

func (e *coreDispatchPrivateError) Error() string { *e.calls++; panic("private provider detail") }

func TestCoreDispatchDoesNotInspectProviderErrorMethods(t *testing.T) {
	calls := 0
	cause := &coreDispatchPrivateError{calls: &calls}
	dispatcher, _, _, _ := coreDispatchFixture(t, func(c *async.ClientConfig) {
		c.Broker = producerBroker{Broker: c.Broker, call: func(context.Context, async.Envelope) error { return cause }}
	})
	handle, err := dispatcher.Enqueue(context.Background(), coreDispatchRequest())
	var original *async.AcceptanceError
	if handle == nil || !errors.As(err, &original) || original.Cause != cause {
		t.Fatal("provider cause not retained")
	}
	for _, format := range []string{"%s", "%v", "%+v", "%#v"} {
		if message := fmt.Sprintf(format, err); strings.Contains(message, "private") || strings.Contains(message, "PANIC") {
			t.Fatal("unsafe formatter", message)
		}
	}
	if calls != 0 {
		t.Fatal("provider Error method invoked")
	}
}

func TestCoreDispatchFreezesRequestClientAndResult(t *testing.T) {
	for _, replacementAt := range []string{"context", "clock", "grant", "publish", "construction"} {
		t.Run(replacementAt, func(t *testing.T) {
			request := coreDispatchRequest()
			var client *async.Client
			var dispatcher tasks.Dispatcher
			other, otherClient, _, _ := coreDispatchFixture(t, nil)
			armed := false
			replace := func(stage string) {
				if armed && stage == replacementAt {
					armed = false
					request.Args[0] = '9'
					*client = *otherClient
					dispatcher = other
				}
			}
			var worker *async.Worker
			dispatcher, client, worker, _ = coreDispatchFixture(t, func(c *async.ClientConfig) {
				c.Clock = func() time.Time { replace("clock"); return time.Now() }
				c.Authorize = func(context.Context, string, string, string) error { replace("grant"); return nil }
				broker := c.Broker
				c.Broker = producerBroker{Broker: broker, call: func(ctx context.Context, e async.Envelope) error { replace("publish"); return broker.Publish(ctx, e) }}
			})
			original := dispatcher
			armed = true
			if replacementAt == "construction" {
				*client = *otherClient
				armed = false
			}
			ctx := &coreDispatchContext{Context: context.Background(), onErr: func() error { replace("context"); return nil }}
			result, err := original.Enqueue(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if value, err := result.Get(context.Background()); err != nil || string(value) != "5" {
				t.Fatal("callback retargeted publication/result", string(value), err)
			}
		})
	}
}

func TestCoreDispatchResultPortableOutcomes(t *testing.T) {
	for _, mode := range []string{"missing", "expired", "denied", "unavailable", "failure", "revoked", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			var store *resultBoundaryStore
			dispatcher, _, _, memory := coreDispatchFixture(t, func(c *async.ClientConfig) {
				store = &resultBoundaryStore{ResultStore: c.Results}
				c.Results = store
				c.Authorize = func(_ context.Context, action, _, _ string) error {
					if action == "read" && mode == "denied" {
						return errors.New("private denial")
					}
					return nil
				}
			})
			result, err := dispatcher.Enqueue(context.Background(), coreDispatchRequest())
			if err != nil {
				t.Fatal(err)
			}
			record, _ := memory.Lookup(context.Background(), result.ID())
			record = coreDispatchTerminal(record)
			store.lookup = func(context.Context, string) (async.Record, error) {
				switch mode {
				case "missing":
					return async.Record{}, async.ErrNotFound
				case "unavailable":
					return record, errors.New("private provider detail")
				case "expired":
					record.Output = nil
				case "failure":
					record.State = async.Failed
					record.Failure = &async.Failure{Code: "FAILED", Message: "Task failed"}
				case "revoked":
					record.State = async.Revoked
					record.Output = nil
				case "malformed":
					record.Envelope.ID = async.StableID("wrong", "task")
				}
				return record, nil
			}
			value, err := result.Get(context.Background())
			if len(value) != 0 || err == nil || strings.Contains(fmt.Sprintf("%#v", err), "private") {
				t.Fatal("unsafe failure", string(value), err)
			}
			if mode == "failure" || mode == "revoked" {
				var failure tasks.Failure
				if !errors.As(err, &failure) || failure.Code == "" {
					t.Fatal("lost terminal failure", err)
				}
			} else {
				want := map[string]error{"missing": tasks.ErrNotFound, "expired": tasks.ErrResultExpired, "denied": tasks.ErrDenied, "unavailable": tasks.ErrUnavailable, "malformed": tasks.ErrUnavailable}[mode]
				if !errors.Is(err, want) {
					t.Fatal("incorrect portable classification", err)
				}
			}
		})
	}
}

func TestCoreDispatchConfigurationContextsAndConcurrency(t *testing.T) {
	for _, client := range []*async.Client{nil, {}} {
		if value, err := async.NewCoreDispatcher(client); value != nil || err != tasks.ErrConfiguration {
			t.Fatal(value, err)
		}
	}
	var nilBroker *producerRetryTypedNilBroker
	var nilResults *producerRetryTypedNilResults
	for _, port := range []string{"broker", "results"} {
		cfg, _, _ := producerRetryFixture(t)
		if port == "broker" {
			cfg.Broker = nilBroker
		} else {
			cfg.Results = nilResults
		}
		client := producerClient(t, cfg)
		if value, err := async.NewCoreDispatcher(client); value != nil || err != tasks.ErrConfiguration {
			t.Fatal("typed-nil provider accepted", value, err)
		}
	}
	dispatcher, _, _, _ := coreDispatchFixture(t, nil)
	var typedNil *coreDispatchContext
	for _, ctx := range []context.Context{nil, typedNil, &coreDispatchContext{Context: context.Background(), onErr: func() error { panic("private") }}} {
		if result, err := dispatcher.Enqueue(ctx, coreDispatchRequest()); result != nil || err == nil {
			t.Fatal(result, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := dispatcher.Enqueue(ctx, coreDispatchRequest()); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatal(result, err)
	}
	var group sync.WaitGroup
	ids := make(chan string, 16)
	for range 16 {
		group.Go(func() {
			result, err := dispatcher.Enqueue(context.Background(), coreDispatchRequest())
			if err != nil {
				t.Error(err)
				return
			}
			ids <- result.ID()
		})
	}
	group.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatal("duplicate automatic identity")
		}
		seen[id] = true
	}
	if len(seen) != 16 {
		t.Fatal("missing concurrent result")
	}
}

type producerRetryTypedNilBroker struct{ async.Broker }
type producerRetryTypedNilResults struct{ async.ResultStore }

func TestCoreDispatchWorkerWaitGuard(t *testing.T) {
	memory, registry := fakes.NewMemory(), async.NewRegistry()
	var handle tasks.Result
	var observed error
	_, err := async.Register(registry, "core.wait", 1, func(ctx context.Context, _ async.TaskContext, value int) (int, error) {
		_, observed = handle.Get(ctx)
		return value, nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client := producerClient(t, async.ClientConfig{Registry: registry, Broker: memory, Results: memory})
	dispatcher, err := async.NewCoreDispatcher(client)
	if err != nil {
		t.Fatal(err)
	}
	handle, err = dispatcher.Enqueue(context.Background(), tasks.Request{Task: "core.wait", Version: 1, Args: json.RawMessage("1")})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{ID: "core-wait", Registry: registry, Broker: memory, Results: memory}
	if err := worker.RunOnce(context.Background()); err != nil || !errors.Is(observed, tasks.ErrWorkerJoin) || !errors.Is(observed, async.ErrWorkerJoin) {
		t.Fatal("worker wait was not refused", err, observed)
	}
}

func FuzzCoreDispatchRequest(f *testing.F) {
	f.Add("producer.test", 1, []byte("4"), "", "", "")
	f.Add("producer.test", 1, []byte("null"), "", "default", "private")
	f.Fuzz(func(t *testing.T, task string, version int, raw []byte, id, queue, scope string) {
		if len(raw) > tasks.MaxPayloadBytes+1 || len(task)+len(id)+len(queue)+len(scope) > 2048 {
			t.Skip()
		}
		dispatcher, _, _, _ := coreDispatchFixture(t, nil)
		before := append([]byte(nil), raw...)
		result, err := dispatcher.Enqueue(context.Background(), tasks.Request{Task: task, Version: version, Args: raw, ID: id, Queue: queue, Scope: scope})
		if !reflect.DeepEqual(before, []byte(raw)) && !(len(before) == 0 && len(raw) == 0) {
			t.Fatal("mutated caller JSON")
		}
		if err == nil && (result == nil || result.ID() == "") {
			t.Fatal("successful admission lost identity")
		}
	})
}
