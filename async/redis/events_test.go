package redis

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	fakes "github.com/Newton-School/gogo/async/testing"
	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	redigo "github.com/redis/go-redis/v9"
)

func redisMonitorEvent(scope, label string) async.Event {
	return async.Event{Kind: "task_started", TaskID: async.StableID("events", label), Task: "events.task", WorkerID: "worker", Scope: scope, State: async.Running, At: time.Now().UTC()}
}

func TestRealRedisEventsObserveWorkerLifecycleWithoutArgumentsOrResults(t *testing.T) {
	ctx := context.Background()
	config := fixture.Start(t)
	config.Role = connector.TaskRole
	connection, err := connector.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	events := &Events{Connection: connection}
	broker, results := &Broker{Connection: connection}, &Results{Connection: connection}
	registry := async.NewRegistry()
	task, err := async.Register(registry, "monitor.execute", 1, func(ctx context.Context, task async.TaskContext, _ string) (string, error) {
		if err := task.Progress(ctx, map[string]string{"private": "private-progress"}); err != nil {
			return "", err
		}
		return "private-result", nil
	}, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: broker, Results: results, Authorize: func(context.Context, string, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	signature, err := task.Signature("private-argument")
	if err != nil {
		t.Fatal(err)
	}
	signature.Options.Scope = "tenant"
	if _, err := client.Enqueue(ctx, signature); err != nil {
		t.Fatal(err)
	}
	delivery, err := broker.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "monitor-worker"})
	if err != nil {
		t.Fatal(err)
	}
	worker := &async.Worker{Registry: registry, Broker: broker, Results: results, ID: "monitor-worker", Events: events, OnError: func(err error) { t.Error(err) }}
	if err := worker.Process(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	page, err := (async.Control{Client: client}).ReadEvents(ctx, events, "tenant", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, entry := range page.Entries {
		kinds = append(kinds, entry.Event.Kind)
		if err := async.ValidateEvent(entry.Event); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(kinds, []string{"task_started", "task_terminal"}) {
		t.Fatal(kinds)
	}
	raw, err := connection.Client().XRange(ctx, events.key("tenant"), "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(raw)
	if err != nil || strings.Contains(string(encoded), "private-") {
		t.Fatal("event stream exposed execution payload", err)
	}
}

func TestRealRedisEventPagesEnforceScopeAndCurrentTaskPolicy(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	events := &Events{Connection: connection}
	one, hidden, other := redisMonitorEvent("one", "first"), redisMonitorEvent("one", "hidden"), redisMonitorEvent("two", "other")
	last := redisMonitorEvent("one", "last")
	last.Kind = "task_replaced"
	for _, event := range []async.Event{one, other, hidden, last} {
		if err := events.PublishEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	backend := fakes.NewMemory()
	client, err := async.NewClient(async.ClientConfig{Registry: async.NewRegistry(), Broker: backend, Results: backend, Authorize: func(_ context.Context, action, scope, id string) error {
		if scope != "one" || action == "inspect" && id == hidden.TaskID {
			return async.ErrDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	control := async.Control{Client: client}
	page, err := control.ReadEvents(ctx, events, "one", "", 2)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Event.TaskID != one.TaskID || page.NextCursor == page.Entries[0].Cursor {
		t.Fatal("hidden event was exposed or cursor did not advance", page, err)
	}
	next, err := control.ReadEvents(ctx, events, "one", page.NextCursor, 2)
	if err != nil || len(next.Entries) != 1 || next.Entries[0].Event.TaskID != last.TaskID || next.Entries[0].Event.Kind != "task_replaced" {
		t.Fatal("replacement event or continuation was lost", next, err)
	}
	if denied, err := control.ReadEvents(ctx, events, "two", "", 2); err != async.ErrDenied || len(denied.Entries) != 0 || denied.NextCursor != "" {
		t.Fatal("foreign scope disclosed", denied, err)
	}
	seen := 0
	for entry, err := range control.ObserveEvents(ctx, events, "one", async.EventStreamOptions{}) {
		if err != nil || entry.Event.Scope != "one" || entry.Event.TaskID == hidden.TaskID || !validStreamCursor(entry.Cursor) {
			t.Fatal(entry, err)
		}
		seen++
		if seen == 2 {
			break
		}
	}
	if seen != 2 {
		t.Fatal(seen)
	}
}

func TestRealRedisEventAdapterRejectsInvalidCursorsAndMalformedScopeData(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	events := &Events{Connection: connection}
	for _, cursor := range []string{"$", "-", "1", "01-0", "1--1", "18446744073709551616-0", "1-1\n", strings.Repeat("1", 257)} {
		if page, err := events.ReadEvents(ctx, "one", cursor, 10); err != async.ErrInvalid || len(page.Entries) != 0 {
			t.Fatal(cursor, page, err)
		}
	}
	for _, scenario := range []string{"foreign_scope", "bad_kind", "non_json", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			scope := "scope-" + scenario
			valid := redisMonitorEvent(scope, "valid")
			if err := events.PublishEvent(ctx, valid); err != nil {
				t.Fatal(err)
			}
			bad := redisMonitorEvent(scope, "bad")
			if scenario == "foreign_scope" {
				bad.Scope = "private-other-scope"
			} else if scenario == "bad_kind" {
				bad.Kind = "unknown"
			}
			raw, _ := json.Marshal(bad)
			if scenario == "non_json" {
				raw = []byte("not-json")
			} else if scenario == "oversized" {
				raw = []byte(strings.Repeat(" ", 16<<10+1))
			}
			if err := connection.Client().XAdd(ctx, &redigo.XAddArgs{Stream: events.key(scope), Values: map[string]any{"event": raw}}).Err(); err != nil {
				t.Fatal(err)
			}
			if page, err := events.ReadEvents(ctx, scope, "", 10); err != async.ErrUnavailable || len(page.Entries) != 0 || page.NextCursor != "" {
				t.Fatal("malformed later event leaked a partial page", page, err)
			}
		})
	}
	base := redisMonitorEvent("validation", "event")
	for _, change := range []func(*async.Event){func(e *async.Event) { e.TaskID = "invalid" }, func(e *async.Event) { e.State = async.Succeeded }, func(e *async.Event) { e.At = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }, func(e *async.Event) { e.Retries = -1 }} {
		event := base
		change(&event)
		if err := events.PublishEvent(ctx, event); err != async.ErrInvalid {
			t.Fatal(err)
		}
	}
	if size, err := connection.Client().XLen(ctx, events.key(base.Scope)).Result(); err != nil || size != 0 {
		t.Fatal("invalid event was persisted", size, err)
	}
	if page, err := events.ReadEvents(ctx, "empty", "0-0", 1); err != nil || len(page.Entries) != 0 || page.NextCursor != "0-0" {
		t.Fatal(page, err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := events.ReadEvents(ctx, "one", "", 1); err == nil || errors.Is(err, async.ErrNotFound) {
		t.Fatal("backend outage was treated as empty history", err)
	}
}
