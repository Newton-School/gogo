package async

import (
	"context"
	"errors"
	"math/rand/v2"
	"reflect"
	"time"
)

// PublishRetryPolicy bounds synchronous producer recovery, not task execution
// retries. Nil ClientConfig.PublishRetry retains one attempt. Providers and the
// optional classifier must cooperate with context and return promptly.
type PublishRetryPolicy struct {
	// MaxAttempts includes the initial round; zero defaults to three. A round
	// invokes transport and registration, or registration alone after acceptance.
	MaxAttempts int
	// Zero durations default to 100 ms, one second and five seconds respectively.
	InitialDelay, MaxDelay, MaxElapsed time.Duration
	DisableJitter                      bool
	// Retryable is trusted, read-only and receives the provider's error. Nil
	// retries only exact ErrBusy/ErrUnavailable. Transport errors can follow an
	// applied write: opting in permits duplicate delivery, not exactly-once work.
	Retryable func(error) bool
}

func normalizePublishRetry(value *PublishRetryPolicy) (*PublishRetryPolicy, error) {
	if value == nil {
		return nil, nil
	}
	p := *value
	if p.MaxAttempts == 0 {
		p.MaxAttempts = 3
	}
	if p.InitialDelay == 0 {
		p.InitialDelay = 100 * time.Millisecond
	}
	if p.MaxDelay == 0 {
		p.MaxDelay = time.Second
	}
	if p.MaxElapsed == 0 {
		p.MaxElapsed = 5 * time.Second
	}
	if p.MaxAttempts < 1 || p.MaxAttempts > 10 || p.InitialDelay < time.Millisecond || p.InitialDelay > 5*time.Second || p.MaxDelay < p.InitialDelay || p.MaxDelay > 5*time.Second || p.MaxElapsed < time.Millisecond || p.MaxElapsed > 30*time.Second {
		return nil, ErrInvalid
	}
	return &p, nil
}

func publishContextError(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	if ctx == nil {
		return ErrInvalid
	}
	v := reflect.ValueOf(ctx)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return ErrInvalid
		}
	}
	return ctx.Err()
}

type producerOperation struct {
	client    Client
	envelope  Envelope
	state     State
	attempted bool
	accepted  bool
	last      error
}

func copyProducerEnvelope(e Envelope) Envelope {
	copy := cloneJSON(e)
	detachEnvelopeTimes(&copy)
	return copy
}

func (op *producerOperation) failure(cause error) error {
	if !op.attempted {
		return cause
	}
	return &AcceptanceError{ID: op.envelope.ID, Confirmed: op.accepted, Cause: cause,
		envelope: copyProducerEnvelope(op.envelope), state: op.state, accepted: op.accepted}
}

// runProducer is used only by the public producer/replay API. Durable intent
// and outbox runners retain their existing one-shot publish and lease behavior.
func runProducer(ctx context.Context, client *Client, signature *Signature, replay *AcceptanceError) (receipt Receipt, err error) {
	return runProducerStarted(ctx, client, signature, replay, time.Now())
}

func runProducerStarted(ctx context.Context, client *Client, signature *Signature, replay *AcceptanceError, started time.Time) (receipt Receipt, err error) {
	var op producerOperation
	return runProducerOperation(ctx, client, signature, replay, started, &op)
}

// producerAdmission is evidence from this invocation, never from callback errors.
// The operation and its observation are private and are not passed to providers.
type producerAdmission struct {
	id                  string
	attempted, accepted bool
}

func runProducerObserved(ctx context.Context, client *Client, signature *Signature) (Receipt, producerAdmission, error) {
	var op producerOperation
	receipt, err := runProducerOperation(ctx, client, signature, nil, time.Now(), &op)
	return receipt, producerAdmission{id: op.envelope.ID, attempted: op.attempted, accepted: op.accepted}, err
}

func runProducerOperation(ctx context.Context, client *Client, signature *Signature, replay *AcceptanceError, started time.Time, op *producerOperation) (receipt Receipt, err error) {
	complete := false
	contextChecked := false
	var finalContextErr error
	defer func() {
		if recover() != nil {
			complete = false
			receipt, err = Receipt{}, errors.Join(op.last, ErrUnavailable)
		}
		if !complete {
			if !contextChecked {
				finalContextErr = publishContextError(ctx)
			}
			if finalContextErr != nil {
				err = errors.Join(err, finalContextErr)
			}
			if err != nil {
				receipt, err = Receipt{}, op.failure(err)
			}
		}
	}()
	if client == nil || signature == nil && replay == nil {
		return Receipt{}, ErrInvalid
	}
	// No mutex is contained in Client. Freeze its ports/options before context,
	// validation, clock, ID generation, authorization or classifier callbacks.
	op.client = *client
	var input Signature
	if replay != nil {
		copy := *replay
		op.envelope = copyProducerEnvelope(copy.envelope)
		op.state, op.accepted, op.attempted = copy.state, copy.accepted, true
		if op.envelope.Validate() != nil || op.state != Queued && op.state != Scheduled {
			op.attempted = false
			return Receipt{}, ErrInvalid
		}
	} else {
		input = signature.Clone()
		detachSignatureTimes(&input)
	}
	if err := publishContextError(ctx); err != nil {
		return Receipt{}, err
	}
	policy := op.client.config.PublishRetry
	attempts := 1
	if policy != nil {
		attempts = policy.MaxAttempts
		owned, cancel := context.WithDeadline(ctx, started.Add(policy.MaxElapsed))
		ctx = owned
		defer func() {
			// Capture the true completion state before our own cancellation;
			// teardown must not manufacture a canceled acceptance outcome.
			finalContextErr, contextChecked = publishContextError(ctx), true
			cancel()
		}()
	}
	if err := publishContextError(ctx); err != nil {
		return Receipt{}, err
	}
	if replay == nil {
		envelope, prepareErr := op.client.prepare(input)
		if prepareErr != nil {
			return Receipt{}, prepareErr
		}
		op.envelope = copyProducerEnvelope(envelope)
	}
	for round := 0; round < attempts; round++ {
		if err := op.guard(ctx, !op.accepted); err != nil {
			return Receipt{}, err
		}
		if !op.accepted {
			var providerErr error
			if op.state == Scheduled {
				if op.client.config.Schedules == nil {
					return Receipt{}, ErrUnavailable
				}
				envelope := copyProducerEnvelope(op.envelope)
				op.attempted = true
				providerErr = op.client.config.Schedules.Schedule(ctx, envelope)
			} else {
				envelope := copyProducerEnvelope(op.envelope)
				op.attempted = true
				providerErr = op.client.config.Broker.Publish(ctx, envelope)
			}
			// Record confirmed acceptance before another context/policy callback.
			if providerErr == nil {
				op.accepted = true
			}
			op.last = providerErr
			if err := publishContextError(ctx); err != nil {
				return Receipt{}, errors.Join(providerErr, err)
			}
			if providerErr != nil {
				if err := waitProducerRetry(ctx, policy, round, attempts, providerErr); err != nil {
					return Receipt{}, err
				}
				continue
			}
			if err := op.guard(ctx, false); err != nil {
				return Receipt{}, err
			}
		}
		providerErr := op.client.config.Results.Register(ctx, copyProducerEnvelope(op.envelope), op.state)
		op.last = providerErr
		if err := publishContextError(ctx); err != nil {
			return Receipt{}, errors.Join(providerErr, err)
		}
		if providerErr != nil {
			if err := waitProducerRetry(ctx, policy, round, attempts, providerErr); err != nil {
				return Receipt{}, err
			}
			continue
		}
		acceptedAt, err := op.now(ctx)
		if err != nil {
			return Receipt{}, err
		}
		receipt = Receipt{ID: op.envelope.ID, State: op.state, AcceptedAt: acceptedAt}
		complete = true
		// Best-effort observer errors cannot replace confirmed acceptance.
		op.client.observeAccepted(ctx, copyProducerEnvelope(op.envelope), op.state)
		return receipt, nil
	}
	return Receipt{}, ErrUnavailable // Every exhausted round returns its cause.
}

func (op *producerOperation) now(ctx context.Context) (time.Time, error) {
	value := op.client.config.Clock().UTC()
	detachWireTime(&value)
	if err := publishContextError(ctx); err != nil {
		return time.Time{}, err
	}
	if value.IsZero() || value.Year() < 0 || value.Year() > 9999 {
		return time.Time{}, ErrInvalid
	}
	return value, nil
}

func (op *producerOperation) guard(ctx context.Context, checkExpiry bool) error {
	if err := publishContextError(ctx); err != nil {
		return err
	}
	err := op.client.authorize(ctx, "enqueue", op.envelope.Scope, op.envelope.ID)
	if contextErr := publishContextError(ctx); contextErr != nil {
		return errors.Join(err, contextErr)
	}
	if err != nil {
		return err
	}
	if checkExpiry {
		now, err := op.now(ctx)
		if err != nil {
			return err
		}
		if !op.envelope.ExpiresAt.IsZero() && !now.Before(op.envelope.ExpiresAt) {
			return ErrInvalid
		}
		if op.state == "" {
			op.state = Queued
			if op.envelope.ETA.After(now) {
				op.state = Scheduled
			}
		}
	}
	return nil
}

func waitProducerRetry(ctx context.Context, policy *PublishRetryPolicy, round, attempts int, cause error) error {
	if round+1 >= attempts || policy == nil {
		return cause
	}
	// Classifiers cannot override permanent errors, including a contradictory
	// joined error containing one. Error methods/classifiers are trusted code.
	for _, terminal := range []error{ErrInvalid, ErrConflict, ErrDenied, ErrUnknownTask, ErrFrozen, ErrCanceled, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(cause, terminal) {
			return cause
		}
	}
	retry := cause == ErrBusy || cause == ErrUnavailable
	if policy.Retryable != nil {
		retry = policy.Retryable(cause)
	}
	if err := publishContextError(ctx); err != nil {
		return errors.Join(cause, err)
	}
	if !retry {
		return cause
	}
	delay := policy.InitialDelay
	for n := 0; n < round && delay < policy.MaxDelay; n++ {
		delay = min(delay*2, policy.MaxDelay)
	}
	if !policy.DisableJitter {
		delay = time.Duration(float64(delay) * rand.Float64())
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return errors.Join(cause, publishContextError(ctx))
	case <-timer.C:
		return nil
	}
}
