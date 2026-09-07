package testing

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/Newton-School/gogo/async"
)

func (m *Memory) quarantineCursor(queue string, position uint64) string {
	digest := sha256.Sum256([]byte(m.quarantineNamespace + "\x00" + queue))
	return base64.RawURLEncoding.EncodeToString([]byte("q1:" + hex.EncodeToString(digest[:]) + ":" + strconv.FormatUint(position, 10)))
}

// Called under m.mu: the namespace and append-only position space must agree.
func (m *Memory) quarantinePosition(queue, cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	at := strings.LastIndex(string(raw), ":")
	if err != nil || at < 0 || m.quarantineNamespace == "" {
		return 0, async.ErrInvalid
	}
	position, err := strconv.ParseUint(string(raw[at+1:]), 10, 64)
	if err != nil || position == 0 || m.quarantineCursor(queue, position) != cursor {
		return 0, async.ErrInvalid
	}
	return position, nil
}

func (m *Memory) ReadQuarantine(ctx context.Context, queue, after string, limit int) (async.QuarantinePage, error) {
	if limit < 1 || limit > 1000 || queue == "" || !async.ValidEventCursor(after) {
		return async.QuarantinePage{}, async.ErrInvalid
	}
	if err := m.check(ctx, "read_quarantine"); err != nil {
		return async.QuarantinePage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	position, err := m.quarantinePosition(queue, after)
	if err != nil {
		return async.QuarantinePage{}, err
	}
	page := async.QuarantinePage{NextCursor: after}
	for index, record := range m.quarantine {
		if uint64(index+1) <= position || record.ID == "" || record.Queue != queue {
			continue
		}
		cursor := m.quarantineCursor(queue, uint64(index+1))
		page.Entries = append(page.Entries, async.QuarantineEntry{Cursor: cursor, Record: record})
		page.NextCursor = cursor
		if len(page.Entries) == limit {
			break
		}
	}
	return page, nil
}

func (m *Memory) RemoveQuarantine(ctx context.Context, entry async.QuarantineEntry) (bool, error) {
	if ctx == nil || async.ValidateQuarantineEntry(entry) != nil {
		return false, async.ErrInvalid
	}
	if err := m.check(ctx, "remove_quarantine"); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	position, err := m.quarantinePosition(entry.Record.Queue, entry.Cursor)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if position > uint64(len(m.quarantine)) || m.quarantine[position-1].ID == "" {
		return false, nil
	}
	record := m.quarantine[position-1]
	expected := entry.Record
	if record.ID != expected.ID || record.Queue != expected.Queue || record.SourceReceipt != expected.SourceReceipt || record.Reason != expected.Reason || record.Digest != expected.Digest || record.Priority != expected.Priority || !record.FirstSeen.Equal(expected.FirstSeen) {
		return false, async.ErrConflict
	}
	// A hole releases metadata without shifting or recycling any cursor.
	m.quarantine[position-1] = async.QuarantineRecord{}
	return true, nil
}

var _ async.QuarantineReader = (*Memory)(nil)
var _ async.QuarantineRemover = (*Memory)(nil)
