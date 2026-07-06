package sessions

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Newton-School/gogo/cache"
)

func TestSessionStoresCreateLoadCycleFlushAndExpire(t *testing.T) {
	ctx := context.Background()
	secret := "session-secret"
	stores := map[string]Store{
		"database":        NewDatabaseStore(secret),
		"cached_database": NewCachedDatabaseStore(NewDatabaseStore(secret), NewCacheStore(cache.NewMemoryStore(), secret)),
		"cache":           NewCacheStore(cache.NewMemoryStore(), secret),
		"file":            NewFileStore(filepath.Join(t.TempDir(), "sessions"), secret),
		"signed_cookie":   NewSignedCookieStore(secret),
	}

	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			session := NewSession(30 * time.Minute)
			session.Set("user_id", "42")
			if !session.Modified {
				t.Fatalf("Set should mark session modified")
			}
			if err := store.Save(ctx, session); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			if session.Key == "" || !VerifySessionKey(secret, session.Key) && name != "signed_cookie" {
				t.Fatalf("session key was not signed: %q", session.Key)
			}

			loaded, ok, err := store.Load(ctx, session.Key)
			if err != nil || !ok {
				t.Fatalf("Load() = %#v, %v, %v", loaded, ok, err)
			}
			if got, ok := loaded.Get("user_id"); !ok || got != "42" || !loaded.Accessed {
				t.Fatalf("loaded user_id/accessed = %q, %v, accessed:%v", got, ok, loaded.Accessed)
			}

			oldKey := loaded.Key
			if err := store.CycleKey(ctx, loaded); err != nil {
				t.Fatalf("CycleKey() error = %v", err)
			}
			if loaded.Key == oldKey || loaded.GetString("user_id") != "42" {
				t.Fatalf("cycled session = %#v, old key %q", loaded, oldKey)
			}
			if _, ok, err := store.Load(ctx, oldKey); err != nil || ok {
				t.Fatalf("old key load after cycle = %v, %v", ok, err)
			}

			if err := store.Flush(ctx, loaded); err != nil {
				t.Fatalf("Flush() error = %v", err)
			}
			if loaded.Key == "" || len(loaded.Data) != 0 {
				t.Fatalf("flushed session = %#v", loaded)
			}

			expired := NewSession(time.Minute)
			expired.Set("state", "expired")
			expired.ExpireDate = time.Now().Add(-time.Second)
			if err := store.Save(ctx, expired); err != nil {
				t.Fatalf("Save(expired) error = %v", err)
			}
			if _, ok, err := store.Load(ctx, expired.Key); err != nil || ok {
				t.Fatalf("Load(expired) = %v, %v", ok, err)
			}
		})
	}
}

func TestSignedCookieStoreRejectsTampering(t *testing.T) {
	store := NewSignedCookieStore("secret")
	session := NewSession(time.Hour)
	session.Set("role", "admin")
	if err := store.Save(context.Background(), session); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	tampered := session.Key[:len(session.Key)-1] + "x"
	loaded, ok, err := store.Load(context.Background(), tampered)
	if !errors.Is(err, ErrSessionTampered) || ok || loaded != nil {
		t.Fatalf("Load(tampered) = %#v, %v, %v", loaded, ok, err)
	}
}

func TestSessionExpiryAndDeleteBehavior(t *testing.T) {
	store := NewDatabaseStore("secret")
	session := NewSession(2 * time.Hour)
	session.Set("cart", "abc")
	if age := session.ExpiryAge(time.Now()); age <= 0 || age > 2*time.Hour {
		t.Fatalf("ExpiryAge() = %s", age)
	}
	if err := store.Save(context.Background(), session); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := store.Delete(context.Background(), session.Key); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, ok, err := store.Load(context.Background(), session.Key); err != nil || ok {
		t.Fatalf("Load(deleted) = %v, %v", ok, err)
	}
}
