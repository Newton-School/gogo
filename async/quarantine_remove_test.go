package async_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

type quarantineRemovalBroker struct {
	async.Broker
	remove func(context.Context, async.QuarantineEntry) (bool, error)
}

func (b quarantineRemovalBroker) RemoveQuarantine(ctx context.Context, entry async.QuarantineEntry) (bool, error) {
	return b.remove(ctx, entry)
}

func TestQuarantineRemovalRequiresExactActionAndReportsOutcomeUncertainty(t *testing.T) {
	for _, scenario := range []string{"removed", "absent", "missing_grant", "denied", "inspect_only", "policy_error", "policy_panic", "policy_cancel", "pre_canceled", "unsupported", "conflict", "invalid", "provider_error", "provider_panic", "provider_cancel", "confirmed_then_cancel", "inconsistent_reply"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entry := quarantinePage().Entries[0]
			calls := 0
			client, _, _ := producerFixture(t, func(config *async.ClientConfig) {
				if scenario != "missing_grant" {
					config.Authorize = func(_ context.Context, action, queue, id string) error {
						if action != "remove_quarantine" || queue != entry.Record.Queue || id != entry.Record.ID {
							t.Fatal("wrong removal grant", action, queue, id)
						}
						switch scenario {
						case "denied", "inspect_only":
							return async.ErrDenied
						case "policy_error":
							return errors.New("private authority details")
						case "policy_panic":
							panic("private authority panic")
						case "policy_cancel":
							cancel()
						}
						return nil
					}
				}
				base := config.Broker
				config.Broker = quarantineRemovalBroker{Broker: base, remove: func(_ context.Context, got async.QuarantineEntry) (bool, error) {
					calls++
					if got != entry {
						t.Fatal("entry identity changed")
					}
					switch scenario {
					case "absent":
						return false, nil
					case "conflict":
						return false, async.ErrConflict
					case "invalid":
						return false, async.ErrInvalid
					case "provider_error":
						return false, errors.New("private provider details")
					case "provider_panic":
						panic("private provider panic")
					case "provider_cancel":
						cancel()
						return false, context.Canceled
					case "confirmed_then_cancel":
						cancel()
						return true, nil
					case "inconsistent_reply":
						return true, async.ErrConflict
					}
					return true, nil
				}}
				if scenario == "unsupported" {
					config.Broker = eventBroker{Broker: base}
				}
			})
			if scenario == "pre_canceled" {
				cancel()
			}
			outcome, err := (async.Control{Client: client}).RemoveQuarantine(ctx, entry)
			want, wantErr, wantCalls := async.QuarantineRemovalNotAttempted, async.ErrUnavailable, 0
			switch scenario {
			case "removed":
				want, wantErr, wantCalls = async.QuarantineRemovalRemoved, nil, 1
			case "absent":
				want, wantErr, wantCalls = async.QuarantineRemovalAbsent, nil, 1
			case "missing_grant", "denied", "inspect_only":
				wantErr = async.ErrDenied
			case "policy_cancel", "pre_canceled":
				wantErr = context.Canceled
			case "conflict":
				want, wantErr, wantCalls = async.QuarantineRemovalConflict, async.ErrConflict, 1
			case "invalid":
				wantErr, wantCalls = async.ErrInvalid, 1
			case "provider_error", "provider_panic", "inconsistent_reply":
				want, wantCalls = async.QuarantineRemovalUnknown, 1
			case "provider_cancel":
				want, wantErr, wantCalls = async.QuarantineRemovalUnknown, context.Canceled, 1
			case "confirmed_then_cancel":
				want, wantErr, wantCalls = async.QuarantineRemovalRemoved, context.Canceled, 1
			}
			if outcome != want || !errors.Is(err, wantErr) || calls != wantCalls || err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal(scenario, outcome, err, calls, want, wantErr, wantCalls)
			}
		})
	}
}

func seedMemoryQuarantine(t *testing.T, client *async.Client, memory *fakes.Memory, signature async.Signature) async.QuarantineEntry {
	t.Helper()
	if _, err := client.Enqueue(context.Background(), signature); err != nil {
		t.Fatal(err)
	}
	delivery, err := memory.Consume(context.Background(), async.ConsumeOptions{Queues: []string{"default"}, Consumer: "quarantine-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.Reject(context.Background(), delivery, "rejected", false); err != nil {
		t.Fatal(err)
	}
	page, err := memory.ReadQuarantine(context.Background(), "default", "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	return page.Entries[len(page.Entries)-1]
}

func TestQuarantineRemovalUnknownAfterAppliedWriteCanBeRetriedExactly(t *testing.T) {
	for _, failure := range []string{"error", "panic", "cancel", "joined-invalid", "joined-conflict", "wrapped-invalid", "wrapped-conflict"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			first := true
			client, memory, signature := producerFixture(t, func(config *async.ClientConfig) {
				config.Authorize = func(context.Context, string, string, string) error { return nil }
				base := config.Broker.(*fakes.Memory)
				config.Broker = quarantineRemovalBroker{Broker: base, remove: func(ctx context.Context, entry async.QuarantineEntry) (bool, error) {
					removed, err := base.RemoveQuarantine(ctx, entry)
					if err != nil || !first {
						return removed, err
					}
					first = false
					switch failure {
					case "panic":
						panic("synthetic lost reply")
					case "cancel":
						cancel()
						return false, context.Canceled
					case "joined-invalid":
						return false, errors.Join(async.ErrInvalid, async.ErrUnavailable)
					case "joined-conflict":
						return false, errors.Join(async.ErrConflict, async.ErrUnavailable)
					case "wrapped-invalid":
						return false, fmt.Errorf("synthetic applied write: %w", async.ErrInvalid)
					case "wrapped-conflict":
						return false, fmt.Errorf("synthetic applied write: %w", async.ErrConflict)
					default:
						return false, errors.New("synthetic lost reply")
					}
				}}
			})
			entry := seedMemoryQuarantine(t, client, memory, signature)
			if outcome, err := (async.Control{Client: client}).RemoveQuarantine(ctx, entry); outcome != async.QuarantineRemovalUnknown || err == nil {
				t.Fatal("unknown mutation reported unchanged", outcome, err)
			}
			if outcome, err := (async.Control{Client: client}).RemoveQuarantine(context.Background(), entry); outcome != async.QuarantineRemovalAbsent || err != nil {
				t.Fatal("exact retry not idempotent", outcome, err)
			}
		})
	}
}

func TestMemoryQuarantineRemovalMatchesAllMetadataAndPreservesCursorPositions(t *testing.T) {
	client, memory, signature := producerFixture(t, func(config *async.ClientConfig) {
		config.Authorize = func(context.Context, string, string, string) error { return nil }
	})
	entries := []async.QuarantineEntry{}
	for range 3 {
		entries = append(entries, seedMemoryQuarantine(t, client, memory, signature))
	}
	for _, field := range []string{"id", "receipt", "reason", "digest", "time", "priority", "queue", "cursor"} {
		entry := entries[1]
		switch field {
		case "id":
			entry.Record.ID = "another-id"
		case "receipt":
			entry.Record.SourceReceipt = "another-receipt"
		case "reason":
			entry.Record.Reason = "unknown_task"
		case "digest":
			entry.Record.Digest = strings.Repeat("f", 64)
		case "time":
			entry.Record.FirstSeen = entry.Record.FirstSeen.Add(time.Second)
		case "priority":
			entry.Record.Priority = 1
		case "queue":
			entry.Record.Queue = "other"
		case "cursor":
			entry.Cursor = entries[0].Cursor
		}
		if removed, err := memory.RemoveQuarantine(context.Background(), entry); removed || err == nil {
			t.Fatal("metadata mismatch removed entry", field, removed, err)
		}
	}
	var workers sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		workers.Go(func() {
			removed, err := memory.RemoveQuarantine(context.Background(), entries[1])
			if err != nil {
				t.Error(err)
			}
			results <- removed
		})
	}
	workers.Wait()
	if (<-results) == (<-results) {
		t.Fatal("concurrent removal did not report one removed and one absent")
	}
	page, err := memory.ReadQuarantine(context.Background(), "default", entries[1].Cursor, 1000)
	if err != nil || len(page.Entries) != 1 || page.Entries[0] != entries[2] {
		t.Fatal("removal shifted cursor positions", page, err)
	}
	newEntry := seedMemoryQuarantine(t, client, memory, signature)
	if newEntry.Cursor == entries[1].Cursor || newEntry.Record.ID == entries[1].Record.ID {
		t.Fatal("removed identity recycled")
	}
	if removed, err := memory.RemoveQuarantine(context.Background(), entries[1]); removed || err != nil {
		t.Fatal("old removal affected new entry", removed, err)
	}
}

func TestQuarantineRemovalRejectsInvalidInputWithoutProviderCalls(t *testing.T) {
	calls := 0
	client, _, _ := producerFixture(t, func(config *async.ClientConfig) {
		config.Authorize = func(context.Context, string, string, string) error { calls++; return nil }
	})
	for _, field := range []string{"cursor", "queue", "id", "receipt", "reason", "digest", "time", "priority"} {
		entry := quarantinePage().Entries[0]
		switch field {
		case "cursor":
			entry.Cursor = ""
		case "queue":
			entry.Record.Queue = "*"
		case "id":
			entry.Record.ID = ""
		case "receipt":
			entry.Record.SourceReceipt = ""
		case "reason":
			entry.Record.Reason = "private unsupported error"
		case "digest":
			entry.Record.Digest = "bad"
		case "time":
			entry.Record.FirstSeen = time.Time{}
		case "priority":
			entry.Record.Priority = 10
		}
		if outcome, err := (async.Control{Client: client}).RemoveQuarantine(context.Background(), entry); outcome != async.QuarantineRemovalNotAttempted || !errors.Is(err, async.ErrInvalid) || calls != 0 {
			t.Fatal("invalid removal reached authority/provider", field, outcome, err)
		}
	}
}
