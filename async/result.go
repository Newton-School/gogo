package async

import (
	"context"
	"time"
)

// A handle's exported Receipt is descriptive, not an in-flight command target.
// Capture once before provider/authorization callbacks, including an entire wait.
type resultOperation struct {
	client *Client
	id     string
}

func (r *Result[O]) operation(ctx context.Context) (resultOperation, error) {
	if r == nil || r.client == nil {
		return resultOperation{}, ErrInvalid
	}
	// Client's private configuration is immutable and contains no mutex. Copy
	// its value so callback assignment through the public *Client cannot swap
	// configured provider ports or grants during this operation or wait.
	client := *r.client
	id := r.Receipt.ID
	if !idPattern.MatchString(id) {
		return resultOperation{}, ErrInvalid
	}
	if err := groupContextError(ctx); err != nil {
		return resultOperation{}, err
	}
	return resultOperation{client: &client, id: id}, nil
}

func (op resultOperation) snapshot(ctx context.Context) (out Record, err error) {
	defer func() {
		if recover() != nil {
			out, err = Record{}, ErrUnavailable
		}
		if contextErr := groupContextError(ctx); contextErr != nil {
			out, err = Record{}, contextErr
		}
	}()
	if err := groupContextError(ctx); err != nil {
		return Record{}, err
	}
	record, err := op.client.config.Results.Lookup(ctx, op.id)
	if err != nil {
		return Record{}, resultReadError(err)
	}
	// Validate and detach before context or authorization callbacks: they may
	// retain provider-owned data but cannot rewrite the detached observation.
	record, err = resultRecord(record, op.id)
	if err != nil {
		return Record{}, err
	}
	if err := groupContextError(ctx); err != nil {
		return Record{}, err
	}
	if err := op.client.authorize(ctx, "read", record.Envelope.Scope, op.id); err != nil {
		return Record{}, err
	}
	return record, nil
}

func resultReadError(err error) error {
	switch err {
	case ErrNotFound, ErrResultExpired, ErrDenied, context.Canceled, context.DeadlineExceeded:
		return err
	default:
		return ErrUnavailable
	}
}

// Snapshot returns one detached, authorized observation. Provider failures,
// malformed records and canceled late replies return no partial metadata.
func (r *Result[O]) Snapshot(ctx context.Context) (Record, error) {
	op, err := r.operation(ctx)
	if err != nil {
		return Record{}, err
	}
	return op.snapshot(ctx)
}
func (r *Result[O]) Ready(ctx context.Context) (bool, error) {
	record, err := r.Snapshot(ctx)
	return record.State.Terminal(), err
}
func (r *Result[O]) Successful(ctx context.Context) (bool, error) {
	record, err := r.Snapshot(ctx)
	return record.State == Succeeded, err
}
func (r *Result[O]) Failed(ctx context.Context) (bool, error) {
	record, err := r.Snapshot(ctx)
	return record.State == Failed, err
}

func (r *Result[O]) Get(ctx context.Context) (out O, err error) {
	defer func() {
		if recover() != nil {
			var zero O
			out, err = zero, ErrUnavailable
		}
		if contextErr := groupContextError(ctx); contextErr != nil {
			var zero O
			out, err = zero, contextErr
		}
	}()
	op, err := r.operation(ctx)
	if err != nil {
		return out, err
	}
	if ctx.Value(workerContextKey{}) != nil {
		return out, ErrWorkerJoin
	}
	timer := time.NewTicker(20 * time.Millisecond)
	defer timer.Stop()
	for {
		record, err := op.snapshot(ctx)
		if err != nil {
			return out, err
		}
		if record.State.Terminal() {
			if record.Failure != nil {
				return out, *record.Failure
			}
			if record.State != Succeeded {
				return out, Failure{Code: string(record.State), Message: "Task did not succeed"}
			}
			if len(record.Output) == 0 {
				return out, ErrResultExpired
			}
			if err := decodeJSON(record.Output, &out); err != nil {
				var zero O
				return zero, ErrUnavailable
			}
			return out, nil
		}
		select {
		case <-ctx.Done():
			if err := groupContextError(ctx); err != nil {
				return out, err
			}
			// A closed Done with no cancellation error violates Context's
			// contract; it is not successful completion of a nonterminal task.
			return out, ErrUnavailable
		case <-timer.C:
		}
	}
}
func (r *Result[O]) Wait(ctx context.Context) (O, error) { return r.Get(ctx) }

func (r *Result[O]) command(ctx context.Context, action string) (err error) {
	defer func() {
		if recover() != nil {
			// A provider may panic after its write. This is unavailable, not
			// confirmation that the task or its retained payload is unchanged.
			err = ErrUnavailable
		}
	}()
	op, err := r.operation(ctx)
	if err != nil {
		return err
	}
	record, err := op.snapshot(ctx)
	if err != nil {
		return err
	}
	if err := op.client.authorize(ctx, action, record.Envelope.Scope, op.id); err != nil {
		return err
	}
	if err := groupContextError(ctx); err != nil {
		return err
	}
	if action == "forget" {
		err = op.client.config.Results.Forget(ctx, op.id)
	} else {
		err = op.client.config.Results.RequestCancel(ctx, op.id, record.Envelope.Scope)
	}
	// Do not replace a confirmed reply with a subsequently canceled context.
	// Wrapped/joined errors cannot prove a no-write sentinel's meaning.
	switch err {
	case nil, ErrNotFound, ErrPinned, ErrDenied, ErrCanceled, ErrBusy, ErrConflict, ErrLeaseLost, context.Canceled, context.DeadlineExceeded:
		return err
	default:
		return ErrUnavailable
	}
}

// Forget preserves coordination pins and tombstones. A provider error after
// invocation may follow an applied deletion; it is not proof of no change.
func (r *Result[O]) Forget(ctx context.Context) error { return r.command(ctx, "forget") }

// Revoke requests durable cooperative cancellation, not proof of execution
// stopping. A failed provider reply can follow a successfully recorded request.
func (r *Result[O]) Revoke(ctx context.Context) error { return r.command(ctx, "revoke") }
