package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Newton-School/gogo/core/security"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	fail    bool
}

func (m *memoryStore) Load(_ context.Context, id string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return Record{}, errors.New("private backend failure")
	}
	r, ok := m.records[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return Clone(r), nil
}
func (m *memoryStore) Create(_ context.Context, r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("private backend failure")
	}
	if m.records == nil {
		m.records = map[string]Record{}
	}
	if _, ok := m.records[r.ID]; ok {
		return ErrConflict
	}
	m.records[r.ID] = Clone(r)
	return nil
}
func (m *memoryStore) Save(_ context.Context, r Record, version uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.records[r.ID]
	if !ok || old.Version != version {
		return ErrConflict
	}
	m.records[r.ID] = Clone(r)
	return nil
}
func (m *memoryStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, id)
	return nil
}
func (m *memoryStore) DeleteIfVersion(_ context.Context, id string, expected uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("private backend failure")
	}
	current, ok := m.records[id]
	if !ok || current.Version != expected || !current.ExpiresAt.After(time.Now()) {
		return ErrConflict
	}
	delete(m.records, id)
	return nil
}

func TestRotationRejectsConcurrentWritesBeforeRevokingIdentity(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{}
	record := Record{ID: "existing", Version: 1, ExpiresAt: time.Now().Add(time.Hour), Data: map[string]json.RawMessage{"state": json.RawMessage(`"initial"`)}}
	if err := store.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	stale := New(record)
	if err := stale.CycleKey(); err != nil {
		t.Fatal(err)
	}
	newer := Clone(record)
	newer.Version = 2
	newer.Data["state"] = json.RawMessage(`"concurrent"`)
	if err := store.Save(ctx, newer, 1); err != nil {
		t.Fatal(err)
	}
	if err := stale.Persist(ctx, store, time.Now(), time.Hour); !errors.Is(err, ErrConflict) {
		t.Fatal("stale rotation accepted", err)
	}
	actual, err := store.Load(ctx, record.ID)
	if err != nil || actual.Version != 2 || string(actual.Data["state"]) != `"concurrent"` || len(store.records) != 1 {
		t.Fatal("newer session changed", actual, err)
	}
	current := New(actual)
	_ = current.CycleKey()
	if err := current.Persist(ctx, store, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("old key not revoked", err)
	}
	if current.Snapshot().ID == record.ID || current.Snapshot().Version != 1 {
		t.Fatal("new identity not created")
	}
}

type withoutVersionedDelete struct{ Store }

func TestRotationWithoutAtomicConditionalDeleteFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{}
	record := Record{ID: "existing", Version: 1, ExpiresAt: time.Now().Add(time.Hour)}
	_ = store.Create(ctx, record)
	session := New(record)
	_ = session.CycleKey()
	if err := session.Persist(ctx, withoutVersionedDelete{store}, time.Now(), time.Hour); !errors.Is(err, ErrRotationUnsupported) {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, record.ID); err != nil {
		t.Fatal("unsupported rotation deleted original", err)
	}
}
func TestVersionConflictAndClone(t *testing.T) {
	ctx := context.Background()
	m := &memoryStore{}
	r := Record{ID: "id", Version: 1, ExpiresAt: time.Now().Add(time.Hour), Data: map[string]json.RawMessage{"x": json.RawMessage(`1`)}}
	_ = m.Create(ctx, r)
	a := New(r)
	b := New(r)
	_ = a.Set("x", 2)
	_ = b.Set("x", 3)
	if e := a.Persist(ctx, m, time.Now(), time.Hour); e != nil {
		t.Fatal(e)
	}
	if e := b.Persist(ctx, m, time.Now(), time.Hour); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if string(r.Data["x"]) != "1" {
		t.Fatal("shared session state")
	}
	if e := a.Set("x", 4); !errors.Is(e, ErrCommitted) {
		t.Fatal(e)
	}
}

func TestVersionExhaustionDoesNotWrapOrWrite(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{}
	record := Record{ID: "fixture-version", Version: math.MaxUint64, ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	session := New(record)
	if err := session.Set("value", "changed"); err != nil {
		t.Fatal(err)
	}
	if err := session.Persist(ctx, store, time.Now(), time.Hour); !errors.Is(err, ErrConflict) {
		t.Fatal("version exhausted without conflict", err)
	}
	stored, err := store.Load(ctx, record.ID)
	if err != nil || stored.Version != math.MaxUint64 || len(stored.Data) != 0 || session.Snapshot().Version != math.MaxUint64 {
		t.Fatal("exhaustion changed session", err)
	}
}
func TestPersistenceBeforeHeadersAndFailureRedaction(t *testing.T) {
	signer, _ := security.NewSigner(security.SigningKey{ID: "test", Value: []byte(strings.Repeat("a", 32))}, nil, "sessions")
	for _, fail := range []bool{false, true} {
		store := &memoryStore{fail: fail}
		mw, _ := Middleware(MiddlewareConfig{Store: store, Signer: signer})
		h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s, _ := FromContext(r.Context())
			_ = s.Set("user", "1")
			_, _ = w.Write([]byte("success"))
			if s.Set("late", 1) != ErrCommitted {
				t.Error("late mutation allowed")
			}
		}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "http://localhost", nil))
		if fail {
			if w.Code != 503 || len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "success") {
				t.Fatal(w.Code, w.Body)
			}
		} else if w.Code != 200 || len(w.Result().Cookies()) != 1 || !strings.Contains(w.Header().Get("Vary"), "Cookie") || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal(w.Code)
		}
	}
}
func TestRotationRevokesOldIdentity(t *testing.T) {
	ctx := context.Background()
	m := &memoryStore{}
	r := Record{ID: "old", Version: 1, ExpiresAt: time.Now().Add(time.Hour)}
	_ = m.Create(ctx, r)
	s := New(r)
	_ = s.CycleKey()
	if err := s.Persist(ctx, m, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, e := m.Load(ctx, "old"); e != ErrNotFound {
		t.Fatal("old session survived")
	}
	if s.Snapshot().ID == "old" {
		t.Fatal("identity unchanged")
	}
}

func TestBrowserCloseSurvivesSubsequentMutation(t *testing.T) {
	store := &memoryStore{}
	signer, _ := security.NewSigner(security.SigningKey{ID: "test", Value: []byte(strings.Repeat("a", 32))}, nil, "sessions")
	mw, _ := Middleware(MiddlewareConfig{Store: store, Signer: signer})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, _ := FromContext(r.Context())
		if r.URL.Path == "/first" {
			_ = session.SetBrowserClose(true)
		}
		_ = session.Set("value", r.URL.Path)
		w.WriteHeader(200)
	}))
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest("GET", "http://localhost/first", nil))
	cookie := first.Result().Cookies()[0]
	second := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "http://localhost/second", nil)
	request.AddCookie(cookie)
	handler.ServeHTTP(second, request)
	if second.Code != 200 {
		t.Fatal(second.Code)
	}
	for _, responseCookie := range second.Result().Cookies() {
		if responseCookie.Name == "gogo_session" && (!responseCookie.Expires.IsZero() || responseCookie.MaxAge != 0) {
			t.Fatal("browser-close session became persistent")
		}
	}
}

func TestFlushThenWriteCreatesFreshIdentityAndCookie(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{}
	old := Record{ID: "previous-account", Version: 8, ExpiresAt: time.Now().Add(time.Minute), BrowserClose: true, Data: map[string]json.RawMessage{"private": json.RawMessage(`"old-secret"`)}}
	if err := store.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	signer, _ := security.NewSigner(security.SigningKey{ID: "test", Value: []byte(strings.Repeat("a", 32))}, nil, "sessions")
	token, _ := signer.Sign([]byte(old.ID))
	mw, _ := Middleware(MiddlewareConfig{Store: store, Signer: signer, TTL: time.Hour})
	r := httptest.NewRequest("POST", "http://localhost/", nil)
	r.AddCookie(&http.Cookie{Name: "gogo_session", Value: token})
	w := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, _ := FromContext(r.Context())
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := s.Set("notice", "Signed out"); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(200)
	})).ServeHTTP(w, r)
	if _, err := store.Load(ctx, old.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("flushed identity survived", err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge <= 0 {
		t.Fatal("new session cookie expired", cookies)
	}
	id, err := signer.Verify(cookies[0].Value, time.Hour)
	if err != nil || string(id) == old.ID {
		t.Fatal(err)
	}
	fresh, err := store.Load(ctx, string(id))
	if err != nil || fresh.Version != 1 || fresh.BrowserClose || fresh.ExpiresAt.Before(time.Now().Add(50*time.Minute)) {
		t.Fatal(fresh, err)
	}
	if _, ok := fresh.Data["private"]; ok {
		t.Fatal("previous account data survived")
	}
}

func TestFlushWithoutLaterWriteOnlyExpiresCookie(t *testing.T) {
	store := &memoryStore{}
	s := New(Record{ID: "old", Version: 1})
	_ = store.Create(context.Background(), s.Snapshot())
	_ = s.Flush()
	if err := s.Persist(context.Background(), store, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 0 || s.Snapshot().ID != "" {
		t.Fatal("logout created an unused replacement session")
	}
}
