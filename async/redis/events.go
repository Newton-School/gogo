package redis

import (
	"context"
	"encoding/json"
	"fmt"

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
	return e.Connection.Namespace() + ":events:{" + connector.Digest(scope) + "}"
}
func (e *Events) PublishEvent(ctx context.Context, event async.Event) error {
	if e.Connection == nil || event.At.IsZero() {
		return async.ErrInvalid
	}
	switch event.Kind {
	case "task_started", "task_retry", "task_terminal", "worker_heartbeat", "worker_offline", "task_progress":
	default:
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
func (e *Events) Read(ctx context.Context, scope, after string, limit int) ([]async.Event, string, error) {
	if e.Connection == nil || limit < 1 || limit > 1000 {
		return nil, "", async.ErrInvalid
	}
	start := "-"
	if after != "" {
		start = "(" + after
	}
	messages, err := e.Connection.Client().XRangeN(ctx, e.key(scope), start, "+", int64(limit)).Result()
	if err != nil {
		return nil, "", err
	}
	events := make([]async.Event, 0, len(messages))
	cursor := after
	for _, message := range messages {
		raw, ok := message.Values["event"].(string)
		if !ok {
			return nil, "", fmt.Errorf("%w: malformed monitor event", async.ErrUnavailable)
		}
		var event async.Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, "", async.ErrUnavailable
		}
		events = append(events, event)
		cursor = message.ID
	}
	return events, cursor, nil
}

var _ async.EventSink = (*Events)(nil)
