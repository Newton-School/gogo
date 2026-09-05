package async

import (
	"context"
	"errors"
	"time"
)

// QuarantineReader is a trusted infrastructure port. It returns only diagnostic
// metadata, never message bodies, task arguments or executable replay input.
// Cursors must bind this provider's namespace and the exact queue. Implementors
// must honor context and return at most limit entries, ordered by their cursor.
type QuarantineReader interface {
	ReadQuarantine(context.Context, string, string, int) (QuarantinePage, error)
}

type QuarantineRecord struct {
	ID, Queue, SourceReceipt string
	Reason, Digest           string
	FirstSeen                time.Time
	// Priority is -1 when an older diagnostic record did not retain it.
	Priority int
}

type QuarantineEntry struct {
	Cursor string
	Record QuarantineRecord
}

type QuarantinePage struct {
	Entries    []QuarantineEntry
	NextCursor string
}

func ValidQuarantineReason(reason string) bool {
	switch reason {
	case "invalid_envelope", "unknown_task", "invalid_arguments", "task_identity_conflict", "rejected":
		return true
	default:
		return false
	}
}

func ValidateQuarantineRecord(record QuarantineRecord) error {
	if record.ID == "" || !ValidEventCursor(record.ID) || !namePattern.MatchString(record.Queue) || record.SourceReceipt == "" || !ValidEventCursor(record.SourceReceipt) || !ValidQuarantineReason(record.Reason) || len(record.Digest) != 64 || record.FirstSeen.IsZero() || record.FirstSeen.UTC().Year() < 0 || record.FirstSeen.UTC().Year() > 9999 || record.Priority < -1 || record.Priority > 9 {
		return ErrInvalid
	}
	if _, err := record.FirstSeen.MarshalJSON(); err != nil {
		return ErrInvalid
	}
	for _, c := range record.Digest {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ErrInvalid
		}
	}
	return nil
}

func (c Control) authorizeQuarantine(ctx context.Context, queue string) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Client.config.Authorize == nil {
		return ErrDenied
	}
	err = c.Client.config.Authorize(ctx, "inspect_quarantine", queue, "")
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrDenied) {
		return ErrDenied
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrUnavailable
}

// InspectQuarantine requires a distinct, explicit queue-level diagnostic grant.
// Unknown/malformed messages have no trustworthy task scope, so ordinary task
// or queue inspection authority does not grant quarantine enumeration. A page
// is released only after current authorization and complete provider validation.
func (c Control) InspectQuarantine(ctx context.Context, queue, after string, limit int) (out QuarantinePage, err error) {
	defer func() {
		if recover() != nil {
			out, err = QuarantinePage{}, ErrUnavailable
		}
		if ctx != nil && ctx.Err() != nil {
			out, err = QuarantinePage{}, ctx.Err()
		}
	}()
	if ctx == nil || c.Client == nil || !namePattern.MatchString(queue) || !ValidEventCursor(after) || limit < 1 || limit > 1000 {
		return QuarantinePage{}, ErrInvalid
	}
	if err := c.authorizeQuarantine(ctx, queue); err != nil {
		return QuarantinePage{}, err
	}
	reader, ok := c.Client.config.Broker.(QuarantineReader)
	if !ok {
		return QuarantinePage{}, ErrUnavailable
	}
	page, err := reader.ReadQuarantine(ctx, queue, after, limit)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return QuarantinePage{}, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return QuarantinePage{}, context.DeadlineExceeded
		}
		if errors.Is(err, ErrInvalid) {
			return QuarantinePage{}, ErrInvalid
		}
		return QuarantinePage{}, ErrUnavailable
	}
	if err := c.authorizeQuarantine(ctx, queue); err != nil {
		return QuarantinePage{}, err
	}
	if len(page.Entries) > limit || !ValidEventCursor(page.NextCursor) {
		return QuarantinePage{}, ErrUnavailable
	}
	seen, ids := map[string]bool{after: true}, map[string]bool{}
	for _, entry := range page.Entries {
		if entry.Cursor == "" || !ValidEventCursor(entry.Cursor) || seen[entry.Cursor] || ids[entry.Record.ID] || entry.Record.Queue != queue || ValidateQuarantineRecord(entry.Record) != nil {
			return QuarantinePage{}, ErrUnavailable
		}
		seen[entry.Cursor], ids[entry.Record.ID] = true, true
	}
	if len(page.Entries) == 0 {
		if page.NextCursor != after {
			return QuarantinePage{}, ErrUnavailable
		}
	} else if page.NextCursor != page.Entries[len(page.Entries)-1].Cursor {
		return QuarantinePage{}, ErrUnavailable
	}
	return QuarantinePage{Entries: append([]QuarantineEntry(nil), page.Entries...), NextCursor: page.NextCursor}, nil
}
