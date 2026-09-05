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

func (m *Memory) ReadQuarantine(ctx context.Context, queue, after string, limit int) (async.QuarantinePage, error) {
	if limit < 1 || limit > 1000 || queue == "" || !async.ValidEventCursor(after) {
		return async.QuarantinePage{}, async.ErrInvalid
	}
	if err := m.check(ctx, "read_quarantine"); err != nil {
		return async.QuarantinePage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var position uint64
	if after != "" {
		raw, err := base64.RawURLEncoding.DecodeString(after)
		at := strings.LastIndex(string(raw), ":")
		if err != nil || at < 0 || m.quarantineNamespace == "" {
			return async.QuarantinePage{}, async.ErrInvalid
		}
		position, err = strconv.ParseUint(string(raw[at+1:]), 10, 64)
		if err != nil || position == 0 || m.quarantineCursor(queue, position) != after {
			return async.QuarantinePage{}, async.ErrInvalid
		}
	}
	page := async.QuarantinePage{NextCursor: after}
	for index, record := range m.quarantine {
		if uint64(index+1) <= position || record.Queue != queue {
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

var _ async.QuarantineReader = (*Memory)(nil)
