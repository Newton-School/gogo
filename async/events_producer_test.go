package async_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

type eventSinkFunc func(context.Context, async.Event) error

func (f eventSinkFunc) PublishEvent(ctx context.Context, event async.Event) error {
	return f(ctx, event)
}

type eventResultStore struct {
	async.ResultStore
	lookup, register error
}

func (r eventResultStore) Lookup(ctx context.Context, id string) (async.Record, error) {
	if r.lookup != nil {
		return async.Record{}, r.lookup
	}
	return r.ResultStore.Lookup(ctx, id)
}
func (r eventResultStore) Register(ctx context.Context, e async.Envelope, state async.State) error {
	if r.register != nil {
		return r.register
	}
	return r.ResultStore.Register(ctx, e, state)
}

type eventBroker struct {
	async.Broker
	err error
}

func (b eventBroker) Publish(ctx context.Context, e async.Envelope) error {
	if b.err != nil {
		return b.err
	}
	return b.Broker.Publish(ctx, e)
}

func producerFixture(t *testing.T, change func(*async.ClientConfig)) (*async.Client, *fakes.Memory, async.Signature) {
	t.Helper()
	backend := fakes.NewMemory()
	registry := async.NewRegistry()
	task, err := async.Register(registry, "event.produce", 1, func(context.Context, async.TaskContext, string) (string, error) { return "private-result", nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	config := async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Schedules: backend, Clock: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }}
	change(&config)
	client, err := async.NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := task.Signature("private-argument")
	if err != nil {
		t.Fatal(err)
	}
	signature.Options.ID = async.StableID("producer", t.Name())
	return client, backend, signature
}

func TestProducerEventsObserveConfirmedPlacementAndSkipRunningTerminalReplay(t *testing.T) {
	ctx := context.Background()
	var events []async.Event
	client, backend, signature := producerFixture(t, func(c *async.ClientConfig) {
		c.Events = eventSinkFunc(func(_ context.Context, e async.Event) error { events = append(events, e); return nil })
	})
	for i := 0; i < 2; i++ {
		if _, err := client.Enqueue(ctx, signature); err != nil {
			t.Fatal(err)
		}
	}
	if len(events) != 2 || events[0].Kind != "task_queued" || events[0].State != async.Queued {
		t.Fatal(events)
	}
	for _, event := range events {
		if err := async.ValidateEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	record, err := backend.Lookup(ctx, signature.Options.ID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := backend.Claim(ctx, record.Envelope, "owner", time.Minute)
	if err != nil || !claim.Acquired {
		t.Fatal(claim, err)
	}
	if _, err := client.Enqueue(ctx, signature); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatal("running replay announced new queued work", events)
	}
	if err := backend.Transition(ctx, async.Transition{ID: record.Envelope.ID, Owner: "owner", Fence: claim.Record.Fence, State: async.Succeeded, Output: []byte(`"private-result"`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Enqueue(ctx, signature); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatal("terminal replay announced new queued work", events)
	}
	scheduled, _, sig := producerFixture(t, func(c *async.ClientConfig) {
		c.Events = eventSinkFunc(func(_ context.Context, e async.Event) error { events = append(events, e); return nil })
	})
	sig.Options.ID = async.StableID("producer", "scheduled")
	sig.Options.Countdown = time.Hour
	receipt, err := scheduled.Enqueue(ctx, sig)
	if err != nil || receipt.State != async.Scheduled || events[len(events)-1].State != async.Scheduled {
		t.Fatal(receipt, events, err)
	}
}

func TestProducerObserverFailureNeverChangesConfirmedAcceptance(t *testing.T) {
	for _, scenario := range []string{"sink_error", "sink_panic", "lookup_error", "timeout", "cancel", "reporter_panic"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reported := 0
			client, backend, sig := producerFixture(t, func(c *async.ClientConfig) {
				c.EventTimeout = time.Millisecond
				c.OnEventError = func(err error) {
					reported++
					if strings.Contains(err.Error(), "private") {
						t.Error("observer leaked provider details")
					}
					if scenario == "reporter_panic" {
						panic("private observer panic")
					}
				}
				if scenario == "lookup_error" {
					c.Results = eventResultStore{ResultStore: c.Results, lookup: errors.New("private backend URL")}
				}
				c.Events = eventSinkFunc(func(ctx context.Context, _ async.Event) error {
					switch scenario {
					case "sink_panic":
						panic("private sink panic")
					case "timeout":
						<-ctx.Done()
						return ctx.Err()
					case "cancel":
						cancel()
						return nil
					default:
						return errors.New("private event endpoint")
					}
				})
			})
			receipt, err := client.Enqueue(ctx, sig)
			if err != nil || receipt.ID != sig.Options.ID || receipt.State != async.Queued || reported != 1 {
				t.Fatal(receipt, err, reported)
			}
			if record, err := backend.Lookup(context.Background(), sig.Options.ID); err != nil || record.State != async.Queued {
				t.Fatal(record.State, err)
			}
		})
	}
}

func TestProducerDoesNotObserveUnknownOrUnregisteredAcceptance(t *testing.T) {
	for _, phase := range []string{"broker", "results", "denied"} {
		t.Run(phase, func(t *testing.T) {
			calls := 0
			client, _, sig := producerFixture(t, func(c *async.ClientConfig) {
				c.Events = eventSinkFunc(func(context.Context, async.Event) error { calls++; return nil })
				switch phase {
				case "broker":
					c.Broker = eventBroker{Broker: c.Broker, err: errors.New("private broker")}
				case "results":
					c.Results = eventResultStore{ResultStore: c.Results, register: errors.New("private result")}
				case "denied":
					c.Authorize = func(context.Context, string, string, string) error { return async.ErrDenied }
				}
			})
			_, err := client.Enqueue(context.Background(), sig)
			if err == nil || calls != 0 {
				t.Fatal(err, calls)
			}
			if phase != "denied" {
				var acceptance *async.AcceptanceError
				if !errors.As(err, &acceptance) || acceptance.Confirmed != (phase == "results") {
					t.Fatal("acceptance classification changed", err)
				}
			}
		})
	}
}

func TestProducerDelayedDispatchObservesBrokerAcceptanceWithoutChangingFireOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var states []async.State
	reports := 0
	client, backend, signature := producerFixture(t, func(c *async.ClientConfig) {
		c.Clock = func() time.Time { return now }
		c.OnEventError = func(error) { reports++ }
		c.Events = eventSinkFunc(func(_ context.Context, e async.Event) error {
			states = append(states, e.State)
			if e.State == async.Queued {
				cancel()
				return errors.New("private observer outage")
			}
			return nil
		})
	})
	backend.Clock = func() time.Time { return now }
	signature.Options.Countdown = time.Hour
	if _, err := client.Enqueue(ctx, signature); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	dispatcher := &async.DelayedDispatcher{Client: client, ID: "dispatcher"}
	if err := dispatcher.Tick(ctx); err != nil {
		t.Fatal("event failure changed committed fire", err)
	}
	if err := dispatcher.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0] != async.Scheduled || states[1] != async.Queued || reports != 1 {
		t.Fatal(states, reports)
	}
	if delivery, err := backend.Consume(context.Background(), async.ConsumeOptions{Queues: []string{"default"}, Consumer: "observer"}); err != nil || delivery.Receipt == "" {
		t.Fatal(delivery, err)
	}
}

func TestWorkerProgressEventsFollowDurableWriteAndNeverChangeProgressOutcome(t *testing.T) {
	for _, mode := range []string{"success", "storage_failure", "sink_failure", "sink_panic"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			backend := fakes.NewMemory()
			storageErr := errors.New("private progress provider")
			if mode == "storage_failure" {
				backend.Fail = func(op string) error {
					if op == "progress" {
						return storageErr
					}
					return nil
				}
			}
			registry := async.NewRegistry()
			task, err := async.Register(registry, "event.progress", 1, func(ctx context.Context, task async.TaskContext, _ int) (int, error) {
				err := task.Progress(ctx, map[string]string{"private": "private-progress"})
				if mode == "storage_failure" {
					if !errors.Is(err, storageErr) {
						t.Error(err)
					}
				} else if err != nil {
					t.Error("event failure changed progress return", err)
				}
				return 42, nil
			}, async.TaskOptions{})
			if err != nil {
				t.Fatal(err)
			}
			client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend})
			if err != nil {
				t.Fatal(err)
			}
			result, err := task.Delay(ctx, client, 1)
			if err != nil {
				t.Fatal(err)
			}
			delivery, err := backend.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
			if err != nil {
				t.Fatal(err)
			}
			progressEvents, reports := 0, 0
			worker := &async.Worker{Registry: registry, Broker: backend, Results: backend, ID: "worker", OnError: func(error) { reports++ }, Events: eventSinkFunc(func(_ context.Context, e async.Event) error {
				if e.Kind == "task_progress" {
					progressEvents++
					if mode == "sink_failure" {
						return errors.New("private event backend")
					}
					if mode == "sink_panic" {
						panic("private event panic")
					}
				}
				return nil
			})}
			if err := worker.Process(ctx, delivery); err != nil {
				t.Fatal(err)
			}
			if value, err := result.Get(ctx); err != nil || value != 42 {
				t.Fatal(value, err)
			}
			wantEvents, wantReports := 1, 0
			if mode == "storage_failure" {
				wantEvents = 0
			}
			if mode == "sink_failure" || mode == "sink_panic" {
				wantReports = 1
			}
			if progressEvents != wantEvents || reports != wantReports {
				t.Fatal(progressEvents, reports)
			}
		})
	}
}

func TestProducerRelayObservesOnlyAfterSourceIntentAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := 0
	var backend *fakes.Memory
	client, memory, signature := producerFixture(t, func(c *async.ClientConfig) {
		c.Workflows = c.Results.(*fakes.Memory)
		c.Events = eventSinkFunc(func(_ context.Context, _ async.Event) error {
			pending, err := backend.ListIntents(context.Background(), 10)
			if err != nil || len(pending) != 0 {
				t.Error("observer ran before source acknowledgement", len(pending), err)
			}
			observed++
			cancel()
			return errors.New("private sink failure")
		})
	})
	backend = memory
	if _, err := client.ApplyCanvas(ctx, async.Chain(signature)); err != nil {
		t.Fatal(err)
	}
	relay := &async.IntentRelay{Client: client, Sources: []async.IntentStore{backend}, ID: "relay"}
	if err := relay.Tick(ctx); err != nil {
		t.Fatal("observer changed source acknowledgement", err)
	}
	if observed != 1 {
		t.Fatal(observed)
	}
	if err := relay.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if observed != 1 {
		t.Fatal("delivered intent replayed", observed)
	}
}
