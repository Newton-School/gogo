// Package sessions provides versioned session persistence and request-local data.
package sessions

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"time"
)

var (
	ErrNotFound  = errors.New("session not found")
	ErrConflict  = errors.New("session changed concurrently")
	ErrCommitted = errors.New("session response already committed")
)

type Record struct {
	ID           string
	Data         map[string]json.RawMessage
	ExpiresAt    time.Time
	Version      uint64
	BrowserClose bool
}
type Store interface {
	Load(context.Context, string) (Record, error)
	Create(context.Context, Record) error
	Save(context.Context, Record, uint64) error
	Delete(context.Context, string) error
}

func Clone(r Record) Record {
	r.Data = maps.Clone(r.Data)
	for k, v := range r.Data {
		r.Data[k] = append(json.RawMessage(nil), v...)
	}
	return r
}
func NewID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

type Session struct {
	record                                         Record
	originalID                                     string
	modified, accessed, flushed, rotate, committed bool
}

func New(record Record) *Session { return &Session{record: Clone(record), originalID: record.ID} }
func (s *Session) Get(key string, dst any) (bool, error) {
	s.accessed = true
	value, ok := s.record.Data[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(value, dst)
}
func (s *Session) Set(key string, value any) error {
	if s.committed {
		return ErrCommitted
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if s.record.Data == nil {
		s.record.Data = map[string]json.RawMessage{}
	}
	s.record.Data[key] = b
	s.modified = true
	return nil
}
func (s *Session) Pop(key string, dst any) (bool, error) {
	if s.committed {
		return false, ErrCommitted
	}
	ok, err := s.Get(key, dst)
	if err == nil && ok {
		delete(s.record.Data, key)
		s.modified = true
	}
	return ok, err
}
func (s *Session) Clear() error {
	if s.committed {
		return ErrCommitted
	}
	s.record.Data = map[string]json.RawMessage{}
	s.modified = true
	return nil
}
func (s *Session) Flush() error {
	if err := s.Clear(); err != nil {
		return err
	}
	s.flushed = true
	// A later write starts a new anonymous/authenticated session. Never retain
	// the flushed identity, expiry policy or prior account's data.
	s.record = Record{Data: map[string]json.RawMessage{}}
	s.modified = false
	s.rotate = false
	return nil
}
func (s *Session) CycleKey() error {
	if s.committed {
		return ErrCommitted
	}
	s.rotate = true
	s.modified = true
	return nil
}
func (s *Session) SetExpiry(expiry time.Time) error {
	if s.committed {
		return ErrCommitted
	}
	s.record.ExpiresAt = expiry
	s.modified = true
	return nil
}
func (s *Session) SetBrowserClose(value bool) error {
	if s.committed {
		return ErrCommitted
	}
	s.record.BrowserClose = value
	s.modified = true
	return nil
}
func (s *Session) Modified() bool   { return s.modified }
func (s *Session) Accessed() bool   { return s.accessed }
func (s *Session) Snapshot() Record { return Clone(s.record) }

// Persist must run before response headers. A rotation deletes the old identity
// before publishing a replacement; provider failures never preserve login silently.
func (s *Session) Persist(ctx context.Context, store Store, now time.Time, ttl time.Duration) error {
	if s.committed {
		return ErrCommitted
	}
	s.committed = true
	if store == nil {
		return errors.New("session store required")
	}
	if s.flushed {
		if s.originalID != "" {
			if err := store.Delete(ctx, s.originalID); err != nil {
				return err
			}
			s.originalID = ""
		}
		if !s.modified {
			return nil
		}
	}
	if !s.modified {
		return nil
	}
	if s.record.ExpiresAt.IsZero() {
		s.record.ExpiresAt = now.Add(ttl)
	}
	if !s.record.ExpiresAt.After(now) {
		return errors.New("session expiry must be in the future")
	}
	if s.rotate && s.originalID != "" {
		if err := store.Delete(ctx, s.originalID); err != nil {
			return err
		}
		s.record.ID = ""
	}
	if s.record.ID == "" {
		id, err := NewID()
		if err != nil {
			return err
		}
		s.record.ID = id
		s.record.Version = 1
		return store.Create(ctx, Clone(s.record))
	}
	expected := s.record.Version
	s.record.Version++
	return store.Save(ctx, Clone(s.record), expected)
}

type contextKey struct{}

func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, contextKey{}, s)
}
func FromContext(ctx context.Context) (*Session, bool) {
	s, ok := ctx.Value(contextKey{}).(*Session)
	return s, ok
}
