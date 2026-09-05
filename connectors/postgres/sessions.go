package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/sessions"
)

type SessionConfig struct {
	Backend *Backend
	// TombstoneTTL defaults to 24 hours. Existing session expiry may extend
	// retention beyond this minimum. Stale Save never inserts a missing record.
	TombstoneTTL time.Duration
}

// Sessions uses the schema declared by core/sessions.Migrations. Every write
// commits before returning. Session middleware must run outside application
// transactions; joining one could publish a cookie before its durable commit.
// Construction performs no I/O and owns neither migrations nor Backend.Close.
type Sessions struct {
	backend         *Backend
	tombstoneMillis int64
}

var ErrSessionRecord = errors.New("postgres: invalid session record")

func NewSessions(config SessionConfig) (*Sessions, error) {
	if config.Backend == nil {
		return nil, errors.New("postgres: session backend required")
	}
	if config.TombstoneTTL == 0 {
		config.TombstoneTTL = 24 * time.Hour
	}
	if config.TombstoneTTL < time.Millisecond {
		return nil, errors.New("postgres: invalid session tombstone retention")
	}
	millis := config.TombstoneTTL.Milliseconds()
	if config.TombstoneTTL%time.Millisecond > 0 {
		millis++
	}
	return &Sessions{backend: config.Backend, tombstoneMillis: millis}, nil
}

func sessionDigest(id string) (string, error) {
	if len(id) != 43 {
		return "", ErrSessionRecord
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(id)
	if err != nil || len(b) != 32 {
		return "", ErrSessionRecord
	}
	digest := sha256.Sum256([]byte(id))
	return hex.EncodeToString(digest[:]), nil
}

func (s *Sessions) ready(ctx context.Context) error {
	if s == nil || s.backend == nil {
		return errors.New("postgres: session backend required")
	}
	if db.InTransaction(ctx, s.backend.Alias()) {
		return errors.New("postgres: session persistence requires a committed application boundary")
	}
	return ctx.Err()
}

type sessionPayload struct {
	Format       int               `json:"format"`
	Data         map[string][]byte `json:"data"`
	BrowserClose bool              `json:"browser_close"`
}

func encodeSession(record sessions.Record) (string, []byte, error) {
	digest, err := sessionDigest(record.ID)
	if err != nil || record.Version < 1 || record.Version > math.MaxInt64 || record.ExpiresAt.IsZero() {
		return "", nil, ErrSessionRecord
	}
	// Match the shared provider payload bound without storing a recoverable
	// bearer key. Keep individual values opaque inside JSONB: PostgreSQL's JSON
	// normalization can expand short scientific-notation numbers dramatically.
	// Base64 encoding preserves exact JSON bytes and bounds provider overhead.
	record.ID = ""
	complete, err := json.Marshal(record)
	if err != nil || len(complete) > 64<<10 {
		return "", nil, ErrSessionRecord
	}
	data := make(map[string][]byte, len(record.Data))
	for key, value := range record.Data {
		if !json.Valid(value) {
			return "", nil, ErrSessionRecord
		}
		data[key] = value
	}
	payload, err := json.Marshal(sessionPayload{Format: 1, Data: data, BrowserClose: record.BrowserClose})
	if err != nil {
		return "", nil, ErrSessionRecord
	}
	return digest, payload, nil
}

func (s *Sessions) Create(ctx context.Context, record sessions.Record) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	digest, payload, err := encodeSession(record)
	if err != nil {
		return err
	}
	result, err := s.backend.Exec(ctx, `INSERT INTO gogo_sessions (key_digest,payload,version,expires_at,created_at,updated_at)
SELECT $1,$2::jsonb,$3,$4,clock_timestamp(),clock_timestamp() WHERE $4::timestamptz>clock_timestamp()
ON CONFLICT (key_digest) DO NOTHING`, digest, payload, int64(record.Version), record.ExpiresAt)
	return sessionWriteResult(result, err)
}

func (s *Sessions) Save(ctx context.Context, record sessions.Record, expected uint64) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if expected < 1 || expected >= math.MaxInt64 || record.Version != expected+1 {
		return ErrSessionRecord
	}
	digest, payload, err := encodeSession(record)
	if err != nil {
		return err
	}
	result, err := s.backend.Exec(ctx, `UPDATE gogo_sessions SET payload=$2::jsonb,version=$3,expires_at=$4,updated_at=clock_timestamp()
WHERE key_digest=$1 AND version=$5 AND deleted_at IS NULL AND expires_at>clock_timestamp() AND $4::timestamptz>clock_timestamp()`, digest, payload, int64(record.Version), record.ExpiresAt, int64(expected))
	return sessionWriteResult(result, err)
}

func sessionWriteResult(result db.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sessions.ErrConflict
	}
	return nil
}

func (s *Sessions) Load(ctx context.Context, id string) (sessions.Record, error) {
	if err := s.ready(ctx); err != nil {
		return sessions.Record{}, err
	}
	digest, err := sessionDigest(id)
	if err != nil {
		return sessions.Record{}, sessions.ErrNotFound
	}
	var payload []byte
	var version int64
	var expires time.Time
	err = db.QueryRow(ctx, s.backend, `SELECT payload,version,expires_at FROM gogo_sessions
WHERE key_digest=$1 AND deleted_at IS NULL AND expires_at>clock_timestamp()`, []any{digest}, &payload, &version, &expires)
	if errors.Is(err, db.ErrNoRows) {
		return sessions.Record{}, sessions.ErrNotFound
	}
	if err != nil {
		return sessions.Record{}, err
	}
	var decoded sessionPayload
	if len(payload) > 128<<10 || version < 1 || json.Unmarshal(payload, &decoded) != nil || decoded.Format != 1 {
		return sessions.Record{}, ErrSessionRecord
	}
	record := sessions.Record{ID: id, Data: make(map[string]json.RawMessage, len(decoded.Data)), Version: uint64(version), ExpiresAt: expires, BrowserClose: decoded.BrowserClose}
	for key, value := range decoded.Data {
		record.Data[key] = json.RawMessage(value)
	}
	if _, _, err := encodeSession(record); err != nil {
		return sessions.Record{}, err
	}
	return record, nil
}

// Delete atomically removes usable payload while retaining a non-authorizing
// tombstone. It is idempotent and also prevents an in-flight Create from making
// a logged-out identity usable again within the configured retention horizon.
func (s *Sessions) Delete(ctx context.Context, id string) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	digest, err := sessionDigest(id)
	if err != nil {
		return ErrSessionRecord
	}
	_, err = s.backend.Exec(ctx, `INSERT INTO gogo_sessions (key_digest,payload,version,expires_at,created_at,updated_at,deleted_at,tombstone_until)
VALUES ($1,'{}'::jsonb,1,clock_timestamp()+$2::bigint*interval '1 millisecond',clock_timestamp(),clock_timestamp(),clock_timestamp(),clock_timestamp()+$2::bigint*interval '1 millisecond')
ON CONFLICT (key_digest) DO UPDATE SET payload='{}'::jsonb,updated_at=clock_timestamp(),deleted_at=COALESCE(gogo_sessions.deleted_at,clock_timestamp()),
tombstone_until=GREATEST(gogo_sessions.tombstone_until,gogo_sessions.expires_at,clock_timestamp()+$2::bigint*interval '1 millisecond')`, digest, s.tombstoneMillis)
	return err
}

// DeleteIfVersion conditionally revokes a live identity for key rotation. It
// never overwrites newer session data or creates a tombstone for a missing key.
// Replacement creation is a subsequent confirmed write, not an implied atomic
// cross-key transfer. A failed replacement therefore requires a fresh login.
func (s *Sessions) DeleteIfVersion(ctx context.Context, id string, expected uint64) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if expected < 1 || expected > math.MaxInt64 {
		return ErrSessionRecord
	}
	digest, err := sessionDigest(id)
	if err != nil {
		return ErrSessionRecord
	}
	result, err := s.backend.Exec(ctx, `UPDATE gogo_sessions SET payload='{}'::jsonb,updated_at=clock_timestamp(),deleted_at=clock_timestamp(),
tombstone_until=GREATEST(tombstone_until,expires_at,clock_timestamp()+$3::bigint*interval '1 millisecond')
WHERE key_digest=$1 AND version=$2 AND deleted_at IS NULL AND expires_at>clock_timestamp()`, digest, int64(expected), s.tombstoneMillis)
	return sessionWriteResult(result, err)
}

// ClearExpired removes at most limit expired sessions or elapsed tombstones.
// SKIP LOCKED permits concurrent maintenance without waiting on live writes.
// It never removes a still-retained tombstone just because its old expiry passed.
func (s *Sessions) ClearExpired(ctx context.Context, limit int) (int64, error) {
	if err := s.ready(ctx); err != nil {
		return 0, err
	}
	if limit < 1 || limit > 10000 {
		return 0, ErrSessionRecord
	}
	result, err := s.backend.Exec(ctx, `WITH expired AS (
 SELECT key_digest FROM gogo_sessions
 WHERE (deleted_at IS NULL AND expires_at<=clock_timestamp()) OR (deleted_at IS NOT NULL AND tombstone_until<=clock_timestamp())
 ORDER BY expires_at,key_digest FOR UPDATE SKIP LOCKED LIMIT $1
) DELETE FROM gogo_sessions USING expired WHERE gogo_sessions.key_digest=expired.key_digest`, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

var _ sessions.Store = (*Sessions)(nil)
var _ sessions.VersionedDeleter = (*Sessions)(nil)
