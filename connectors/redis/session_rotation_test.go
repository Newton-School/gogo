package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/sessions"
)

func rotationStore(t *testing.T) (*connector.Sessions, *connector.Connection) {
	t.Helper()
	cfg := fixture.Start(t)
	cfg.Role = connector.SessionRole
	connection, err := connector.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return &connector.Sessions{Connection: connection}, connection
}

func rotationRecord(t *testing.T, version uint64) sessions.Record {
	t.Helper()
	id, err := sessions.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return sessions.Record{ID: id, Version: version, ExpiresAt: time.Now().Add(time.Hour).UTC(), Data: map[string]json.RawMessage{"value": json.RawMessage(`{"z":9007199254740993,"a":18446744073709551615}`)}}
}

func TestRealRedisConditionalSessionDeleteExactVersionAndTombstone(t *testing.T) {
	ctx := context.Background()
	store, connection := rotationStore(t)
	for _, version := range []uint64{1, 9007199254740993, math.MaxUint64} {
		t.Run(strconv.FormatUint(version, 10), func(t *testing.T) {
			record := rotationRecord(t, version)
			if err := store.Create(ctx, record); err != nil {
				t.Fatal(err)
			}
			key := connection.Namespace() + ":session:{" + connector.Digest(record.ID) + "}"
			before, err := connection.Client().HGetAll(ctx, key).Result()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.DeleteIfVersion(ctx, record.ID, version-1); !errors.Is(err, sessions.ErrConflict) {
				t.Fatal("adjacent version revoked session", err)
			}
			after, err := connection.Client().HGetAll(ctx, key).Result()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("conflict changed stored version/payload", err)
			}
			if err := store.DeleteIfVersion(ctx, record.ID, version); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(ctx, record.ID); !errors.Is(err, sessions.ErrNotFound) {
				t.Fatal(err)
			}
			deleted, err := connection.Client().HGetAll(ctx, key).Result()
			if err != nil || deleted["version"] != "tombstone" || deleted["deleted"] != "1" || deleted["payload"] != "" {
				t.Fatal("invalid tombstone", err)
			}
			if err := store.DeleteIfVersion(ctx, record.ID, version); !errors.Is(err, sessions.ErrConflict) {
				t.Fatal("duplicate conditional deletion succeeded", err)
			}
			if err := store.Create(ctx, record); !errors.Is(err, sessions.ErrConflict) {
				t.Fatal("old identity resurrected", err)
			}
			if version != math.MaxUint64 {
				stale := sessions.Clone(record)
				stale.Version++
				if err := store.Save(ctx, stale, version); !errors.Is(err, sessions.ErrConflict) {
					t.Fatal("stale writer resurrected tombstone", err)
				}
			}
			if ttl := connection.Client().PTTL(ctx, key).Val(); ttl < 4*time.Minute || ttl > 5*time.Minute {
				t.Fatal("tombstone TTL", ttl)
			}
		})
	}
}

func TestRealRedisConditionalSessionDeleteRejectsMissingExpiredAndInvalidID(t *testing.T) {
	ctx := context.Background()
	store, connection := rotationStore(t)
	record := rotationRecord(t, 1)
	if err := store.DeleteIfVersion(ctx, record.ID, 1); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal(err)
	}
	key := connection.Namespace() + ":session:{" + connector.Digest(record.ID) + "}"
	if connection.Client().Exists(ctx, key).Val() != 0 {
		t.Fatal("missing deletion created tombstone")
	}
	if err := store.DeleteIfVersion(ctx, "bad-id", 1); !errors.Is(err, connector.ErrInvalid) {
		t.Fatal(err)
	}
	if err := store.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	// Retain the fixture key while its authoritative logical expiry is past.
	if err := connection.Client().HSet(ctx, key, "expiry", 1).Err(); err != nil {
		t.Fatal(err)
	}
	before, _ := connection.Client().HGetAll(ctx, key).Result()
	if err := store.DeleteIfVersion(ctx, record.ID, 1); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal(err)
	}
	after, _ := connection.Client().HGetAll(ctx, key).Result()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("expired rejection changed payload")
	}
}

func TestRealRedisConditionalSessionDeleteRacesCompetingSave(t *testing.T) {
	ctx := context.Background()
	store, _ := rotationStore(t)
	for range 30 {
		record := rotationRecord(t, 9007199254740993)
		if err := store.Create(ctx, record); err != nil {
			t.Fatal(err)
		}
		updated := sessions.Clone(record)
		updated.Version++
		updated.Data["winner"] = json.RawMessage(`true`)
		var saveErr, deleteErr error
		var wg sync.WaitGroup
		wg.Go(func() { saveErr = store.Save(ctx, updated, record.Version) })
		wg.Go(func() { deleteErr = store.DeleteIfVersion(ctx, record.ID, record.Version) })
		wg.Wait()
		if (saveErr == nil) == (deleteErr == nil) {
			t.Fatal("CAS operations both won/lost", saveErr, deleteErr)
		}
		loaded, err := store.Load(ctx, record.ID)
		if saveErr == nil {
			if !errors.Is(deleteErr, sessions.ErrConflict) || err != nil || loaded.Version != updated.Version || string(loaded.Data["winner"]) != "true" {
				t.Fatal("stale rotation overwrote newer save", deleteErr, loaded, err)
			}
		} else if !errors.Is(saveErr, sessions.ErrConflict) || !errors.Is(err, sessions.ErrNotFound) {
			t.Fatal("stale save resurrected rotation", saveErr, err)
		}
	}
}
