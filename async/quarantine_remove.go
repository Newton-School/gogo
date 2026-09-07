package async

import (
	"context"
	"errors"
)

// QuarantineRemover is a trusted optional infrastructure port. It removes only
// the exact diagnostic entry identified by a previously inspected cursor and
// complete metadata. Namespace/queue/position must agree. The match and removal
// are atomic, never an inspection followed by an unconditional delete.
//
// A nil error confirms removal (true) or absence (false). Absence does not prove
// this caller performed an earlier removal. The exact ErrInvalid sentinel means
// input failed before a write; exact ErrConflict means existing metadata differed
// and nothing was removed. Wrapped, joined and all other errors may follow an
// applied removal. Repeating the exact entry is
// safe; providers must not recycle positions. Source streams/ACKs, task state
// and executable input are outside this operation.
type QuarantineRemover interface {
	RemoveQuarantine(context.Context, QuarantineEntry) (bool, error)
}

type QuarantineRemovalOutcome string

const (
	QuarantineRemovalNotAttempted QuarantineRemovalOutcome = "not_attempted"
	QuarantineRemovalRemoved      QuarantineRemovalOutcome = "removed"
	QuarantineRemovalAbsent       QuarantineRemovalOutcome = "already_absent"
	QuarantineRemovalConflict     QuarantineRemovalOutcome = "conflict"
	QuarantineRemovalUnknown      QuarantineRemovalOutcome = "unknown"
)

func ValidateQuarantineEntry(entry QuarantineEntry) error {
	if entry.Cursor == "" || !ValidEventCursor(entry.Cursor) || ValidateQuarantineRecord(entry.Record) != nil {
		return ErrInvalid
	}
	return nil
}

// RemoveQuarantine requires an explicit remove_quarantine grant for this exact
// queue and record ID. Inspection authority is not removal authority. Always
// retain both the outcome and error: cancellation can follow confirmed removal,
// and an unknown outcome must never be interpreted as an unchanged queue.
func (c Control) RemoveQuarantine(ctx context.Context, entry QuarantineEntry) (outcome QuarantineRemovalOutcome, err error) {
	outcome = QuarantineRemovalNotAttempted
	attempted := false
	defer func() {
		if recover() != nil {
			if attempted {
				outcome = QuarantineRemovalUnknown
			}
			err = ErrUnavailable
		}
		if ctx != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if ctx == nil || c.Client == nil || ValidateQuarantineEntry(entry) != nil {
		return outcome, ErrInvalid
	}
	if err := c.authorizeQuarantineAction(ctx, "remove_quarantine", entry.Record.Queue, entry.Record.ID); err != nil {
		return outcome, err
	}
	remover, ok := c.Client.config.Broker.(QuarantineRemover)
	if !ok {
		return outcome, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return outcome, err
	}
	attempted = true
	outcome = QuarantineRemovalUnknown
	removed, err := remover.RemoveQuarantine(ctx, entry)
	if err != nil {
		if removed {
			return outcome, ErrUnavailable
		}
		switch {
		case err == ErrInvalid:
			return QuarantineRemovalNotAttempted, ErrInvalid
		case err == ErrConflict:
			return QuarantineRemovalConflict, ErrConflict
		case errors.Is(err, context.Canceled):
			return outcome, context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			return outcome, context.DeadlineExceeded
		default:
			return outcome, ErrUnavailable
		}
	}
	if removed {
		return QuarantineRemovalRemoved, nil
	}
	return QuarantineRemovalAbsent, nil
}
