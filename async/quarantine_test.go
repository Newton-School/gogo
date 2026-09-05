package async_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

type quarantineBroker struct {
	async.Broker
	read func(context.Context, string, string, int) (async.QuarantinePage, error)
}

func (b quarantineBroker) ReadQuarantine(ctx context.Context, queue, after string, limit int) (async.QuarantinePage, error) {
	return b.read(ctx, queue, after, limit)
}

func quarantinePage() async.QuarantinePage {
	return async.QuarantinePage{Entries: []async.QuarantineEntry{{Cursor: "cursor-one", Record: async.QuarantineRecord{
		ID: "entry-one", Queue: "default", SourceReceipt: "source-one", Reason: "unknown_task", Digest: strings.Repeat("a", 64), Priority: -1, FirstSeen: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}}, NextCursor: "cursor-one"}
}

func TestQuarantineInspectionRequiresExplicitCurrentQueueAuthority(t *testing.T) {
	for _, scenario := range []string{"allow", "missing", "denied", "late_denied", "policy_error", "policy_panic", "policy_cancel", "provider_error", "provider_panic", "provider_cancel", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reads, grants := 0, 0
			client, _, _ := producerFixture(t, func(c *async.ClientConfig) {
				if scenario != "missing" {
					c.Authorize = func(_ context.Context, action, scope, id string) error {
						grants++
						if action != "inspect_quarantine" || scope != "default" || id != "" {
							t.Fatal(action, scope, id)
						}
						switch scenario {
						case "denied":
							return async.ErrDenied
						case "late_denied":
							if grants == 2 {
								return async.ErrDenied
							}
						case "policy_error":
							return errors.New("private policy endpoint")
						case "policy_panic":
							panic("private policy panic")
						case "policy_cancel":
							cancel()
						}
						return nil
					}
				}
				base := c.Broker
				c.Broker = quarantineBroker{Broker: base, read: func(ctx context.Context, queue, after string, limit int) (async.QuarantinePage, error) {
					reads++
					if queue != "default" || after != "" || limit != 10 {
						t.Fatal(queue, after, limit)
					}
					switch scenario {
					case "provider_error":
						return quarantinePage(), errors.New("private provider endpoint")
					case "provider_panic":
						panic("private provider panic")
					case "provider_cancel":
						cancel()
					}
					return quarantinePage(), nil
				}}
				if scenario == "unsupported" {
					c.Broker = eventBroker{Broker: base}
				}
			})
			page, err := (async.Control{Client: client}).InspectQuarantine(ctx, "default", "", 10)
			if scenario == "allow" {
				if err != nil || len(page.Entries) != 1 || reads != 1 || grants != 2 {
					t.Fatal(page, err, reads, grants)
				}
				return
			}
			want := async.ErrUnavailable
			if scenario == "missing" || scenario == "denied" || scenario == "late_denied" {
				want = async.ErrDenied
			}
			if scenario == "policy_cancel" || scenario == "provider_cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) || len(page.Entries) != 0 || page.NextCursor != "" || strings.Contains(err.Error(), "private") {
				t.Fatal(page, err, want)
			}
			if (scenario == "missing" || scenario == "denied" || strings.HasPrefix(scenario, "policy_") || scenario == "unsupported") && reads != 0 {
				t.Fatal("provider called before authority", reads)
			}
		})
	}
}

func TestQuarantineInspectionRejectsEntireMalformedPage(t *testing.T) {
	for _, scenario := range []string{"scope", "reason", "digest", "timestamp", "timestamp_offset", "timestamp_local_year", "priority", "cursor", "duplicate_id", "next", "overflow"} {
		t.Run(scenario, func(t *testing.T) {
			client, _, _ := producerFixture(t, func(c *async.ClientConfig) {
				c.Authorize = func(context.Context, string, string, string) error { return nil }
				c.Broker = quarantineBroker{Broker: c.Broker, read: func(context.Context, string, string, int) (async.QuarantinePage, error) {
					page := quarantinePage()
					second := page.Entries[0]
					second.Cursor = "cursor-two"
					second.Record.ID = "entry-two"
					switch scenario {
					case "scope":
						second.Record.Queue = "other"
					case "reason":
						second.Record.Reason = "private body"
					case "digest":
						second.Record.Digest = "private digest"
					case "timestamp":
						second.Record.FirstSeen = time.Time{}
					case "timestamp_offset":
						second.Record.FirstSeen = time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("invalid", 25*3600))
					case "timestamp_local_year":
						second.Record.FirstSeen = time.Date(10000, 1, 1, 0, 0, 0, 0, time.FixedZone("future", 3600))
					case "priority":
						second.Record.Priority = 10
					case "cursor":
						second.Cursor = page.Entries[0].Cursor
					case "duplicate_id":
						second.Record.ID = page.Entries[0].Record.ID
					}
					page.Entries = append(page.Entries, second)
					page.NextCursor = second.Cursor
					if scenario == "next" {
						page.NextCursor = "other"
					}
					if scenario == "overflow" {
						page.Entries = append(page.Entries, second)
					}
					return page, nil
				}}
			})
			page, err := (async.Control{Client: client}).InspectQuarantine(context.Background(), "default", "", 2)
			if !errors.Is(err, async.ErrUnavailable) || len(page.Entries) != 0 || page.NextCursor != "" {
				t.Fatal(page, err)
			}
		})
	}
}

func TestMemoryQuarantineIsPayloadFreeIdempotentAndCursorBound(t *testing.T) {
	ctx := context.Background()
	client, backend, sig := producerFixture(t, func(c *async.ClientConfig) {
		c.Authorize = func(context.Context, string, string, string) error { return nil }
	})
	for index := 0; index < 3; index++ {
		sig.Options.ID = async.StableID("quarantine", string(rune('a'+index)))
		if _, err := client.Enqueue(ctx, sig); err != nil {
			t.Fatal(err)
		}
		delivery, err := backend.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := backend.Reject(ctx, delivery, "unknown_task", false); err != nil {
				t.Fatal(err)
			}
		}
	}
	control := async.Control{Client: client}
	first, err := control.InspectQuarantine(ctx, "default", "", 2)
	if err != nil || len(first.Entries) != 2 {
		t.Fatal(first, err)
	}
	second, err := control.InspectQuarantine(ctx, "default", first.NextCursor, 2)
	if err != nil || len(second.Entries) != 1 {
		t.Fatal(second, err)
	}
	empty, err := control.InspectQuarantine(ctx, "default", second.NextCursor, 2)
	if err != nil || len(empty.Entries) != 0 || empty.NextCursor != second.NextCursor {
		t.Fatal(empty, err)
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), "private-argument") || strings.Contains(string(encoded), "event.produce") {
		t.Fatal("quarantine retained message data")
	}
	if _, err := backend.ReadQuarantine(ctx, "other", first.NextCursor, 2); !errors.Is(err, async.ErrInvalid) {
		t.Fatal("cross-queue cursor accepted", err)
	}
	other := fakes.NewMemory()
	if _, err := other.ReadQuarantine(ctx, "default", first.NextCursor, 2); !errors.Is(err, async.ErrInvalid) {
		t.Fatal("cross-namespace cursor accepted", err)
	}
	stats, err := backend.Inspect(ctx, []string{"default"})
	if err != nil || len(stats) != 1 || stats[0].Quarantined != 3 || stats[0].Pending != 0 {
		t.Fatal(stats, err)
	}
}
