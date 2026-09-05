package async

import (
	"context"
	"errors"
	"iter"
	"time"
	"unicode/utf8"
)

// EventReader is a trusted, lossy observation port. It does not authorize
// callers and never supplies execution/result authority. Custom providers must
// honor context cancellation and return a bounded, ordered page for one scope.
type EventReader interface {
	ReadEvents(context.Context, string, string, int) (EventPage, error)
}

type EventEntry struct {
	Cursor string
	Event  Event
}

type EventPage struct {
	Entries    []EventEntry
	NextCursor string
}

func validEventScope(scope string) bool {
	if len(scope) > 256 || !utf8.ValidString(scope) {
		return false
	}
	for _, c := range scope {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

// ValidEventCursor applies the portable opaque-cursor bounds. Providers may
// impose their own format rules; an empty cursor starts the retained history.
func ValidEventCursor(cursor string) bool {
	if len(cursor) > 256 || !utf8.ValidString(cursor) {
		return false
	}
	for _, c := range cursor {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

func ValidateEvent(event Event) error {
	if event.At.IsZero() || event.At.Year() < 0 || event.At.Year() > 9999 || !validEventScope(event.Scope) || event.Retries < 0 || event.WorkerID != "" && !ValidWorkerID(event.WorkerID) {
		return ErrInvalid
	}
	switch event.Kind {
	case "worker_heartbeat", "worker_offline":
		if event.WorkerID == "" || event.TaskID != "" || event.Task != "" || event.State != "" || event.Retries != 0 {
			return ErrInvalid
		}
		return nil
	case "task_queued":
		if event.State != Queued && event.State != Scheduled {
			return ErrInvalid
		}
	case "task_started", "task_progress", "task_replaced":
		if event.State != Running {
			return ErrInvalid
		}
	case "task_retry":
		if event.State != RetryWait {
			return ErrInvalid
		}
	case "task_terminal":
		if !event.State.Terminal() {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if !idPattern.MatchString(event.TaskID) || !namePattern.MatchString(event.Task) {
		return ErrInvalid
	}
	return nil
}

func validateEventPage(page EventPage, scope, after string, limit int) error {
	if len(page.Entries) > limit || !ValidEventCursor(page.NextCursor) {
		return ErrUnavailable
	}
	seen := map[string]bool{after: true}
	for _, entry := range page.Entries {
		if entry.Cursor == "" || !ValidEventCursor(entry.Cursor) || seen[entry.Cursor] || entry.Event.Scope != scope || ValidateEvent(entry.Event) != nil {
			return ErrUnavailable
		}
		seen[entry.Cursor] = true
	}
	if len(page.Entries) == 0 {
		if page.NextCursor != after {
			return ErrUnavailable
		}
	} else if page.NextCursor != page.Entries[len(page.Entries)-1].Cursor {
		return ErrUnavailable
	}
	return nil
}

func (c Control) authorizeEvents(ctx context.Context, scope string) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Client.config.Authorize == nil {
		return ErrDenied
	}
	err = c.Client.config.Authorize(ctx, "inspect_events", scope, "")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrDenied) {
		return ErrDenied
	}
	return ErrUnavailable
}

func (c Control) authorizeEvent(ctx context.Context, event Event) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	id := event.TaskID
	if id == "" {
		id = event.WorkerID
	}
	return c.authorizeInspection(ctx, event.Scope, id)
}

// ReadEvents explicitly authorizes stream enumeration, then each observed
// task/worker identity. Only explicit per-event denial is hidden; policy/provider
// outages and cancellation return no page. NextCursor advances past hidden events
// within the authorized scope, but never crosses into another scope's stream.
func (c Control) ReadEvents(ctx context.Context, source EventReader, scope, after string, limit int) (out EventPage, err error) {
	defer func() {
		if recover() != nil {
			out, err = EventPage{}, ErrUnavailable
		}
	}()
	if ctx == nil || c.Client == nil || source == nil || !validEventScope(scope) || !ValidEventCursor(after) || limit < 1 || limit > 1000 {
		return EventPage{}, ErrInvalid
	}
	if err := c.authorizeEvents(ctx, scope); err != nil {
		return EventPage{}, err
	}
	page, err := source.ReadEvents(ctx, scope, after, limit)
	if ctx.Err() != nil {
		return EventPage{}, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return EventPage{}, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return EventPage{}, context.DeadlineExceeded
		}
		if errors.Is(err, ErrInvalid) {
			return EventPage{}, ErrInvalid
		}
		return EventPage{}, ErrUnavailable
	}
	if err := c.authorizeEvents(ctx, scope); err != nil {
		return EventPage{}, err
	}
	if err := validateEventPage(page, scope, after, limit); err != nil {
		return EventPage{}, err
	}
	visible := make([]EventEntry, 0, len(page.Entries))
	for _, entry := range page.Entries {
		if err := c.authorizeEvent(ctx, entry.Event); err == nil {
			visible = append(visible, entry)
		} else if !errors.Is(err, ErrDenied) {
			return EventPage{}, err
		}
	}
	if err := c.authorizeEvents(ctx, scope); err != nil {
		return EventPage{}, err
	}
	return EventPage{Entries: visible, NextCursor: page.NextCursor}, nil
}

type EventStreamOptions struct {
	After        string
	BatchSize    int
	PollInterval time.Duration
}

// ObserveEvents starts only when the returned iterator is consumed. It has no
// background goroutine or persistent subscription. Retain each yielded cursor
// for resumption; break/cancellation stops observation, never tasks. Events may
// be trimmed or duplicated and are never a substitute for authoritative results.
func (c Control) ObserveEvents(ctx context.Context, source EventReader, scope string, options EventStreamOptions) iter.Seq2[EventEntry, error] {
	return func(yield func(EventEntry, error) bool) {
		limit := options.BatchSize
		if limit == 0 {
			limit = 100
		}
		poll := options.PollInterval
		if poll == 0 {
			poll = time.Second
		}
		if ctx == nil || c.Client == nil || source == nil || limit < 1 || limit > 1000 || poll < 10*time.Millisecond || poll > time.Minute || !validEventScope(scope) || !ValidEventCursor(options.After) {
			yield(EventEntry{}, ErrInvalid)
			return
		}
		cursor := options.After
		for {
			page, err := c.ReadEvents(ctx, source, scope, cursor, limit)
			if err != nil {
				yield(EventEntry{}, err)
				return
			}
			for _, entry := range page.Entries {
				if err := c.authorizeEvents(ctx, scope); err != nil {
					yield(EventEntry{}, err)
					return
				}
				if err := c.authorizeEvent(ctx, entry.Event); err != nil {
					if errors.Is(err, ErrDenied) {
						continue
					}
					yield(EventEntry{}, err)
					return
				}
				if !yield(entry, nil) {
					return
				}
			}
			cursor = page.NextCursor
			// Poll after every page, including pages whose events were all
			// hidden, to bound backend load and denial-heavy catch-up work.
			timer := time.NewTimer(poll)
			select {
			case <-ctx.Done():
				timer.Stop()
				yield(EventEntry{}, ctx.Err())
				return
			case <-timer.C:
			}
		}
	}
}
