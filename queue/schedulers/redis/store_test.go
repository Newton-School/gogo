package redis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/queue"
)

func TestRedisScheduleStoreKeys(t *testing.T) {
	store := NewStore(Config{Prefix: "gogo"})
	keys := store.Keys()
	if keys.Entries != "gogo:schedule:entries" {
		t.Fatalf("entries key = %q", keys.Entries)
	}
	if got := keys.Lock("daily-sync"); got != "gogo:schedule:locks:daily-sync" {
		t.Fatalf("lock key = %q", got)
	}
}

func TestRedisScheduleStoreSavesListsAndLocksAcrossClients(t *testing.T) {
	if os.Getenv("GOGO_TEST_REDIS_ADDR") == "" {
		t.Skip("set GOGO_TEST_REDIS_ADDR to run Redis schedule store tests")
	}
	ctx := context.Background()
	prefix := redisTestPrefix(t)
	first := NewStore(Config{URL: redisTestURL(), Prefix: prefix})
	defer first.Close()
	second := NewStore(Config{URL: redisTestURL(), Prefix: prefix})
	defer second.Close()

	lastRun := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	entry := queue.ScheduleEntry{
		Name:      "daily-sync",
		Signature: queue.NewSignature("legacy.sync", "tenant-1").WithQueue("critical"),
		Schedule:  queue.IntervalSchedule{Every: time.Minute, StartAt: lastRun},
		Enabled:   true,
		LastRunAt: &lastRun,
		Send:      queue.SendOptions{ID: "scheduled-task"},
	}
	if err := first.Save(ctx, entry); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	entries, err := second.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "daily-sync" || entries[0].Signature.Name != "legacy.sync" {
		t.Fatalf("entries = %#v", entries)
	}
	if schedule, ok := entries[0].Schedule.(queue.IntervalSchedule); !ok || schedule.Every != time.Minute || !schedule.StartAt.Equal(lastRun) {
		t.Fatalf("schedule = %#v", entries[0].Schedule)
	}
	if entries[0].Send.ID != "scheduled-task" {
		t.Fatalf("send options = %#v", entries[0].Send)
	}

	lock, err := first.Lock(ctx, "daily-sync", time.Minute)
	if err != nil {
		t.Fatalf("first Lock() error = %v", err)
	}
	if _, err := second.Lock(ctx, "daily-sync", time.Minute); !errors.Is(err, queue.ErrScheduleLocked) {
		t.Fatalf("second Lock() error = %v, want ErrScheduleLocked", err)
	}
	if err := lock.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	secondLock, err := second.Lock(ctx, "daily-sync", time.Minute)
	if err != nil {
		t.Fatalf("second Lock after release error = %v", err)
	}
	if err := secondLock.Release(ctx); err != nil {
		t.Fatalf("second Release() error = %v", err)
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
