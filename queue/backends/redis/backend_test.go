package redis

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/queue"
)

func TestRedisBackendKeysAndResultBehavior(t *testing.T) {
	backend := NewBackend(Config{Prefix: "gogo"})
	keys := backend.Keys("task-1")
	if keys.Result != "gogo:result:task-1" || keys.Children != "gogo:result:task-1:children" || keys.Group != "gogo:group:task-1" || keys.Chord != "gogo:chord:task-1" {
		t.Fatalf("keys = %#v", keys)
	}
}

func TestRedisBackendResultBehavior(t *testing.T) {
	if os.Getenv("GOGO_TEST_REDIS_ADDR") == "" {
		t.Skip("set GOGO_TEST_REDIS_ADDR to run Redis backend behavior tests")
	}
	backend := NewBackend(Config{URL: redisTestURL(), Prefix: redisTestPrefix(t)})
	defer backend.Close()
	result := queue.Result{TaskID: "task-1", State: queue.StateSuccess, Result: "ok", Traceback: "trace", Children: []string{"child"}}
	if err := backend.StoreResult(context.Background(), result); err != nil {
		t.Fatalf("StoreResult() error = %v", err)
	}
	stored, err := backend.GetResult(context.Background(), "task-1")
	if err != nil || stored.State != queue.StateSuccess || stored.Result != "ok" || stored.Traceback != "trace" {
		t.Fatalf("GetResult() = %#v, %v", stored, err)
	}
	count, err := backend.ChordCounter(context.Background(), "chord-1", 2)
	if err != nil || count != 2 {
		t.Fatalf("ChordCounter() = %d, %v", count, err)
	}
}

func TestRedisBackendWaitAcrossClients(t *testing.T) {
	if os.Getenv("GOGO_TEST_REDIS_ADDR") == "" {
		t.Skip("set GOGO_TEST_REDIS_ADDR to run Redis backend wait tests")
	}
	prefix := redisTestPrefix(t)
	first := NewBackend(Config{URL: redisTestURL(), Prefix: prefix})
	defer first.Close()
	second := NewBackend(Config{URL: redisTestURL(), Prefix: prefix})
	defer second.Close()

	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = first.StoreResult(context.Background(), queue.Result{TaskID: "task-wait", State: queue.StateSuccess, Result: "done"})
	}()
	result, err := second.Wait(context.Background(), "task-wait", time.Second)
	if err != nil || result.Result != "done" {
		t.Fatalf("Wait() = %#v, %v", result, err)
	}
}

func TestRedisBackendResultExpiry(t *testing.T) {
	if os.Getenv("GOGO_TEST_REDIS_ADDR") == "" {
		t.Skip("set GOGO_TEST_REDIS_ADDR to run Redis backend expiry tests")
	}
	now := time.Date(2026, 6, 28, 10, 30, 0, 0, time.UTC)
	backend := NewBackend(Config{URL: redisTestURL(), Prefix: redisTestPrefix(t), Now: func() time.Time { return now }})
	defer backend.Close()
	expires := now.Add(time.Minute)
	if err := backend.StoreResult(context.Background(), queue.Result{TaskID: "task-1", State: queue.StateSuccess, ExpiresAt: &expires}); err != nil {
		t.Fatalf("StoreResult() error = %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := backend.GetResult(context.Background(), "task-1"); err == nil {
		t.Fatalf("expired result should fail")
	}
}

func redisTestURL() string {
	value := strings.TrimSpace(os.Getenv("GOGO_TEST_REDIS_ADDR"))
	if strings.Contains(value, "://") {
		return value
	}
	return "redis://" + value + "/0"
}

func redisTestPrefix(t *testing.T) string {
	t.Helper()
	name := strings.NewReplacer("/", ":", " ", ":", "-", ":").Replace(t.Name())
	return fmt.Sprintf("gogo:test:%s:%d", name, time.Now().UnixNano())
}
