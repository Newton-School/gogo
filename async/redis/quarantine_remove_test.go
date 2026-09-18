package redis_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	redigo "github.com/redis/go-redis/v9"
)

func TestRealRedisQuarantineRemovalIsAtomicExactAndDoesNotTouchLiveState(t *testing.T) {
	ctx := context.Background()
	config := fixture.Start(t)
	b := quarantineBackend(t, 1, config)
	for range 3 {
		seedQuarantine(t, b)
	}
	page, err := b.ReadQuarantine(ctx, "default", "", 10)
	if err != nil || len(page.Entries) != 3 {
		t.Fatal(page, err)
	}
	entry := page.Entries[1]
	for _, field := range []string{"id", "cursor", "receipt", "reason", "digest", "priority", "time", "queue"} {
		changed := entry
		switch field {
		case "id":
			changed.Record.ID = page.Entries[0].Record.ID
		case "cursor":
			changed.Cursor = page.Entries[0].Cursor
		case "receipt":
			changed.Record.SourceReceipt = "1-0"
		case "reason":
			changed.Record.Reason = "rejected"
		case "digest":
			changed.Record.Digest = strings.Repeat("f", 64)
		case "priority":
			changed.Record.Priority = 9
		case "time":
			changed.Record.FirstSeen = changed.Record.FirstSeen.Add(time.Millisecond)
		case "queue":
			changed.Record.Queue = "other"
		}
		if removed, err := b.RemoveQuarantine(ctx, changed); removed || err == nil {
			t.Fatal("mismatched metadata removed quarantine", field, removed, err)
		}
	}
	other := quarantineBackend(t, 2, config)
	if removed, err := other.RemoveQuarantine(ctx, entry); removed || !errors.Is(err, async.ErrInvalid) {
		t.Fatal("cross-database removal accepted", removed, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if removed, err := b.RemoveQuarantine(canceled, entry); removed || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled removal accepted", removed, err)
	}
	// Preserve a separate pending task and a queued task, including result data.
	pending := taskEnvelope(t)
	results := &adapter.Results{Connection: b.Connection}
	if err := results.Register(ctx, pending, async.Queued); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "live-worker"}); err != nil {
		t.Fatal(err)
	}
	queued := taskEnvelope(t)
	if err := b.Publish(ctx, queued); err != nil {
		t.Fatal(err)
	}
	prefix := strings.TrimSuffix(quarantineRedisKey(b, "default"), ":quarantine")
	keys := []string{b.Connection.PartitionKey("task", pending.ID, "state"), prefix + ":published:" + pending.ID + ":0", prefix + ":published:" + queued.ID + ":0"}
	for priority := range 10 {
		keys = append(keys, prefix+":p"+strconv.Itoa(priority))
	}
	before := map[string]string{}
	for _, key := range keys {
		value, err := b.Connection.Client().Dump(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
		before[key] = value
	}
	var workers sync.WaitGroup
	outcomes := make(chan bool, 2)
	for range 2 {
		workers.Go(func() {
			removed, err := b.RemoveQuarantine(ctx, entry)
			if err != nil {
				t.Error(err)
			}
			outcomes <- removed
		})
	}
	workers.Wait()
	if (<-outcomes) == (<-outcomes) {
		t.Fatal("concurrent removal did not return one removed and one absent")
	}
	for _, key := range keys {
		value, err := b.Connection.Client().Dump(ctx, key).Result()
		if err != nil || value != before[key] {
			t.Fatal("quarantine removal changed live broker/result state", key, err)
		}
	}
	remaining, err := b.ReadQuarantine(ctx, "default", entry.Cursor, 10)
	if err != nil || len(remaining.Entries) != 1 || remaining.Entries[0] != page.Entries[2] {
		t.Fatal("deleted position broke cursor resume", remaining, err)
	}
	if removed, err := b.RemoveQuarantine(ctx, entry); removed || err != nil {
		t.Fatal("exact retry affected another diagnostic", removed, err)
	}
	stats, err := b.Inspect(ctx, []string{"default"})
	if err != nil || stats[0].Quarantined != 2 || stats[0].Pending != 1 || stats[0].Queued != 1 {
		t.Fatal("wrong state after diagnostic removal", stats, err)
	}
}

func TestRealRedisQuarantineRemovalRefusesUnknownOrDuplicateStoredMetadata(t *testing.T) {
	ctx := context.Background()
	b := quarantineBackend(t, 1, fixture.Start(t))
	key := quarantineRedisKey(b, "default")
	for _, scenario := range []string{"priority", "unknown", "duplicate"} {
		fields := []any{"receipt", "1-0", "code", "rejected", "digest", strings.Repeat("a", 64), "priority", "9"}
		if scenario == "unknown" {
			fields = append(fields, "future_metadata", "opaque")
		}
		if scenario == "duplicate" {
			fields = append(fields, "receipt", "1-0")
		}
		id, err := b.Connection.Client().XAdd(ctx, &redigo.XAddArgs{Stream: key, Values: fields}).Result()
		if err != nil {
			t.Fatal(err)
		}
		page, err := b.ReadQuarantine(ctx, "default", "", 10)
		if err != nil {
			t.Fatal(err)
		}
		var entry async.QuarantineEntry
		for _, item := range page.Entries {
			if item.Record.ID == id {
				entry = item
			}
		}
		removed, err := b.RemoveQuarantine(ctx, entry)
		if scenario == "priority" {
			if err != nil || !removed {
				t.Fatal("known complete priority metadata rejected", removed, err)
			}
		} else if err == nil || removed {
			t.Fatal("unsupported stored metadata deleted", scenario, removed, err)
		}
		rows, err := b.Connection.Client().XRange(ctx, key, id, id).Result()
		if err != nil || scenario != "priority" && len(rows) != 1 || scenario == "priority" && len(rows) != 0 {
			t.Fatal("preflight changed stored row", scenario, rows, err)
		}
	}
}

func TestRealRedisQuarantineRemovalDoesNotTreatInvalidPriorityAsLegacyAbsence(t *testing.T) {
	ctx := context.Background()
	b := quarantineBackend(t, 1, fixture.Start(t))
	seedQuarantine(t, b)
	page, err := b.ReadQuarantine(ctx, "default", "", 1)
	if err != nil || len(page.Entries) != 1 {
		t.Fatal(page, err)
	}
	entry := page.Entries[0]
	key := quarantineRedisKey(b, "default")
	// The public record cannot represent an explicit invalid -1 priority.
	// A forged copy of the known cursor/metadata must not make it removable.
	id, err := b.Connection.Client().XAdd(ctx, &redigo.XAddArgs{Stream: key, Values: map[string]any{"receipt": entry.Record.SourceReceipt, "code": entry.Record.Reason, "digest": entry.Record.Digest, "priority": "-1"}}).Result()
	if err != nil {
		t.Fatal(err)
	}
	// Keep the database/queue binding while changing only the position suffix.
	raw, err := base64.RawURLEncoding.DecodeString(entry.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	prefix := strings.TrimSuffix(string(raw), entry.Record.ID)
	entry.Cursor = base64.RawURLEncoding.EncodeToString([]byte(prefix + id))
	entry.Record.ID = id
	millis, _, _ := strings.Cut(id, "-")
	ms, _ := strconv.ParseInt(millis, 10, 64)
	entry.Record.FirstSeen = time.UnixMilli(ms).UTC()
	if removed, err := b.RemoveQuarantine(ctx, entry); removed || err == nil {
		t.Fatal("invalid explicit priority accepted as absent", removed, err)
	}
	rows, err := b.Connection.Client().XRange(ctx, key, id, id).Result()
	if err != nil || len(rows) != 1 {
		t.Fatal("invalid stored priority was removed", rows, err)
	}
}

var _ async.QuarantineRemover = (*adapter.Broker)(nil)
