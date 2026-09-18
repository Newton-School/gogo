package redis

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

// Events is an optional lossy monitor stream, never authoritative task state.
// Scope has a separate stream so an authorized cursor cannot reveal another
// scope's task counts through gaps in a shared stream position.
type Events struct {
	Connection *connector.Connection
	MaxLength  int64
}

func (e *Events) key(scope string) string {
	return "events:{" + connector.Digest(scope) + "}"
}
func (e *Events) PublishEvent(ctx context.Context, event async.Event) error {
	if ctx == nil || e == nil || e.Connection == nil || async.ValidateEvent(event) != nil {
		return async.ErrInvalid
	}
	maxLength := e.MaxLength
	if maxLength == 0 {
		maxLength = 10000
	}
	if maxLength < 1 || maxLength > 1000000 {
		return async.ErrInvalid
	}
	raw, err := json.Marshal(event)
	if err != nil || len(raw) > 16<<10 {
		return async.ErrInvalid
	}
	return e.Connection.Client().XAdd(ctx, &redigo.XAddArgs{Stream: e.key(event.Scope), MaxLen: maxLength, Approx: true, Values: map[string]any{"event": raw}}).Err()
}
func validStreamCursor(cursor string) bool {
	if cursor == "" {
		return true
	}
	if len(cursor) > 41 {
		return false
	}
	parts := strings.Split(cursor, "-")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil || strconv.FormatUint(value, 10) != part {
			return false
		}
	}
	return true
}

// ReadEvents is a trusted raw adapter. Use async.Control.ReadEvents or
// ObserveEvents for application-facing current authorization and filtering.
func (e *Events) ReadEvents(ctx context.Context, scope, after string, limit int) (async.EventPage, error) {
	if ctx == nil || e == nil || e.Connection == nil || limit < 1 || limit > 1000 || len(scope) > 256 || !async.ValidEventCursor(scope) || !validStreamCursor(after) {
		return async.EventPage{}, async.ErrInvalid
	}
	start := "-"
	if after != "" {
		start = "(" + after
	}
	messages, err := e.Connection.Client().XRangeN(ctx, e.key(scope), start, "+", int64(limit)).Result()
	if err != nil {
		return async.EventPage{}, err
	}
	entries := make([]async.EventEntry, 0, len(messages))
	cursor := after
	for _, message := range messages {
		raw, ok := message.Values["event"].(string)
		if !ok || len(raw) > 16<<10 || !validStreamCursor(message.ID) || message.ID == "" {
			return async.EventPage{}, async.ErrUnavailable
		}
		var event async.Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil || async.ValidateEvent(event) != nil || event.Scope != scope {
			return async.EventPage{}, async.ErrUnavailable
		}
		entries = append(entries, async.EventEntry{Cursor: message.ID, Event: event})
		cursor = message.ID
	}
	return async.EventPage{Entries: entries, NextCursor: cursor}, nil
}

// Read retains the original trusted adapter convenience API. It has no caller
// authorization; application entry points must use async.Control instead.
func (e *Events) Read(ctx context.Context, scope, after string, limit int) ([]async.Event, string, error) {
	page, err := e.ReadEvents(ctx, scope, after, limit)
	if err != nil {
		return nil, "", err
	}
	events := make([]async.Event, len(page.Entries))
	for i, entry := range page.Entries {
		events[i] = entry.Event
	}
	return events, page.NextCursor, nil
}

var _ async.EventSink = (*Events)(nil)
var _ async.EventReader = (*Events)(nil)
