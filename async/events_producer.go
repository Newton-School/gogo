package async

import (
	"context"
	"time"
)

// Observation is synchronous and context-bounded, without a detached goroutine.
// Trusted sinks/providers must honor context; a blocking observer that ignores
// cancellation cannot be forcibly terminated by the framework.
func observeEvent(ctx context.Context, timeout time.Duration, report func(error), observe func(context.Context) error) {
	failed := false
	func() {
		defer func() {
			if recover() != nil {
				failed = true
			}
		}()
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if ctx.Err() != nil {
			failed = true
			return
		}
		failed = observe(ctx) != nil || ctx.Err() != nil
	}()
	if failed && report != nil {
		// Never expose provider errors/panic values or recursively call a
		// failing observer. Acceptance and durable task state are unchanged.
		defer func() { _ = recover() }()
		report(Failure{Code: "EVENT_OBSERVER_FAILED", Message: "Task event observation failed"})
	}
}

func (c *Client) observeAccepted(ctx context.Context, envelope Envelope, state State) {
	if c.config.Events == nil {
		return
	}
	observeEvent(ctx, c.config.EventTimeout, c.config.OnEventError, func(ctx context.Context) error {
		record, err := c.config.Results.Lookup(ctx, envelope.ID)
		if err != nil {
			return err
		}
		digest := envelope.Digest()
		if record.Envelope.Validate() != nil || record.Envelope.Digest() != digest || record.Digest != digest || record.Envelope.ID != envelope.ID {
			return ErrUnavailable
		}
		// Replaying an already-running/finished task must not announce a new
		// execution. Unclaimed acceptance observations can still duplicate;
		// consumers must not interpret their count as an execution count.
		switch record.State {
		case Queued, Scheduled, RetryWait:
		case Running, Succeeded, Failed, Revoked, Expired:
			return nil
		default:
			return ErrUnavailable
		}
		if record.CancelRequested || record.Envelope.Retries != envelope.Retries {
			return nil
		}
		event := Event{Kind: "task_queued", TaskID: envelope.ID, Task: envelope.Task, Scope: envelope.Scope, State: state, Retries: envelope.Retries, At: c.config.Clock().UTC()}
		if err := ValidateEvent(event); err != nil {
			return err
		}
		return c.config.Events.PublishEvent(ctx, event)
	})
}
