package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/sessions"
)

func TestPostgresSessionConditionalRevocation(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	store, err := postgres.NewSessions(postgres.SessionConfig{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: sessions.Migrations()}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	newRecord := func(version uint64) sessions.Record {
		id, err := sessions.NewID()
		if err != nil {
			t.Fatal(err)
		}
		return sessions.Record{ID: id, Version: version, ExpiresAt: time.Now().UTC().Add(time.Hour), Data: map[string]json.RawMessage{"data": json.RawMessage(`"preserved"`)}}
	}
	for _, version := range []uint64{1, 9007199254740993, math.MaxInt64} {
		record := newRecord(version)
		if err := store.Create(ctx, record); err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteIfVersion(ctx, record.ID, version+1); version < math.MaxInt64 && !errors.Is(err, sessions.ErrConflict) || version == math.MaxInt64 && !errors.Is(err, postgres.ErrSessionRecord) {
			t.Fatal("wrong version accepted", version, err)
		}
		loaded, err := store.Load(ctx, record.ID)
		if err != nil || loaded.Version != version || string(loaded.Data["data"]) != `"preserved"` {
			t.Fatal("rejected revoke changed data", err)
		}
		if err := store.DeleteIfVersion(ctx, record.ID, version); err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteIfVersion(ctx, record.ID, version); !errors.Is(err, sessions.ErrConflict) {
			t.Fatal("duplicate revoke accepted", err)
		}
		if _, err := store.Load(ctx, record.ID); !errors.Is(err, sessions.ErrNotFound) {
			t.Fatal(err)
		}
		if err := store.Create(ctx, record); !errors.Is(err, sessions.ErrConflict) {
			t.Fatal("revoked identity recreated", err)
		}
	}
	missing := newRecord(1)
	if err := store.DeleteIfVersion(ctx, missing.ID, 1); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal(err)
	}
	if err := store.Create(ctx, missing); err != nil {
		t.Fatal("conditional miss inserted tombstone", err)
	}
	digest := sha256.Sum256([]byte(missing.ID))
	if _, err := backend.Exec(ctx, "UPDATE gogo_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE key_digest=$1", hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIfVersion(ctx, missing.ID, 1); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal("expired identity revoked as live", err)
	}
	for range 8 {
		record := newRecord(1)
		if err := store.Create(ctx, record); err != nil {
			t.Fatal(err)
		}
		next := sessions.Clone(record)
		next.Version = 2
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-start; results <- store.Save(ctx, next, 1) }()
		go func() { <-start; results <- store.DeleteIfVersion(ctx, record.ID, 1) }()
		close(start)
		wins := 0
		for range 2 {
			err := <-results
			if err == nil {
				wins++
			} else if !errors.Is(err, sessions.ErrConflict) {
				t.Fatal(err)
			}
		}
		if wins != 1 {
			t.Fatal("conditional save/revoke had multiple winners", wins)
		}
	}
	if err := db.Atomic(ctx, backend, db.AtomicOptions{}, func(ctx context.Context) error {
		if err := store.DeleteIfVersion(ctx, missing.ID, 1); err == nil {
			t.Fatal("ambient transaction accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSessionStoreCASRevocationAndCleanup(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	store, err := postgres.NewSessions(postgres.SessionConfig{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	newRecord := func() sessions.Record {
		id, err := sessions.NewID()
		if err != nil {
			t.Fatal(err)
		}
		return sessions.Record{ID: id, Version: 1, ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond), BrowserClose: true, Data: map[string]json.RawMessage{"precise": json.RawMessage(`{"z":9007199254740993,"a":18446744073709551615,"scientific":1e100000,"decimal":1.2300}`)}}
	}
	r := newRecord()
	if err := store.Create(ctx, r); err == nil {
		t.Fatal("session construction silently created schema")
	}
	runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: sessions.Migrations()}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal("repeat migrate", err)
	}
	if err := store.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, r); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal("duplicate identity accepted", err)
	}
	loaded, err := store.Load(ctx, r.ID)
	if err != nil || loaded.ID != r.ID || loaded.Version != 1 || !loaded.BrowserClose || string(loaded.Data["precise"]) != string(r.Data["precise"]) || !loaded.ExpiresAt.Equal(r.ExpiresAt) {
		t.Fatal("session round trip", err, loaded.Version)
	}
	digest := sha256.Sum256([]byte(r.ID))
	key := hex.EncodeToString(digest[:])
	var payload string
	if err := db.QueryRow(ctx, backend, "SELECT payload::text FROM gogo_sessions WHERE key_digest=$1", []any{key}, &payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, r.ID) {
		t.Fatal("recoverable bearer stored")
	}
	// A reused stale version has exactly one winner under actual concurrent SQL.
	const contenders = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, contenders)
	for i := range contenders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			next := sessions.Clone(loaded)
			next.Version = 2
			next.Data["writer"], _ = json.Marshal(i)
			results <- store.Save(ctx, next, 1)
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, sessions.ErrConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatal("CAS winner count", winners)
	}
	current, err := store.Load(ctx, r.ID)
	if err != nil || current.Version != 2 {
		t.Fatal(err, current.Version)
	}
	if err := store.Delete(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, r.ID); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("revoked identity loaded", err)
	}
	current.Version++
	if err := store.Save(ctx, current, 2); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal("late write resurrected revoked session", err)
	}
	if err := store.Create(ctx, r); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal("create bypassed tombstone", err)
	}
	if err := store.Delete(ctx, r.ID); err != nil {
		t.Fatal("repeat logout", err)
	}
	if err := db.QueryRow(ctx, backend, "SELECT payload::text FROM gogo_sessions WHERE key_digest=$1", []any{key}, &payload); err != nil || payload != "{}" {
		t.Fatal("logout retained private payload", err, payload)
	}
	if n, err := store.ClearExpired(ctx, 10); err != nil || n != 0 {
		t.Fatal("retained tombstone prematurely cleared", n, err)
	}
	unknown := newRecord()
	if err := store.Delete(ctx, unknown.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, unknown); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal("in-flight create revived unknown logout", err)
	}
	// Database clock, not a caller clock, owns expiry. Fixtures move exact rows
	// into the past without timing sleeps or touching application databases.
	expired := newRecord()
	if err := store.Create(ctx, expired); err != nil {
		t.Fatal(err)
	}
	expiredDigest := sha256.Sum256([]byte(expired.ID))
	expiredKey := hex.EncodeToString(expiredDigest[:])
	if _, err := backend.Exec(ctx, "UPDATE gogo_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE key_digest=$1", expiredKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, expired.ID); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("expired session loaded", err)
	}
	expired.Version = 2
	if err := store.Save(ctx, expired, 1); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal("expired session extended after expiry", err)
	}
	if _, err := backend.Exec(ctx, "UPDATE gogo_sessions SET expires_at=clock_timestamp()-interval '1 second',tombstone_until=clock_timestamp()-interval '1 second' WHERE key_digest=$1", key); err != nil {
		t.Fatal(err)
	}
	if n, err := store.ClearExpired(ctx, 1); err != nil || n != 1 {
		t.Fatal("cleanup batch one", n, err)
	}
	if n, err := store.ClearExpired(ctx, 1); err != nil || n != 1 {
		t.Fatal("cleanup batch two", n, err)
	}
	if n, err := store.ClearExpired(ctx, 1); err != nil || n != 0 {
		t.Fatal("cleanup deleted unexpired tombstone", n, err)
	}
	if err := store.Save(ctx, current, 2); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal("purged session resurrected", err)
	}
	maxVersion := newRecord()
	maxVersion.Version = math.MaxInt64
	if err := store.Create(ctx, maxVersion); err != nil {
		t.Fatal(err)
	}
	maxVersion.Version++
	if err := store.Save(ctx, maxVersion, math.MaxInt64); !errors.Is(err, postgres.ErrSessionRecord) {
		t.Fatal("version overflow accepted", err)
	}
	oldVersion, err := store.Load(ctx, maxVersion.ID)
	if err != nil || oldVersion.Version != math.MaxInt64 {
		t.Fatal("overflow changed persisted version", err)
	}
	tooLarge := newRecord()
	tooLarge.Data["large"], _ = json.Marshal(strings.Repeat("x", 64<<10))
	if err := store.Create(ctx, tooLarge); !errors.Is(err, postgres.ErrSessionRecord) {
		t.Fatal("unbounded payload accepted", err)
	}
	if _, err := store.Load(ctx, "not-a-canonical-bearer"); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("malformed bearer lookup", err)
	}
	// A provider call cannot claim durability inside an uncommitted transaction.
	if err := db.Atomic(ctx, backend, db.AtomicOptions{}, func(txctx context.Context) error {
		if err := store.Create(txctx, newRecord()); err == nil {
			t.Fatal("published session before transaction commit")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
