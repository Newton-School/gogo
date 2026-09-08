package async_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

type producerBroker struct {
	async.Broker
	call func(context.Context, async.Envelope) error
}

func (b producerBroker) Publish(ctx context.Context, e async.Envelope) error { return b.call(ctx, e) }

type producerResults struct {
	async.ResultStore
	call func(context.Context, async.Envelope, async.State) error
}

func (r producerResults) Register(ctx context.Context, e async.Envelope, s async.State) error {
	return r.call(ctx, e, s)
}

type producerSchedule struct {
	async.ScheduleStore
	call func(context.Context, async.Envelope) error
}

func (s producerSchedule) Schedule(ctx context.Context, e async.Envelope) error {
	return s.call(ctx, e)
}

type producerLookupResults struct {
	async.ResultStore
	call func(context.Context, string) (async.Record, error)
}

func (r producerLookupResults) Lookup(ctx context.Context, id string) (async.Record, error) {
	return r.call(ctx, id)
}

func producerRetryFixture(t *testing.T) (async.ClientConfig, *async.Task[int, int], *fakes.Memory) {
	t.Helper()
	registry := async.NewRegistry()
	task, err := async.Register(registry, "producer.test", 1, func(_ context.Context, tc async.TaskContext, value int) (int, error) {
		if tc.Retries != 0 {
			t.Error("publication consumed a task retry")
		}
		return value + 1, nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memory := fakes.NewMemory()
	return async.ClientConfig{Registry: registry, Broker: memory, Results: memory, Schedules: memory,
		PublishRetry: &async.PublishRetryPolicy{InitialDelay: time.Millisecond, MaxDelay: time.Millisecond, DisableJitter: true}}, task, memory
}

func producerClient(t *testing.T, config async.ClientConfig) *async.Client {
	t.Helper()
	c, err := async.NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func producerAcceptance(t *testing.T, err error, confirmed bool) *async.AcceptanceError {
	t.Helper()
	var value *async.AcceptanceError
	if !errors.As(err, &value) || value.ID == "" || value.Confirmed != confirmed || strings.Contains(err.Error(), "private") {
		t.Fatalf("incorrect acceptance outcome: %#v / %v", value, err)
	}
	return value
}

func TestProducerRetryConfiguration(t *testing.T) {
	for _, mutate := range []func(*async.PublishRetryPolicy){
		func(p *async.PublishRetryPolicy) { p.MaxAttempts = -1 },
		func(p *async.PublishRetryPolicy) { p.MaxAttempts = 11 },
		func(p *async.PublishRetryPolicy) { p.InitialDelay = -1 },
		func(p *async.PublishRetryPolicy) { p.InitialDelay = time.Microsecond },
		func(p *async.PublishRetryPolicy) { p.InitialDelay = 6 * time.Second },
		func(p *async.PublishRetryPolicy) { p.MaxDelay = time.Microsecond },
		func(p *async.PublishRetryPolicy) { p.MaxDelay = 6 * time.Second },
		func(p *async.PublishRetryPolicy) { p.MaxElapsed = time.Microsecond },
		func(p *async.PublishRetryPolicy) { p.MaxElapsed = 31 * time.Second },
	} {
		cfg, _, _ := producerRetryFixture(t)
		mutate(cfg.PublishRetry)
		if c, err := async.NewClient(cfg); c != nil || err != async.ErrInvalid {
			t.Fatal(c, err)
		}
	}
	for _, policy := range []*async.PublishRetryPolicy{nil, {}, {MaxAttempts: 1}, {MaxAttempts: 10, InitialDelay: 5 * time.Second, MaxDelay: 5 * time.Second, MaxElapsed: 30 * time.Second}} {
		cfg, _, _ := producerRetryFixture(t)
		cfg.PublishRetry = policy
		producerClient(t, cfg)
	}
}

func TestProducerRetryExactEnvelopeAndDefaultSingleAttempt(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "retry"}[enabled], func(t *testing.T) {
			cfg, task, memory := producerRetryFixture(t)
			if !enabled {
				cfg.PublishRetry = nil
			}
			calls, ids, validations := 0, 0, 0
			cfg.NewID = func() (string, error) { ids++; return async.NewID() }
			var first []byte
			cfg.Broker = producerBroker{Broker: memory, call: func(ctx context.Context, e async.Envelope) error {
				calls++
				raw, _ := json.Marshal(e)
				if calls == 1 {
					first = raw
				} else if string(raw) != string(first) {
					t.Fatal("retry changed envelope", string(raw), string(first))
				}
				if e.Retries != 0 {
					t.Fatal("execution retry consumed")
				}
				// Provider-owned arguments must not become the next attempt's data.
				if calls < 3 {
					e.Args[0] = '9'
					*e.MaxRetries = 99
					return async.ErrUnavailable
				}
				validations++
				return memory.Publish(ctx, e)
			}}
			c := producerClient(t, cfg)
			if enabled {
				cfg.PublishRetry.MaxAttempts = 1 // Constructor snapshot.
			}
			result, err := task.Delay(context.Background(), c, 4)
			if !enabled {
				producerAcceptance(t, err, false)
				if calls != 1 || ids != 1 || result != nil {
					t.Fatal(calls, ids, result)
				}
				return
			}
			if err != nil || calls != 3 || ids != 1 || validations != 1 {
				t.Fatal(err, calls, ids, validations)
			}
			worker := &async.Worker{Registry: cfg.Registry, Broker: memory, Results: memory, ID: "producer-worker"}
			if err := worker.Process(context.Background(), take(t, memory)); err != nil {
				t.Fatal(err)
			}
			if value, err := result.Get(context.Background()); err != nil || value != 5 {
				t.Fatal(value, err)
			}
		})
	}
}

func TestProducerRetryConfirmedRegistrationAndRetainedReplay(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "manual"}[replay], func(t *testing.T) {
			cfg, task, memory := producerRetryFixture(t)
			if replay {
				cfg.PublishRetry = nil
			}
			publishes, registers, fail := 0, 0, true
			cfg.Broker = producerBroker{Broker: memory, call: func(ctx context.Context, e async.Envelope) error {
				publishes++
				return memory.Publish(ctx, e)
			}}
			cfg.Results = producerResults{ResultStore: memory, call: func(ctx context.Context, e async.Envelope, state async.State) error {
				registers++
				if fail {
					if !replay {
						fail = false
					}
					return async.ErrUnavailable
				}
				return memory.Register(ctx, e, state)
			}}
			c := producerClient(t, cfg)
			_, err := task.Delay(context.Background(), c, 1)
			if replay {
				acceptance := producerAcceptance(t, err, true)
				id := acceptance.ID
				acceptance.ID, acceptance.Confirmed = "modified-description", false
				fail = false
				receipt, retryErr := acceptance.Retry(context.Background(), c)
				if retryErr != nil || receipt.ID != id {
					t.Fatal(receipt, retryErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if publishes != 1 || registers != 2 {
				t.Fatal("confirmed transport was repeated", publishes, registers)
			}
		})
	}
}

func TestProducerRetryScheduledIdentityAcrossETAAndExpiry(t *testing.T) {
	for _, mode := range []string{"eta", "expired unknown", "expired confirmed"} {
		t.Run(mode, func(t *testing.T) {
			cfg, task, memory := producerRetryFixture(t)
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			initial := now
			cfg.Clock = func() time.Time { return now }
			calls, registers := 0, 0
			cfg.Broker = producerBroker{Broker: memory, call: func(context.Context, async.Envelope) error {
				t.Fatal("retry crossed from schedule to broker")
				return nil
			}}
			cfg.Schedules = producerSchedule{ScheduleStore: memory, call: func(ctx context.Context, e async.Envelope) error {
				calls++
				if !e.CreatedAt.Equal(initial) || !e.ETA.Equal(initial.Add(time.Second)) {
					t.Fatal("countdown was recomputed", e)
				}
				if mode == "expired confirmed" {
					return memory.Schedule(ctx, e)
				}
				now = initial.Add(2 * time.Second)
				if mode == "expired unknown" {
					now = initial.Add(4 * time.Second)
				}
				if calls == 1 {
					return async.ErrUnavailable
				}
				return memory.Schedule(ctx, e)
			}}
			cfg.Results = producerResults{ResultStore: memory, call: func(ctx context.Context, e async.Envelope, state async.State) error {
				registers++
				if mode == "expired confirmed" && registers == 1 {
					now = initial.Add(4 * time.Second)
					return async.ErrUnavailable
				}
				return memory.Register(ctx, e, state)
			}}
			c := producerClient(t, cfg)
			_, err := task.Delay(context.Background(), c, 1, async.WithCountdown(time.Second), async.WithExpiry(initial.Add(3*time.Second)))
			if mode == "expired unknown" {
				producerAcceptance(t, err, false)
				if !errors.Is(err, async.ErrInvalid) || calls != 1 || registers != 0 {
					t.Fatal(err, calls, registers)
				}
			} else if err != nil || mode == "eta" && (calls != 2 || registers != 1) || mode == "expired confirmed" && (calls != 1 || registers != 2) {
				t.Fatal(err, calls, registers)
			}
		})
	}
}

func TestProducerRetryClassificationAndCancellation(t *testing.T) {
	for _, mode := range []string{"default transport", "custom transport", "permanent joined", "classifier panic", "classifier cancel", "provider panic", "provider cancel", "confirmed cancel", "auth revoked", "exhausted", "elapsed"} {
		t.Run(mode, func(t *testing.T) {
			cfg, task, memory := producerRetryFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, grants, classified := 0, 0, 0
			cfg.Authorize = func(context.Context, string, string, string) error {
				grants++
				if mode == "auth revoked" && grants > 1 {
					return async.ErrDenied
				}
				return nil
			}
			if mode != "default transport" {
				cfg.PublishRetry.Retryable = func(error) bool {
					classified++
					if mode == "classifier panic" {
						panic("private classifier")
					}
					if mode == "classifier cancel" {
						cancel()
					}
					return true
				}
			}
			if mode == "elapsed" {
				cfg.PublishRetry.MaxElapsed = 10 * time.Millisecond
				cfg.PublishRetry.InitialDelay, cfg.PublishRetry.MaxDelay = time.Second, time.Second
			}
			cfg.Broker = producerBroker{Broker: memory, call: func(ctx context.Context, e async.Envelope) error {
				calls++
				if mode == "provider panic" {
					panic("private provider")
				}
				if mode == "provider cancel" || mode == "confirmed cancel" {
					cancel()
					if mode == "confirmed cancel" {
						return nil
					}
				}
				if mode == "permanent joined" {
					return errors.Join(async.ErrConflict, io.EOF)
				}
				if mode == "custom transport" && calls == 2 {
					return memory.Publish(ctx, e)
				}
				return io.EOF
			}}
			c := producerClient(t, cfg)
			_, err := task.Delay(ctx, c, 1)
			if mode == "custom transport" {
				if err != nil || calls != 2 {
					t.Fatal(err, calls)
				}
				return
			}
			producerAcceptance(t, err, mode == "confirmed cancel")
			wantCalls := 1
			if mode == "exhausted" {
				wantCalls = 3
			}
			if calls != wantCalls || mode == "permanent joined" && classified != 0 {
				t.Fatal(err, calls, classified)
			}
			if strings.Contains(mode, "cancel") && !errors.Is(err, context.Canceled) || mode == "elapsed" && !errors.Is(err, context.DeadlineExceeded) || mode == "auth revoked" && !errors.Is(err, async.ErrDenied) {
				t.Fatal("lost terminal cause", err)
			}
		})
	}
}

type producerCallbackContext struct {
	context.Context
	callback func()
}

func (c *producerCallbackContext) Err() error {
	if callback := c.callback; callback != nil {
		c.callback = nil
		callback()
	}
	return c.Context.Err()
}

func TestProducerRetryClientAndEnvelopeOwnership(t *testing.T) {
	for _, point := range []string{"context", "clock", "authorize", "broker", "classifier", "results"} {
		t.Run(point, func(t *testing.T) {
			cfg, task, memory := producerRetryFixture(t)
			other, _, _ := producerRetryFixture(t)
			other.Broker = producerBroker{call: func(context.Context, async.Envelope) error { t.Fatal("replacement broker"); return nil }}
			replacementReads := 0
			other.Results = producerLookupResults{ResultStore: memory, call: func(context.Context, string) (async.Record, error) {
				replacementReads++
				return async.Record{}, async.ErrUnavailable
			}}
			replacement := producerClient(t, other)
			var client *async.Client
			replace := func() { *client = *replacement }
			calls, registers, mutations := 0, 0, 0
			mutate := func(name string) {
				if name == point && mutations == 0 {
					mutations++
					replace()
				}
			}
			cfg.Clock = func() time.Time { mutate("clock"); return time.Now() }
			cfg.Authorize = func(context.Context, string, string, string) error { mutate("authorize"); return nil }
			cfg.PublishRetry.Retryable = func(error) bool { mutate("classifier"); return true }
			cfg.Broker = producerBroker{Broker: memory, call: func(ctx context.Context, e async.Envelope) error {
				calls++
				mutate("broker")
				if calls == 1 {
					return async.ErrUnavailable
				}
				return memory.Publish(ctx, e)
			}}
			cfg.Results = producerResults{ResultStore: memory, call: func(ctx context.Context, e async.Envelope, state async.State) error {
				registers++
				mutate("results")
				return memory.Register(ctx, e, state)
			}}
			client = producerClient(t, cfg)
			ctx := &producerCallbackContext{Context: context.Background(), callback: func() { mutate("context") }}
			result, err := task.Delay(ctx, client, 1)
			if err != nil || calls != 2 || registers != 1 || mutations != 1 {
				t.Fatal(err, calls, registers, mutations)
			}
			if record, err := result.Snapshot(context.Background()); err != nil || record.Envelope.ID != result.Receipt.ID || replacementReads != 0 {
				t.Fatal("returned result lost producer client", record.Envelope.ID, err, replacementReads)
			}
			worker := &async.Worker{Registry: cfg.Registry, Broker: memory, Results: memory, ID: "producer-owner"}
			if err := worker.Process(context.Background(), take(t, memory)); err != nil {
				t.Fatal(err)
			}
			if value, err := result.Get(context.Background()); err != nil || value != 2 || replacementReads != 0 {
				t.Fatal("returned Get lost producer client", value, err, replacementReads)
			}
		})
	}
}

type producerArgument struct {
	Value  int
	mutate func()
}

func (v producerArgument) MarshalJSON() ([]byte, error) {
	if v.mutate != nil {
		v.mutate()
	}
	return []byte(`{"Value":1}`), nil
}

func TestProducerRetryDelayCapturesBeforeArgumentAndOptionCallbacks(t *testing.T) {
	cfg, _, memory := producerRetryFixture(t)
	task, err := async.Register(cfg.Registry, "producer.original", 1, func(context.Context, async.TaskContext, producerArgument) (int, error) { return 1, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	otherTask, err := async.Register(cfg.Registry, "producer.replacement", 1, func(context.Context, async.TaskContext, producerArgument) (int, error) { return 2, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var accepted async.Envelope
	cfg.Broker = producerBroker{Broker: memory, call: func(ctx context.Context, envelope async.Envelope) error {
		accepted = envelope
		return memory.Publish(ctx, envelope)
	}}
	client := producerClient(t, cfg)
	other, _, _ := producerRetryFixture(t)
	other.Broker = producerBroker{call: func(context.Context, async.Envelope) error { t.Fatal("replaced producer client"); return nil }}
	replacement := producerClient(t, other)
	options := []async.DispatchOption{func(*async.DispatchOptions) {}, async.WithPriority(3)}
	argument := producerArgument{Value: 1, mutate: func() {
		*client, *task = *replacement, *otherTask
		options[1] = async.WithPriority(9)
	}}
	result, err := task.Delay(context.Background(), client, argument, options...)
	if err != nil || accepted.Task != "producer.original" || accepted.Priority != 3 {
		t.Fatal(err, accepted.Task, accepted.Priority)
	}
	if record, err := result.Snapshot(context.Background()); err != nil || record.Envelope.Task != "producer.original" {
		t.Fatal(record.Envelope.Task, err)
	}
}

func TestProducerRetryDelayPreparationConsumesElapsedBudget(t *testing.T) {
	cfg, task, memory := producerRetryFixture(t)
	cfg.PublishRetry.MaxElapsed = time.Millisecond
	calls := 0
	cfg.Broker = producerBroker{Broker: memory, call: func(context.Context, async.Envelope) error { calls++; return nil }}
	client := producerClient(t, cfg)
	_, err := task.Delay(context.Background(), client, 1, func(*async.DispatchOptions) { time.Sleep(5 * time.Millisecond) })
	if !errors.Is(err, context.DeadlineExceeded) || calls != 0 {
		t.Fatal("typed preparation reset the producer deadline", err, calls)
	}
}

func TestProducerRetryConfirmedFailureCanRepairWithoutAnotherPublish(t *testing.T) {
	for _, mode := range []string{"register panic", "clock panic", "classifier panic", "post-acceptance denial"} {
		t.Run(mode, func(t *testing.T) {
			cfg, task, memory := producerRetryFixture(t)
			publishes, registers, grants := 0, 0, 0
			broken := true
			cfg.Clock = func() time.Time {
				if broken && mode == "clock panic" && registers != 0 {
					panic("private clock")
				}
				return time.Now()
			}
			cfg.Authorize = func(context.Context, string, string, string) error {
				grants++
				if broken && mode == "post-acceptance denial" && grants > 1 {
					return async.ErrDenied
				}
				return nil
			}
			cfg.PublishRetry.Retryable = func(error) bool { panic("private classifier") }
			cfg.Broker = producerBroker{Broker: memory, call: func(ctx context.Context, e async.Envelope) error {
				publishes++
				return memory.Publish(ctx, e)
			}}
			cfg.Results = producerResults{ResultStore: memory, call: func(ctx context.Context, e async.Envelope, s async.State) error {
				registers++
				if broken && mode == "register panic" {
					panic("private registration")
				}
				if broken && mode == "classifier panic" {
					return async.ErrUnavailable
				}
				return memory.Register(ctx, e, s)
			}}
			client := producerClient(t, cfg)
			_, err := task.Delay(context.Background(), client, 1)
			acceptance := producerAcceptance(t, err, true)
			broken = false
			receipt, err := acceptance.Retry(context.Background(), client)
			if err != nil || publishes != 1 || receipt.ID != acceptance.ID {
				t.Fatal(receipt, err, publishes, registers, grants)
			}
		})
	}
}

func TestProducerRetryInvalidContextsAndIndependentCalls(t *testing.T) {
	cfg, task, memory := producerRetryFixture(t)
	var calls atomic.Int32
	cfg.Broker = producerBroker{Broker: memory, call: func(ctx context.Context, e async.Envelope) error {
		calls.Add(1)
		return memory.Publish(ctx, e)
	}}
	c := producerClient(t, cfg)
	var typedNil *producerCallbackContext
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, typedNil, canceled, &producerCallbackContext{Context: context.Background(), callback: func() { panic("private context") }}} {
		if _, err := task.Delay(ctx, c, 1); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal(err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal(calls.Load())
	}
	var group sync.WaitGroup
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := task.Delay(context.Background(), c, 1)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- result.Receipt.ID
		}()
	}
	group.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatal("shared producer identity")
		}
		seen[id] = true
	}
	if len(seen) != 12 || calls.Load() != 12 {
		t.Fatal(len(seen), calls.Load())
	}
}

func TestProducerRetryProviderMetadataCannotRewriteNextAttempt(t *testing.T) {
	cfg, task, memory := producerRetryFixture(t)
	cfg.AllowedHeaders = []string{"trace"}
	var retained async.Envelope
	calls := 0
	var first []byte
	cfg.Broker = producerBroker{Broker: memory, call: func(ctx context.Context, e async.Envelope) error {
		calls++
		raw, _ := json.Marshal(e)
		if calls == 1 {
			first, retained = raw, e
			return async.ErrUnavailable
		}
		if !reflect.DeepEqual(first, raw) {
			t.Fatal("retained provider view changed next attempt")
		}
		return memory.Publish(ctx, e)
	}}
	cfg.PublishRetry.Retryable = func(error) bool {
		retained.Headers["trace"] = "changed"
		retained.Stamps["batch"] = "changed"
		*retained.CreatedAt.Location() = *time.FixedZone("changed", 3600)
		*retained.ETA.Location() = *time.FixedZone("changed", 3600)
		retained.Args[0] = '9'
		return true
	}
	c := producerClient(t, cfg)
	if _, err := task.Delay(context.Background(), c, 1, async.WithHeaders(map[string]string{"trace": "original"}), async.WithStamps(map[string]string{"batch": "original"})); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
}
