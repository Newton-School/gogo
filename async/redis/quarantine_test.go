package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	redigo "github.com/redis/go-redis/v9"
)

func quarantineBackend(t *testing.T, database int, cfg connector.Config) *adapter.Broker {
	t.Helper()
	address, err := url.Parse(cfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	address.Path = "/" + strconv.Itoa(database)
	cfg.URL, cfg.Role = address.String(), connector.TaskRole
	conn, err := connector.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &adapter.Broker{Connection: conn, Queues: []string{"default", "other"}}
}

func quarantineRedisKey(b *adapter.Broker, queue string) string {
	return "queue:{" + connector.Digest(queue) + "}:quarantine"
}

func seedQuarantine(t *testing.T, b *adapter.Broker) async.Delivery {
	t.Helper()
	ctx := context.Background()
	e := taskEnvelope(t)
	e.Args = json.RawMessage(`"synthetic-private-argument"`)
	if err := b.Publish(ctx, e); err != nil {
		t.Fatal(err)
	}
	d, err := b.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "quarantine-worker"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := b.Reject(ctx, d, "unknown_task", false); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestRealRedisQuarantineInspectionIsPayloadFreeBoundedAndDatabaseBound(t *testing.T) {
	ctx := context.Background()
	cfg := fixture.Start(t)
	b := quarantineBackend(t, 1, cfg)
	start := time.Now().Add(-time.Second)
	for range 3 {
		seedQuarantine(t, b)
	}
	page, err := b.ReadQuarantine(ctx, "default", "", 2)
	if err != nil || len(page.Entries) != 2 {
		t.Fatal(page, err)
	}
	for _, entry := range page.Entries {
		if entry.Record.Reason != "unknown_task" || entry.Record.Priority != -1 || entry.Record.FirstSeen.Before(start) || entry.Record.FirstSeen.After(time.Now().Add(time.Second)) {
			t.Fatal(entry)
		}
	}
	second, err := b.ReadQuarantine(ctx, "default", page.NextCursor, 2)
	if err != nil || len(second.Entries) != 1 || second.Entries[0].Record.ID == page.Entries[1].Record.ID {
		t.Fatal(second, err)
	}
	empty, err := b.ReadQuarantine(ctx, "default", second.NextCursor, 2)
	if err != nil || len(empty.Entries) != 0 || empty.NextCursor != second.NextCursor {
		t.Fatal(empty, err)
	}
	raw, err := b.Connection.Client().XRange(ctx, quarantineRedisKey(b, "default"), "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []any{page, raw} {
		encoded, err := json.Marshal(v)
		if err != nil || strings.Contains(string(encoded), "synthetic-private-argument") || strings.Contains(string(encoded), "test.run") {
			t.Fatal("message contents reached diagnostics")
		}
	}
	for _, cursor := range []string{"$", "0-0", "bad\nvalue", strings.Repeat("a", 257)} {
		if _, err := b.ReadQuarantine(ctx, "default", cursor, 2); !errors.Is(err, async.ErrInvalid) {
			t.Fatal("malformed cursor accepted", err)
		}
	}
	if _, err := b.ReadQuarantine(ctx, "other", page.NextCursor, 2); !errors.Is(err, async.ErrInvalid) {
		t.Fatal("cross-queue cursor accepted", err)
	}
	other := quarantineBackend(t, 2, cfg)
	if _, err := other.ReadQuarantine(ctx, "default", page.NextCursor, 2); !errors.Is(err, async.ErrInvalid) {
		t.Fatal("cross-database cursor accepted", err)
	}
	stats, err := b.Inspect(ctx, []string{"default"})
	if err != nil || len(stats) != 1 || stats[0].Quarantined != 3 || stats[0].Pending != 0 || stats[0].Queued != 0 {
		t.Fatal(stats, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.ReadQuarantine(canceled, "default", "", 2); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRealRedisQuarantineCorruptLaterRecordReturnsNoPartialPage(t *testing.T) {
	ctx := context.Background()
	b := quarantineBackend(t, 1, fixture.Start(t))
	seedQuarantine(t, b)
	key := quarantineRedisKey(b, "default")
	for _, scenario := range []string{"reason", "digest", "receipt", "priority"} {
		fields := map[string]any{"receipt": "1-0", "code": "rejected", "digest": strings.Repeat("a", 64)}
		switch scenario {
		case "reason":
			fields["code"] = "private provider details"
		case "digest":
			fields["digest"] = strings.Repeat("A", 64)
		case "receipt":
			fields["receipt"] = "0-01"
		case "priority":
			fields["priority"] = "10"
		}
		id, err := b.Connection.Client().XAdd(ctx, &redigo.XAddArgs{Stream: key, Values: fields}).Result()
		if err != nil {
			t.Fatal(err)
		}
		page, err := b.ReadQuarantine(ctx, "default", "", 10)
		if !errors.Is(err, async.ErrUnavailable) || len(page.Entries) != 0 || page.NextCursor != "" {
			t.Fatal("partial corrupt page exposed", scenario, page, err)
		}
		if err := b.Connection.Client().XDel(ctx, key, id).Err(); err != nil {
			t.Fatal(err)
		}
	}
	// Future writer metadata is accepted only as a validated number; arbitrary
	// extra source data is never projected into the diagnostic record.
	if err := b.Connection.Client().XAdd(ctx, &redigo.XAddArgs{Stream: key, Values: map[string]any{"receipt": "1-0", "code": "rejected", "digest": strings.Repeat("b", 64), "priority": "9", "body": "private-not-displayed"}}).Err(); err != nil {
		t.Fatal(err)
	}
	page, err := b.ReadQuarantine(ctx, "default", "", 10)
	if err != nil || len(page.Entries) != 2 || page.Entries[1].Record.Priority != 9 {
		t.Fatal(page, err)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "private-not-displayed") {
		t.Fatal("unrequested source field displayed")
	}
}
