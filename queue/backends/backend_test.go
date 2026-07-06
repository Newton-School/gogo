package backends

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Newton-School/gogo/queue"
)

func TestMemoryBackendResultStorageExpiryChildrenGroupAndChord(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 30, 0, 0, time.UTC)
	backend := NewMemoryBackend(MemoryOptions{Now: func() time.Time { return now }})
	expires := now.Add(time.Minute)
	result := queue.Result{
		TaskID:    "task-1",
		State:     queue.StateSuccess,
		Result:    "ok",
		Children:  []string{"child-1"},
		ExpiresAt: &expires,
	}
	if err := backend.StoreResult(context.Background(), result); err != nil {
		t.Fatalf("StoreResult() error = %v", err)
	}
	stored, err := backend.GetResult(context.Background(), "task-1")
	if err != nil || stored.State != queue.StateSuccess || stored.Result != "ok" {
		t.Fatalf("GetResult() = %#v, %v", stored, err)
	}
	children, err := backend.Children(context.Background(), "task-1")
	if err != nil || !reflect.DeepEqual(children, []string{"child-1"}) {
		t.Fatalf("Children() = %#v, %v", children, err)
	}
	group, err := backend.GroupResult(context.Background(), "group-1", []string{"task-1", "task-2"})
	if err != nil || group.ID != "group-1" || !reflect.DeepEqual(group.Children, []string{"task-1", "task-2"}) {
		t.Fatalf("GroupResult() = %#v, %v", group, err)
	}
	count, err := backend.ChordCounter(context.Background(), "chord-1", 1)
	if err != nil || count != 1 {
		t.Fatalf("ChordCounter(+1) = %d, %v", count, err)
	}
	count, err = backend.ChordCounter(context.Background(), "chord-1", 2)
	if err != nil || count != 3 {
		t.Fatalf("ChordCounter(+2) = %d, %v", count, err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := backend.GetResult(context.Background(), "task-1"); !errors.Is(err, ErrResultExpired) {
		t.Fatalf("GetResult(expired) error = %v, want ErrResultExpired", err)
	}
}

func TestMemoryBackendWaitAndForget(t *testing.T) {
	backend := NewMemoryBackend(MemoryOptions{})
	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = backend.StoreResult(context.Background(), queue.Result{TaskID: "task-2", State: queue.StateSuccess, Result: "done"})
	}()
	result, err := backend.Wait(context.Background(), "task-2", time.Second)
	if err != nil || result.Result != "done" {
		t.Fatalf("Wait() = %#v, %v", result, err)
	}
	if err := backend.Forget(context.Background(), "task-2"); err != nil {
		t.Fatalf("Forget() error = %v", err)
	}
	if _, err := backend.GetResult(context.Background(), "task-2"); !errors.Is(err, ErrResultNotFound) {
		t.Fatalf("GetResult(forgotten) error = %v, want ErrResultNotFound", err)
	}
}
