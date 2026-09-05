package redis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Newton-School/gogo/core/sessions"
	redigo "github.com/redis/go-redis/v9"
)

type Sessions struct {
	Connection   *Connection
	TombstoneTTL time.Duration
}

func validSessionID(id string) bool {
	if len(id) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(id)
	return err == nil && len(decoded) == 32
}

func (s *Sessions) key(id string) string {
	return s.Connection.namespace + ":session:{" + Digest(id) + "}"
}
func (s *Sessions) valid() error {
	if s.Connection == nil || s.Connection.role == CacheRole {
		return ErrInvalid
	}
	return nil
}

var sessionWrite = redigo.NewScript(`
local tm=redis.call('TIME');local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
if tonumber(ARGV[4])<=now then return -2 end
local old=redis.call('HGET',KEYS[1],'version')
if ARGV[1]=='create' then if old then return 0 end
else if not old or old~=ARGV[2] or redis.call('HGET',KEYS[1],'deleted')=='1' then return 0 end end
redis.call('HSET',KEYS[1],'version',ARGV[3],'payload',ARGV[5],'expiry',ARGV[4],'deleted','0')
redis.call('PEXPIREAT',KEYS[1],ARGV[4]);return 1
`)

func (s *Sessions) write(ctx context.Context, r sessions.Record, expected uint64, create bool) error {
	if err := s.valid(); err != nil {
		return err
	}
	if !validSessionID(r.ID) || r.Version < 1 {
		return ErrInvalid
	}
	if !create && r.Version != expected+1 {
		return ErrInvalid
	}
	id := r.ID
	// The session ID is a bearer secret. Store only its digest as the lookup
	// key, never a recoverable copy in the session payload.
	r.ID = ""
	b, err := json.Marshal(r)
	if err != nil || len(b) > 64<<10 {
		return ErrInvalid
	}
	mode := "save"
	if create {
		mode = "create"
	}
	out, err := s.Connection.Atomic(ctx, sessionWrite, []string{s.key(id)}, mode, strconv.FormatUint(expected, 10), strconv.FormatUint(r.Version, 10), r.ExpiresAt.UnixMilli(), b)
	if err != nil {
		return err
	}
	if out.(int64) != 1 {
		return sessions.ErrConflict
	}
	return nil
}
func (s *Sessions) Create(ctx context.Context, r sessions.Record) error {
	return s.write(ctx, r, 0, true)
}
func (s *Sessions) Save(ctx context.Context, r sessions.Record, expected uint64) error {
	return s.write(ctx, r, expected, false)
}

var sessionLoad = redigo.NewScript(`
local tm=redis.call('TIME');local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
local expiry=redis.call('HGET',KEYS[1],'expiry')
if not expiry or tonumber(expiry)<=now or redis.call('HGET',KEYS[1],'deleted')=='1' then return false end
return redis.call('HGET',KEYS[1],'payload')
`)

func (s *Sessions) Load(ctx context.Context, id string) (sessions.Record, error) {
	var r sessions.Record
	if err := s.valid(); err != nil {
		return r, err
	}
	if !validSessionID(id) {
		return r, sessions.ErrNotFound
	}
	out, err := s.Connection.Atomic(ctx, sessionLoad, []string{s.key(id)})
	if errors.Is(err, redigo.Nil) {
		return r, sessions.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	b, ok := out.(string)
	if !ok {
		return r, sessions.ErrNotFound
	}
	if err := json.Unmarshal([]byte(b), &r); err != nil {
		return r, ErrUnavailable
	}
	r.ID = id
	return r, nil
}

var sessionDelete = redigo.NewScript(`redis.call('HSET',KEYS[1],'deleted','1','version','tombstone');redis.call('HDEL',KEYS[1],'payload');redis.call('PEXPIRE',KEYS[1],ARGV[1]);return 1`)

func (s *Sessions) Delete(ctx context.Context, id string) error {
	if err := s.valid(); err != nil {
		return err
	}
	if !validSessionID(id) {
		return ErrInvalid
	}
	ttl := s.TombstoneTTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	if ttl < time.Minute {
		return ErrInvalid
	}
	_, err := s.Connection.Atomic(ctx, sessionDelete, []string{s.key(id)}, ttl.Milliseconds())
	return err
}

var _ sessions.Store = (*Sessions)(nil)
