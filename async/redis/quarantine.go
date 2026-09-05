package redis

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
)

func (b *Broker) quarantineCursor(queue, position string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("q1:" + connector.Digest(b.queuePrefix(queue)) + ":" + position))
}

func (b *Broker) quarantinePosition(queue, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if !async.ValidEventCursor(cursor) {
		return "", async.ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	prefix := "q1:" + connector.Digest(b.queuePrefix(queue)) + ":"
	if err != nil || !strings.HasPrefix(string(raw), prefix) {
		return "", async.ErrInvalid
	}
	position := strings.TrimPrefix(string(raw), prefix)
	if position == "" || !validStreamCursor(position) || b.quarantineCursor(queue, position) != cursor {
		return "", async.ErrInvalid
	}
	return position, nil
}

// ReadQuarantine is a raw trusted port. Application callers must use
// Control.InspectQuarantine. The namespace/queue binding prevents accidental
// cursor reuse across streams; it is not a credential or a signed grant.
func (b *Broker) ReadQuarantine(ctx context.Context, queue, after string, limit int) (async.QuarantinePage, error) {
	if ctx == nil || b == nil || b.valid() != nil || !b.allowed(queue) || limit < 1 || limit > 1000 {
		return async.QuarantinePage{}, async.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return async.QuarantinePage{}, err
	}
	position, err := b.quarantinePosition(queue, after)
	if err != nil {
		return async.QuarantinePage{}, err
	}
	start := "-"
	if position != "" {
		start = "(" + position
	}
	items, err := b.Connection.Client().XRangeN(ctx, b.queuePrefix(queue)+":quarantine", start, "+", int64(limit)).Result()
	if err != nil {
		return async.QuarantinePage{}, err
	}
	page := async.QuarantinePage{NextCursor: after}
	for _, item := range items {
		if item.ID == "" || !validStreamCursor(item.ID) {
			return async.QuarantinePage{}, async.ErrUnavailable
		}
		millis, _, _ := strings.Cut(item.ID, "-")
		ms, err := strconv.ParseInt(millis, 10, 64)
		if err != nil {
			return async.QuarantinePage{}, async.ErrUnavailable
		}
		receipt, receiptOK := item.Values["receipt"].(string)
		code, codeOK := item.Values["code"].(string)
		digest, digestOK := item.Values["digest"].(string)
		record := async.QuarantineRecord{ID: item.ID, Queue: queue, SourceReceipt: receipt, Reason: code, Digest: digest, FirstSeen: time.UnixMilli(ms).UTC(), Priority: -1}
		if raw, exists := item.Values["priority"]; exists {
			value, ok := raw.(string)
			priority, err := strconv.Atoi(value)
			if !ok || err != nil || strconv.Itoa(priority) != value || priority < 0 || priority > 9 {
				return async.QuarantinePage{}, async.ErrUnavailable
			}
			record.Priority = priority
		}
		if !receiptOK || !codeOK || !digestOK || receipt == "" || !validStreamCursor(receipt) || async.ValidateQuarantineRecord(record) != nil {
			return async.QuarantinePage{}, async.ErrUnavailable
		}
		cursor := b.quarantineCursor(queue, item.ID)
		page.Entries = append(page.Entries, async.QuarantineEntry{Cursor: cursor, Record: record})
		page.NextCursor = cursor
	}
	if err := ctx.Err(); err != nil {
		return async.QuarantinePage{}, err
	}
	return page, nil
}

var _ async.QuarantineReader = (*Broker)(nil)
