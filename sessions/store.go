package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Newton-School/gogo/cache"
)

var (
	// ErrSessionTampered is returned when a signed key or cookie fails validation.
	ErrSessionTampered = errors.New("session data was tampered")
	// ErrSessionStoreRequired is returned when a composed store misses a backend.
	ErrSessionStoreRequired = errors.New("session store is required")
)

// Store is the common session backend contract.
type Store interface {
	Load(ctx context.Context, key string) (*Session, bool, error)
	Save(ctx context.Context, session *Session) error
	Delete(ctx context.Context, key string) error
	CycleKey(ctx context.Context, session *Session) error
	Flush(ctx context.Context, session *Session) error
}

type sessionRecord struct {
	Data       map[string]string `json:"data"`
	ExpireDate time.Time         `json:"expire_date"`
}

// DatabaseStore is a database-shaped in-memory session backend.
type DatabaseStore struct {
	secret string
	mu     sync.RWMutex
	rows   map[string]sessionRecord
}

// NewDatabaseStore creates a database-backed session store.
func NewDatabaseStore(secret string) *DatabaseStore {
	return &DatabaseStore{secret: secret, rows: make(map[string]sessionRecord)}
}

// Load returns one unexpired session.
func (s *DatabaseStore) Load(ctx context.Context, key string) (*Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !VerifySessionKey(s.secret, key) {
		return nil, false, ErrSessionTampered
	}
	s.mu.RLock()
	record, ok := s.rows[key]
	s.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	session := sessionFromRecord(key, record)
	if session.IsExpired(time.Now()) {
		_ = s.Delete(ctx, key)
		return nil, false, nil
	}
	return session, true, nil
}

// Save inserts or updates one session.
func (s *DatabaseStore) Save(ctx context.Context, session *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensureServerSideSession(s.secret, session); err != nil {
		return err
	}
	s.mu.Lock()
	s.rows[session.Key] = recordFromSession(session)
	s.mu.Unlock()
	session.Modified = false
	return nil
}

// Delete removes one session.
func (s *DatabaseStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.rows, key)
	s.mu.Unlock()
	return nil
}

// CycleKey rotates the key while preserving data.
func (s *DatabaseStore) CycleKey(ctx context.Context, session *Session) error {
	return cycleServerSideKey(ctx, s, s.secret, session)
}

// Flush deletes existing data and creates a new empty session key.
func (s *DatabaseStore) Flush(ctx context.Context, session *Session) error {
	return flushSession(ctx, s, s.secret, session)
}

// CacheStore stores sessions in the framework cache abstraction.
type CacheStore struct {
	secret  string
	store   cache.Store
	mu      sync.RWMutex
	deleted map[string]struct{}
}

// NewCacheStore creates a cache-only session backend.
func NewCacheStore(store cache.Store, secret string) *CacheStore {
	return &CacheStore{secret: secret, store: store, deleted: make(map[string]struct{})}
}

// Load returns one unexpired cached session.
func (s *CacheStore) Load(ctx context.Context, key string) (*Session, bool, error) {
	if s.store == nil {
		return nil, false, ErrSessionStoreRequired
	}
	if !VerifySessionKey(s.secret, key) {
		return nil, false, ErrSessionTampered
	}
	if s.isDeleted(key) {
		return nil, false, nil
	}
	entry, ok, err := s.store.Get(ctx, key)
	if err != nil || !ok {
		return nil, ok, err
	}
	record, err := decodeRecord(entry.Body)
	if err != nil {
		return nil, false, err
	}
	session := sessionFromRecord(key, record)
	if session.IsExpired(time.Now()) {
		_ = s.Delete(ctx, key)
		return nil, false, nil
	}
	return session, true, nil
}

// Save writes one session to cache.
func (s *CacheStore) Save(ctx context.Context, session *Session) error {
	if s.store == nil {
		return ErrSessionStoreRequired
	}
	if err := ensureServerSideSession(s.secret, session); err != nil {
		return err
	}
	body, err := encodeRecord(recordFromSession(session))
	if err != nil {
		return err
	}
	ttl := session.ExpiryAge(time.Now())
	if session.ExpireDate.IsZero() {
		ttl = 0
	}
	s.unmarkDeleted(session.Key)
	if err := s.store.Set(ctx, session.Key, cache.Entry{Body: body}, ttl); err != nil {
		return err
	}
	session.Modified = false
	return nil
}

// Delete removes one cached session by tombstoning its key.
func (s *CacheStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.deleted[key] = struct{}{}
	s.mu.Unlock()
	return nil
}

// CycleKey rotates the key while preserving data.
func (s *CacheStore) CycleKey(ctx context.Context, session *Session) error {
	return cycleServerSideKey(ctx, s, s.secret, session)
}

// Flush deletes existing data and creates a new empty session key.
func (s *CacheStore) Flush(ctx context.Context, session *Session) error {
	return flushSession(ctx, s, s.secret, session)
}

func (s *CacheStore) isDeleted(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.deleted[key]
	return ok
}

func (s *CacheStore) unmarkDeleted(key string) {
	s.mu.Lock()
	delete(s.deleted, key)
	s.mu.Unlock()
}

// CachedDatabaseStore reads through cache and persists to database.
type CachedDatabaseStore struct {
	database Store
	cache    *CacheStore
}

// NewCachedDatabaseStore creates a cached database session backend.
func NewCachedDatabaseStore(database Store, cacheStore *CacheStore) *CachedDatabaseStore {
	return &CachedDatabaseStore{database: database, cache: cacheStore}
}

// Load tries cache first and falls back to database.
func (s *CachedDatabaseStore) Load(ctx context.Context, key string) (*Session, bool, error) {
	if s.cache != nil {
		session, ok, err := s.cache.Load(ctx, key)
		if err != nil || ok {
			return session, ok, err
		}
	}
	if s.database == nil {
		return nil, false, ErrSessionStoreRequired
	}
	session, ok, err := s.database.Load(ctx, key)
	if err != nil || !ok {
		return session, ok, err
	}
	if s.cache != nil {
		_ = s.cache.Save(ctx, session.clone())
	}
	return session, true, nil
}

// Save writes to database and cache.
func (s *CachedDatabaseStore) Save(ctx context.Context, session *Session) error {
	if s.database == nil {
		return ErrSessionStoreRequired
	}
	if err := s.database.Save(ctx, session); err != nil {
		return err
	}
	if s.cache != nil {
		return s.cache.Save(ctx, session.clone())
	}
	return nil
}

// Delete removes from database and cache.
func (s *CachedDatabaseStore) Delete(ctx context.Context, key string) error {
	if s.database != nil {
		if err := s.database.Delete(ctx, key); err != nil {
			return err
		}
	}
	if s.cache != nil {
		return s.cache.Delete(ctx, key)
	}
	return nil
}

// CycleKey rotates the key while preserving data.
func (s *CachedDatabaseStore) CycleKey(ctx context.Context, session *Session) error {
	oldKey := session.Key
	if s.database == nil {
		return ErrSessionStoreRequired
	}
	if err := s.database.CycleKey(ctx, session); err != nil {
		return err
	}
	if s.cache != nil {
		_ = s.cache.Delete(ctx, oldKey)
		return s.cache.Save(ctx, session.clone())
	}
	return nil
}

// Flush deletes existing data and creates a new empty session key.
func (s *CachedDatabaseStore) Flush(ctx context.Context, session *Session) error {
	oldKey := session.Key
	if s.database == nil {
		return ErrSessionStoreRequired
	}
	if err := s.database.Flush(ctx, session); err != nil {
		return err
	}
	if s.cache != nil {
		_ = s.cache.Delete(ctx, oldKey)
		return s.cache.Save(ctx, session.clone())
	}
	return nil
}

// FileStore stores sessions as JSON files.
type FileStore struct {
	*DatabaseStore
	dir string
}

// NewFileStore creates a file-backed session backend.
func NewFileStore(dir, secret string) *FileStore {
	return &FileStore{DatabaseStore: NewDatabaseStore(secret), dir: dir}
}

// Load reads one session file.
func (s *FileStore) Load(ctx context.Context, key string) (*Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !VerifySessionKey(s.secret, key) {
		return nil, false, ErrSessionTampered
	}
	body, err := os.ReadFile(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	record, err := decodeRecord(body)
	if err != nil {
		return nil, false, err
	}
	session := sessionFromRecord(key, record)
	if session.IsExpired(time.Now()) {
		_ = s.Delete(ctx, key)
		return nil, false, nil
	}
	return session, true, nil
}

// Save writes one session file with owner-only permissions.
func (s *FileStore) Save(ctx context.Context, session *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensureServerSideSession(s.secret, session); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	body, err := encodeRecord(recordFromSession(session))
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path(session.Key), body, 0o600); err != nil {
		return err
	}
	session.Modified = false
	return nil
}

// Delete removes one session file.
func (s *FileStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.Remove(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// CycleKey rotates the key while preserving file-backed data.
func (s *FileStore) CycleKey(ctx context.Context, session *Session) error {
	return cycleServerSideKey(ctx, s, s.secret, session)
}

// Flush deletes existing file-backed data and creates a new empty session key.
func (s *FileStore) Flush(ctx context.Context, session *Session) error {
	return flushSession(ctx, s, s.secret, session)
}

func (s *FileStore) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

// SignedCookieStore stores the entire session payload in a signed cookie value.
type SignedCookieStore struct {
	secret  string
	mu      sync.RWMutex
	revoked map[string]struct{}
}

// NewSignedCookieStore creates a signed-cookie session backend.
func NewSignedCookieStore(secret string) *SignedCookieStore {
	return &SignedCookieStore{secret: secret, revoked: make(map[string]struct{})}
}

// Load verifies and decodes a signed-cookie session payload.
func (s *SignedCookieStore) Load(ctx context.Context, key string) (*Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if s.isRevoked(key) {
		return nil, false, nil
	}
	payload, ok := verifySignedValue(s.secret, key)
	if !ok {
		return nil, false, ErrSessionTampered
	}
	payload, _, _ = strings.Cut(payload, "~")
	body, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, false, ErrSessionTampered
	}
	record, err := decodeRecord(body)
	if err != nil {
		return nil, false, err
	}
	session := sessionFromRecord(key, record)
	if session.IsExpired(time.Now()) {
		return nil, false, nil
	}
	return session, true, nil
}

// Save serializes and signs the session payload into Key.
func (s *SignedCookieStore) Save(ctx context.Context, session *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ensureExpiry(session)
	body, err := encodeRecord(recordFromSession(session))
	if err != nil {
		return err
	}
	nonce, err := randomToken(12)
	if err != nil {
		return err
	}
	payload := base64.RawURLEncoding.EncodeToString(body) + "~" + nonce
	session.Key = signValue(s.secret, payload)
	session.Modified = false
	return nil
}

// Delete revokes a signed cookie value for this process.
func (s *SignedCookieStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.revoked[key] = struct{}{}
	s.mu.Unlock()
	return nil
}

// CycleKey re-signs the current payload and revokes the old value.
func (s *SignedCookieStore) CycleKey(ctx context.Context, session *Session) error {
	oldKey := session.Key
	if err := s.Save(ctx, session); err != nil {
		return err
	}
	if oldKey != "" {
		return s.Delete(ctx, oldKey)
	}
	return nil
}

// Flush clears data and issues a new signed cookie payload.
func (s *SignedCookieStore) Flush(ctx context.Context, session *Session) error {
	oldKey := session.Key
	session.Data = make(map[string]string)
	session.Modified = true
	if err := s.Save(ctx, session); err != nil {
		return err
	}
	if oldKey != "" {
		return s.Delete(ctx, oldKey)
	}
	return nil
}

func (s *SignedCookieStore) isRevoked(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.revoked[key]
	return ok
}

func ensureServerSideSession(secret string, session *Session) error {
	if session == nil {
		return ErrSessionStoreRequired
	}
	if session.Key == "" {
		key, err := NewSignedSessionKey(secret)
		if err != nil {
			return err
		}
		session.Key = key
	} else if !VerifySessionKey(secret, session.Key) {
		return ErrSessionTampered
	}
	ensureExpiry(session)
	return nil
}

func ensureExpiry(session *Session) {
	if session.ExpireDate.IsZero() && session.maxAge > 0 {
		session.ExpireDate = time.Now().Add(session.maxAge)
	}
}

func cycleServerSideKey(ctx context.Context, store Store, secret string, session *Session) error {
	oldKey := session.Key
	key, err := NewSignedSessionKey(secret)
	if err != nil {
		return err
	}
	session.Key = key
	session.Modified = true
	if err := store.Save(ctx, session); err != nil {
		return err
	}
	if oldKey != "" {
		return store.Delete(ctx, oldKey)
	}
	return nil
}

func flushSession(ctx context.Context, store Store, secret string, session *Session) error {
	oldKey := session.Key
	key, err := NewSignedSessionKey(secret)
	if err != nil {
		return err
	}
	session.Key = key
	session.Data = make(map[string]string)
	session.Modified = true
	if err := store.Save(ctx, session); err != nil {
		return err
	}
	if oldKey != "" {
		return store.Delete(ctx, oldKey)
	}
	return nil
}

func recordFromSession(session *Session) sessionRecord {
	ensureExpiry(session)
	return sessionRecord{Data: cloneData(session.Data), ExpireDate: session.ExpireDate}
}

func sessionFromRecord(key string, record sessionRecord) *Session {
	return &Session{Key: key, Data: cloneData(record.Data), ExpireDate: record.ExpireDate}
}

func encodeRecord(record sessionRecord) ([]byte, error) {
	body, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode session: %w", err)
	}
	return body, nil
}

func decodeRecord(body []byte) (sessionRecord, error) {
	var record sessionRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return sessionRecord{}, fmt.Errorf("decode session: %w", err)
	}
	if record.Data == nil {
		record.Data = make(map[string]string)
	}
	return record, nil
}
