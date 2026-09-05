package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Newton-School/gogo/core/security"
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
		} else if w.Code != 200 || len(w.Result().Cookies()) != 1 {
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
