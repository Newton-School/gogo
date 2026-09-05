package async

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"runtime"
	"sync"
	"time"
)

type Worker struct {
	Registry    *Registry
	Broker      Broker
	Results     ResultStore
	ID          string
	Queues      []string
	Concurrency int
	Lease       time.Duration
	Heartbeat   time.Duration
	Clock       func() time.Time
	Jitter      func() float64
	OnError     func(error)
	Executor    Executor
	Events      EventSink
	initOnce    sync.Once
	initErr     error
	activityMu  sync.Mutex
	active      map[string]TaskActivity
}

func (w *Worker) initialize() error {
	w.initOnce.Do(func() { w.initErr = w.defaults() })
	return w.initErr
}

func (w *Worker) defaults() error {
	if w.Registry == nil || w.Broker == nil || w.Results == nil {
		return ErrInvalid
	}
	if w.ID == "" {
		id, err := NewID()
		if err != nil {
			return err
		}
		w.ID = id
	}
	if w.Concurrency == 0 {
		w.Concurrency = min(32, max(1, runtime.NumCPU()))
	}
	if w.Concurrency < 1 || w.Concurrency > 1024 {
		return ErrInvalid
	}
	if w.Lease == 0 {
		w.Lease = 60 * time.Second
	}
	if w.Heartbeat == 0 {
		w.Heartbeat = 20 * time.Second
	}
	if w.Heartbeat <= 0 || w.Lease <= w.Heartbeat {
		return ErrInvalid
	}
	if w.Clock == nil {
		w.Clock = time.Now
	}
	if w.Jitter == nil {
		w.Jitter = rand.Float64
	}
	if len(w.Queues) == 0 {
		w.Queues = []string{"default"}
	}
	w.Registry.mu.RLock()
	for _, definition := range w.Registry.definitions {
		if definition.options.HardLimit > 0 && w.Executor == nil {
			w.Registry.mu.RUnlock()
			return ErrInvalid
		}
	}
	w.Registry.mu.RUnlock()
	return w.Registry.Freeze(true)
}

func (w *Worker) report(err error) {
	if err != nil && w.OnError != nil {
		w.OnError(err)
	}
}

func (w *Worker) Run(ctx context.Context) error {
	if err := w.initialize(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for slot := 0; slot < w.Concurrency; slot++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lastReclaim := w.Clock()
			for ctx.Err() == nil {
				if w.Clock().Sub(lastReclaim) >= w.Lease {
					lastReclaim = w.Clock()
					deliveries, err := w.Broker.Reclaim(ctx, ConsumeOptions{Queues: w.Queues, Consumer: w.ID}, w.Lease)
					if err != nil {
						w.report(err)
					} else {
						for _, delivery := range deliveries {
							if ctx.Err() != nil {
								break
							}
							w.report(w.Process(ctx, delivery))
						}
					}
				}
				d, err := w.Broker.Consume(ctx, ConsumeOptions{Queues: w.Queues, Consumer: w.ID, Wait: time.Second})
				if err != nil {
					if !errors.Is(err, ErrNotFound) && ctx.Err() == nil {
						w.report(err)
						select {
						case <-ctx.Done():
						case <-time.After(100 * time.Millisecond):
						}
					}
					continue
				}
				w.report(w.Process(ctx, d))
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}

// Process handles a single reservation. Any uncertain result-store outcome leaves
// the delivery pending; an old worker can never acknowledge uncommitted success.
func (w *Worker) Process(ctx context.Context, delivery Delivery) error {
	if err := w.initialize(); err != nil {
		return err
	}
	e, err := DecodeEnvelope(delivery.Body)
	if err != nil {
		return w.Broker.Reject(ctx, delivery, "invalid_envelope", false)
	}
	d, err := w.Registry.lookup(e.Task, e.Version)
	if err != nil || d.call == nil {
		return w.Broker.Reject(ctx, delivery, "unknown_task", false)
	}
	if err := d.validate(e.Args); err != nil {
		return w.Broker.Reject(ctx, delivery, "invalid_arguments", false)
	}
	if err := w.Registry.validateLinks(Signature{Task: e.Task, Version: e.Version, Args: e.Args, Callbacks: e.Callbacks, Errbacks: e.Errbacks}, 0); err != nil {
		return w.Broker.Reject(ctx, delivery, "invalid_arguments", false)
	}
	claim, err := w.Results.Claim(ctx, e, w.ID, w.Lease)
	if errors.Is(err, ErrConflict) {
		return w.Broker.Reject(ctx, delivery, "task_identity_conflict", false)
	}
	if err != nil {
		return err
	}
	if claim.Duplicate {
		return w.Broker.Ack(ctx, delivery)
	}
	if !claim.Acquired {
		return nil
	}
	record := claim.Record
	w.activity(e)
	defer w.inactive(e.ID)
	w.event(ctx, e, "task_started", Running)
	tc := TaskContext{ID: e.ID, Retries: e.Retries, DeliveryCount: record.DeliveryCount, Fence: record.Fence, WorkerID: w.ID, WorkflowID: e.WorkflowID, ParentID: e.ParentID, RootID: e.RootID, Scope: e.Scope, Principal: e.Principal, Headers: cloneJSON(e.Headers), Stamps: cloneJSON(e.Stamps)}
	tc.progress = func(ctx context.Context, b json.RawMessage) error {
		return w.Results.RecordProgress(ctx, e.ID, tc.Fence, w.ID, b)
	}
	state := Succeeded
	var output json.RawMessage
	var runErr error
	if record.CancelRequested {
		state = Revoked
		runErr = context.Canceled
	} else if !e.ExpiresAt.IsZero() && !w.Clock().Before(e.ExpiresAt) {
		state = Expired
		runErr = context.DeadlineExceeded
	} else {
		runCtx, cancel := context.WithCancel(context.WithValue(ctx, workerContextKey{}, true))
		defer cancel()
		if d.options.SoftLimit > 0 && w.Executor == nil {
			var deadlineCancel context.CancelFunc
			runCtx, deadlineCancel = context.WithTimeout(runCtx, d.options.SoftLimit)
			defer deadlineCancel()
		}
		var heartbeatWG sync.WaitGroup
		heartbeatWG.Add(1)
		stopHeartbeat := make(chan struct{})
		leaseError := make(chan error, 1)
		go func() {
			defer heartbeatWG.Done()
			ticker := time.NewTicker(w.Heartbeat)
			defer ticker.Stop()
			for {
				select {
				case <-stopHeartbeat:
					return
				case <-runCtx.Done():
					return
				case <-ticker.C:
					current, err := w.Results.Renew(runCtx, e.ID, tc.Fence, w.ID, w.Lease)
					if err != nil {
						leaseError <- err
						cancel()
						return
					}
					if current.CancelRequested {
						cancel()
						return
					}
				}
			}
		}()
		output, runErr = w.invoke(runCtx, d, tc, e.Args)
		if runCtx.Err() != nil && runErr == nil {
			runErr = runCtx.Err()
		}
		close(stopHeartbeat)
		heartbeatWG.Wait()
		select {
		case err := <-leaseError:
			return err
		default:
		}
		current, err := w.Results.Lookup(ctx, e.ID)
		if err != nil {
			return err
		}
		if current.CancelRequested {
			state = Revoked
			runErr = context.Canceled
		}
	}
	transition := Transition{ID: e.ID, Fence: tc.Fence, Owner: w.ID, State: state, Output: output}
	if runErr != nil {
		if state == Succeeded {
			state = Failed
		}
		if state == Failed {
			if eta, ok := d.options.Retry.Next(runErr, e.Retries, w.Clock(), e.ExpiresAt, w.Jitter()); ok {
				next := cloneJSON(e)
				next.Retries++
				next.ETA = eta
				transition.State = RetryWait
				transition.Next = &next
				transition.Output = nil
				transition.Intents = []Intent{{ID: StableID(e.ID, "retry-"+time.Duration(next.Retries).String()), SourceID: e.ID, Kind: "publish", Envelope: &next}}
				if err := w.Results.Transition(ctx, transition); err != nil {
					return err
				}
				w.event(ctx, e, "task_retry", RetryWait)
				if d.options.Hooks.OnRetry != nil {
					w.observe(func() error { return d.options.Hooks.OnRetry(ctx, tc, runErr) })
				}
				if d.options.Hooks.AfterReturn != nil {
					w.observe(func() error { return d.options.Hooks.AfterReturn(ctx, tc, RetryWait) })
				}
				return w.Broker.Ack(ctx, delivery)
			}
		}
		transition.State = state
		transition.Output = nil
		transition.Failure = &Failure{Code: string(state), Message: "Task execution did not complete successfully"}
	}
	transition.Intents = CompletionIntents(e, transition.State, transition.Output, transition.Failure)
	linked, err := w.linkedIntents(e, transition)
	if err != nil {
		return err
	}
	transition.Intents = append(transition.Intents, linked...)
	if err := w.Results.Transition(ctx, transition); err != nil {
		return err
	}
	w.event(ctx, e, "task_terminal", transition.State)
	if runErr == nil && d.options.Hooks.OnSuccess != nil {
		w.observe(func() error { return d.options.Hooks.OnSuccess(ctx, tc, output) })
	}
	if runErr != nil && d.options.Hooks.OnFailure != nil {
		w.observe(func() error { return d.options.Hooks.OnFailure(ctx, tc, runErr) })
	}
	if d.options.Hooks.AfterReturn != nil {
		w.observe(func() error { return d.options.Hooks.AfterReturn(ctx, tc, transition.State) })
	}
	return w.Broker.Ack(ctx, delivery)
}

func (w *Worker) invoke(ctx context.Context, d *definition, tc TaskContext, args json.RawMessage) (output json.RawMessage, err error) {
	defer func() {
		if recover() != nil {
			err = Failure{Code: "PANIC", Message: "Task handler panicked"}
		}
	}()
	if d.options.HardLimit > 0 && w.Executor == nil {
		return nil, Failure{Code: "EXECUTOR_REQUIRED", Message: "A subprocess executor is required for a hard limit"}
	}
	if d.options.Authorize != nil {
		if err := d.options.Authorize(ctx, tc); err != nil {
			return nil, ErrDenied
		}
	}
	if d.options.Hooks.BeforeStart != nil {
		if err := d.options.Hooks.BeforeStart(ctx, tc); err != nil {
			return nil, err
		}
	}
	if w.Executor != nil {
		if d.options.HardLimit > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d.options.HardLimit)
			defer cancel()
		}
		e := Envelope{ProtocolVersion: ProtocolVersion, ID: tc.ID, Task: d.name, Version: d.version, Args: args, CreatedAt: w.Clock().UTC(), Queue: d.options.Queue, Retries: tc.Retries, MaxRetries: d.options.Retry.MaxRetries, Scope: tc.Scope, Principal: tc.Principal, WorkflowID: tc.WorkflowID, ParentID: tc.ParentID, RootID: tc.RootID, Headers: tc.Headers, Stamps: tc.Stamps}
		output, err := w.Executor.Execute(ctx, Execution{Envelope: e, TaskContext: tc})
		if err != nil {
			return nil, err
		}
		if err := d.validateOutput(output); err != nil {
			return nil, ErrInvalid
		}
		return output, nil
	}
	return d.call(ctx, tc, args)
}

func (w *Worker) Reclaim(ctx context.Context) error {
	if err := w.initialize(); err != nil {
		return err
	}
	deliveries, err := w.Broker.Reclaim(ctx, ConsumeOptions{Queues: w.Queues, Consumer: w.ID}, w.Lease)
	if err != nil {
		return err
	}
	for _, d := range deliveries {
		if err := w.Process(ctx, d); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) observe(callback func() error) {
	defer func() {
		if recover() != nil {
			w.report(Failure{Code: "OBSERVER_PANIC", Message: "Task observer failed after durable state transition"})
		}
	}()
	w.report(callback())
}
