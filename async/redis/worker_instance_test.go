package redis_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	adapter "github.com/Newton-School/gogo/async/redis"
)

func TestRealRedisWorkerInstanceIdentityIsImmutableUntilTakeover(t *testing.T) {
	ctx := context.Background()
	_, results := backends(t)
	store := &adapter.Workers{Connection: results.Connection}
	lease, snapshot := redisPresence(t, "instance")
	snapshot.InstanceID, _ = async.NewID()
	if err := store.ClaimWorker(ctx, lease, snapshot, time.Minute); err != nil {
		t.Fatal(err)
	}
	key := results.Connection.PartitionKey("worker", snapshot.ID, "presence")
	before, err := results.Connection.Client().HGetAll(ctx, key).Result()
	if err != nil || before["instance"] != snapshot.InstanceID {
		t.Fatal("instance not persisted", err)
	}
	changed := snapshot
	changed.InstanceID, _ = async.NewID()
	for _, operation := range []func() error{
		func() error { return store.ClaimWorker(ctx, lease, changed, time.Minute) },
		func() error { return store.RenewWorker(ctx, lease, changed, time.Minute) },
		func() error { return store.ReleaseWorker(ctx, lease, changed) },
	} {
		if err := operation(); !errors.Is(err, async.ErrConflict) {
			t.Fatal("same lease changed instance", err)
		}
		after, err := results.Connection.Client().HGetAll(ctx, key).Result()
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("rejected change mutated persisted presence", err)
		}
	}
	if err := store.ReleaseWorker(ctx, lease, snapshot); err != nil {
		t.Fatal(err)
	}
	other, _ := redisPresence(t, snapshot.ID)
	if err := store.ClaimWorker(ctx, other, snapshot, time.Minute); !errors.Is(err, async.ErrConflict) {
		t.Fatal("replacement owner reused instance", err)
	}
	if err := store.ClaimWorker(ctx, other, changed, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewWorker(ctx, lease, snapshot, time.Minute); !errors.Is(err, async.ErrLeaseLost) {
		t.Fatal("old instance renewed replacement", err)
	}
	presence, err := store.LookupWorker(ctx, snapshot.ID)
	if err != nil || presence.Snapshot.InstanceID != changed.InstanceID {
		t.Fatal("replacement identity lost", err)
	}
}
