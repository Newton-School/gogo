package async_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
)

type eventReaderFunc func(context.Context, string, string, int) (async.EventPage, error)

func (f eventReaderFunc) ReadEvents(ctx context.Context, scope, after string, limit int) (async.EventPage, error) {
	return f(ctx, scope, after, limit)
}

func eventControl(t *testing.T, policy func(context.Context, string, string, string) error) async.Control {
	t.Helper()
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: backend, Results: backend, Authorize: policy})
	if err != nil {
		t.Fatal(err)
	}
	return async.Control{Client: client}
}

func monitorEntry(n int) async.EventEntry {
	return async.EventEntry{Cursor: fmt.Sprint(n), Event: async.Event{Kind: "task_started", TaskID: async.StableID("monitor", fmt.Sprint(n)), Task: "monitor.task", WorkerID: "worker", Scope: "tenant", State: async.Running, At: time.Date(2026, 1, 2, 3, 4, n, 0, time.UTC)}}
}

func monitorPage() async.EventPage {
	return async.EventPage{Entries: []async.EventEntry{monitorEntry(1), monitorEntry(2), monitorEntry(3)}, NextCursor: "3"}
}

func TestEventReadRequiresScopeAndCurrentObjectAuthority(t *testing.T) {
	ctx := context.Background()
	reads := 0
	source := eventReaderFunc(func(_ context.Context, scope, after string, limit int) (async.EventPage, error) {
		reads++
		if scope != "tenant" || after != "" || limit != 3 {
			t.Fatal("unbounded or cross-scope provider request")
		}
		return monitorPage(), nil
	})
	for _, policy := range []func(context.Context, string, string, string) error{nil, func(context.Context, string, string, string) error { return async.ErrDenied }} {
		page, err := eventControl(t, policy).ReadEvents(ctx, source, "tenant", "", 3)
		if !errors.Is(err, async.ErrDenied) || reads != 0 || len(page.Entries) != 0 || page.NextCursor != "" {
			t.Fatal(page, err, reads)
		}
	}
	var actions []string
	control := eventControl(t, func(_ context.Context, action, scope, id string) error {
		if scope != "tenant" {
			t.Fatal("policy scope changed")
		}
		actions = append(actions, action)
		if id == monitorEntry(2).Event.TaskID {
			return fmt.Errorf("private denial explanation: %w", async.ErrDenied)
		}
		return nil
	})
	page, err := control.ReadEvents(ctx, source, "tenant", "", 3)
	if err != nil || len(page.Entries) != 2 || page.Entries[0].Cursor != "1" || page.Entries[1].Cursor != "3" || page.NextCursor != "3" {
		t.Fatal(page, err)
	}
	if !reflect.DeepEqual(actions, []string{"inspect_events", "inspect_events", "inspect", "inspect", "inspect", "inspect_events"}) {
		t.Fatal(actions)
	}
}

func TestEventReadDropsProviderDataOnFailureAndMalformedPage(t *testing.T) {
	control := eventControl(t, func(context.Context, string, string, string) error { return nil })
	for _, scenario := range []string{"outage", "panic", "wrong_scope", "unknown_kind", "invalid_state", "duplicate_cursor", "invalid_cursor", "wrong_next", "stuck_next", "too_many"} {
		t.Run(scenario, func(t *testing.T) {
			source := eventReaderFunc(func(context.Context, string, string, int) (async.EventPage, error) {
				page := monitorPage()
				switch scenario {
				case "outage":
					return page, errors.New("private backend URL")
				case "panic":
					panic("private provider detail")
				case "wrong_scope":
					page.Entries[2].Event.Scope = "another-tenant"
				case "unknown_kind":
					page.Entries[2].Event.Kind = "unvalidated"
				case "invalid_state":
					page.Entries[2].Event.State = async.Succeeded
				case "duplicate_cursor":
					page.Entries[2].Cursor = page.Entries[0].Cursor
				case "invalid_cursor":
					page.Entries[2].Cursor = strings.Repeat("a", 257)
				case "wrong_next":
					page.NextCursor = "4"
				case "stuck_next":
					page.Entries = nil
				case "too_many":
					page.Entries = append(page.Entries, monitorEntry(4))
				}
				return page, nil
			})
			page, err := control.ReadEvents(context.Background(), source, "tenant", "", 3)
			if err != async.ErrUnavailable || len(page.Entries) != 0 || page.NextCursor != "" {
				t.Fatal("unsafe partial monitoring response", page, err)
			}
		})
	}
}

func TestEventReadAuthorizationOutagesCancellationAndDenialAreDistinct(t *testing.T) {
	for _, target := range []string{"stream", "event"} {
		for _, result := range []string{"outage", "panic", "cancel", "denied"} {
			t.Run(target+"/"+result, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				control := eventControl(t, func(_ context.Context, action, _, _ string) error {
					if target == "stream" && action != "inspect_events" || target == "event" && action != "inspect" {
						return nil
					}
					switch result {
					case "panic":
						panic("private policy detail")
					case "cancel":
						cancel()
						return nil
					case "denied":
						return fmt.Errorf("private denial: %w", async.ErrDenied)
					default:
						return errors.New("private policy backend")
					}
				})
				source := eventReaderFunc(func(context.Context, string, string, int) (async.EventPage, error) { return monitorPage(), nil })
				page, err := control.ReadEvents(ctx, source, "tenant", "", 3)
				expected := async.ErrUnavailable
				if result == "cancel" {
					expected = context.Canceled
				} else if result == "denied" {
					if target == "event" {
						if err != nil || len(page.Entries) != 0 || page.NextCursor != "3" {
							t.Fatal("explicit object denial was not hidden", page, err)
						}
						return
					}
					expected = async.ErrDenied
				}
				if err != expected || len(page.Entries) != 0 || page.NextCursor != "" {
					t.Fatal(page, err)
				}
			})
		}
	}
}

func TestEventStreamReauthorizesBeforeEachYieldAndStopsOnBreak(t *testing.T) {
	denied := ""
	reads := 0
	control := eventControl(t, func(_ context.Context, action, _, id string) error {
		if action == "inspect" && id == denied {
			return async.ErrDenied
		}
		return nil
	})
	source := eventReaderFunc(func(context.Context, string, string, int) (async.EventPage, error) {
		reads++
		return monitorPage(), nil
	})
	var cursors []string
	for entry, err := range control.ObserveEvents(context.Background(), source, "tenant", async.EventStreamOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		cursors = append(cursors, entry.Cursor)
		if entry.Cursor == "1" {
			denied = monitorEntry(2).Event.TaskID
		} else {
			break
		}
	}
	if !reflect.DeepEqual(cursors, []string{"1", "3"}) || reads != 1 {
		t.Fatal(cursors, reads)
	}
}

func TestEventStreamRevocationOutageAndCancellationStopWithoutAnotherEvent(t *testing.T) {
	for _, stop := range []string{"revoke_stream", "policy_outage", "cancel"} {
		t.Run(stop, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopped := false
			control := eventControl(t, func(context.Context, string, string, string) error {
				if stopped {
					if stop == "revoke_stream" {
						return async.ErrDenied
					}
					if stop == "policy_outage" {
						return errors.New("private failure")
					}
				}
				return nil
			})
			source := eventReaderFunc(func(context.Context, string, string, int) (async.EventPage, error) { return monitorPage(), nil })
			yielded := 0
			var final error
			for entry, err := range control.ObserveEvents(ctx, source, "tenant", async.EventStreamOptions{}) {
				if err != nil {
					if entry.Cursor != "" || entry.Event.TaskID != "" {
						t.Fatal("error yield exposed event")
					}
					final = err
					break
				}
				yielded++
				stopped = true
				if stop == "cancel" {
					cancel()
				}
			}
			expected := async.ErrDenied
			if stop == "policy_outage" {
				expected = async.ErrUnavailable
			} else if stop == "cancel" {
				expected = context.Canceled
			}
			if yielded != 1 || final != expected {
				t.Fatal(yielded, final)
			}
		})
	}
}

func TestEventStreamEmptyPollIsCancelableAndConsumerPanicPropagates(t *testing.T) {
	control := eventControl(t, func(context.Context, string, string, string) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reads := 0
	read := make(chan struct{})
	go func() {
		select {
		case <-read:
			cancel()
		case <-ctx.Done():
		}
	}()
	source := eventReaderFunc(func(_ context.Context, _, after string, _ int) (async.EventPage, error) {
		reads++
		if reads == 1 {
			close(read)
		}
		return async.EventPage{NextCursor: after}, nil
	})
	for entry, err := range control.ObserveEvents(ctx, source, "tenant", async.EventStreamOptions{}) {
		if err != context.Canceled || entry.Cursor != "" {
			t.Fatal(entry, err)
		}
	}
	if reads != 1 {
		t.Fatal("empty stream busy-polled", reads)
	}
	defer func() {
		if recover() != "consumer panic" {
			t.Fatal("iterator recovered and re-entered a panicking consumer")
		}
	}()
	populated := eventReaderFunc(func(context.Context, string, string, int) (async.EventPage, error) { return monitorPage(), nil })
	for range control.ObserveEvents(context.Background(), populated, "tenant", async.EventStreamOptions{}) {
		panic("consumer panic")
	}
}
