package redis

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

// Workers stores redacted, expiring observational presence. It may share a
// configured Redis connection but never reads or changes task/result state.
type Workers struct{ Connection *connector.Connection }

var workerWrite = redigo.NewScript(`
for i,key in ipairs(KEYS) do local k=redis.call('TYPE',key).ok;local wanted='hash';if i==2 then wanted='zset' end;if k~='none' and k~=wanted then return redis.error_reply('invalid worker monitor key') end end
local clock=redis.call('TIME');local now=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000)
local owner=redis.call('HGET',KEYS[1],'owner')
local status=redis.call('HGET',KEYS[1],'status');local expires=redis.call('HGET',KEYS[1],'expires')
if owner and (not expires or not tonumber(expires) or (status~='online' and status~='offline')) then return redis.error_reply('invalid worker presence') end
if ARGV[1]=='claim' then
 if owner and owner~=ARGV[2] and status=='online' and tonumber(expires)>now then return 'CONFLICT' end
elseif not owner or owner~=ARGV[2] or status~='online' then return 'LOST' end
if owner==ARGV[2] and (redis.call('HGET',KEYS[1],'instance') or '')~=ARGV[5] then return 'CONFLICT' end
if owner and owner~=ARGV[2] and ARGV[5]~='' and redis.call('HGET',KEYS[1],'instance')==ARGV[5] then return 'CONFLICT' end
local state='online';local untilAt=now+tonumber(ARGV[4]);if ARGV[1]=='release' then state='offline';untilAt=now end
redis.call('HSET',KEYS[1],'owner',ARGV[2],'instance',ARGV[5],'snapshot',ARGV[3],'status',state,'observed',string.format('%.0f',now),'expires',string.format('%.0f',untilAt))
redis.call('PEXPIRE',KEYS[1],86400000)
redis.call('ZREMRANGEBYSCORE',KEYS[2],'-inf',now)
redis.call('ZADD',KEYS[2],now+86400000,ARGV[6]);redis.call('PEXPIRE',KEYS[2],86400000)
return 'OK'
`)

var workerLookup = redigo.NewScript(`
local value=redis.call('HMGET',KEYS[1],'snapshot','status','observed','expires')
if not value[1] then return {} end
if not value[2] or not value[3] or not value[4] or not tonumber(value[4]) then return redis.error_reply('invalid worker presence') end
local clock=redis.call('TIME');local now=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000)
if value[2]=='online' and tonumber(value[4])<=now then value[2]='lost' end
return value
`)

func (w *Workers) key(id string) string { return w.Connection.PartitionKey("worker", id, "presence") }

func (w *Workers) write(ctx context.Context, operation string, lease async.WorkerLease, snapshot async.WorkerSnapshot, ttl time.Duration) error {
	if w == nil || w.Connection == nil {
		return async.ErrInvalid
	}
	if err := async.ValidateWorkerLease(lease, snapshot, ttl); err != nil {
		return err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return async.ErrInvalid
	}
	index, _ := w.Connection.PartitionIndex("worker", connector.Partition(lease.WorkerID), "dashboard-v1")
	result, err := w.Connection.Atomic(ctx, workerWrite, []string{w.key(lease.WorkerID), index}, operation, connector.Digest(lease.Token), data, ttl.Milliseconds(), snapshot.InstanceID, lease.WorkerID)
	if err != nil {
		return err
	}
	switch result {
	case "OK":
		return nil
	case "CONFLICT":
		return async.ErrConflict
	case "LOST":
		return async.ErrLeaseLost
	default:
		return async.ErrUnavailable
	}
}

func (w *Workers) ClaimWorker(ctx context.Context, lease async.WorkerLease, snapshot async.WorkerSnapshot, ttl time.Duration) error {
	return w.write(ctx, "claim", lease, snapshot, ttl)
}
func (w *Workers) RenewWorker(ctx context.Context, lease async.WorkerLease, snapshot async.WorkerSnapshot, ttl time.Duration) error {
	return w.write(ctx, "renew", lease, snapshot, ttl)
}
func (w *Workers) ReleaseWorker(ctx context.Context, lease async.WorkerLease, snapshot async.WorkerSnapshot) error {
	return w.write(ctx, "release", lease, snapshot, time.Millisecond)
}
func (w *Workers) LookupWorker(ctx context.Context, id string) (async.WorkerPresence, error) {
	if w == nil || w.Connection == nil || !async.ValidWorkerID(id) {
		return async.WorkerPresence{}, async.ErrInvalid
	}
	value, err := w.Connection.Atomic(ctx, workerLookup, []string{w.key(id)})
	if err != nil {
		return async.WorkerPresence{}, err
	}
	items, ok := value.([]any)
	if !ok {
		return async.WorkerPresence{}, async.ErrUnavailable
	}
	if len(items) == 0 {
		return async.WorkerPresence{}, async.ErrNotFound
	}
	if len(items) != 4 {
		return async.WorkerPresence{}, async.ErrUnavailable
	}
	var fields [4]string
	for i := range items {
		text, ok := items[i].(string)
		if !ok {
			return async.WorkerPresence{}, async.ErrUnavailable
		}
		fields[i] = text
	}
	var out async.WorkerPresence
	if len(fields[0]) > async.MaxPayloadBytes || json.Unmarshal([]byte(fields[0]), &out.Snapshot) != nil || async.ValidateWorkerSnapshot(out.Snapshot) != nil || out.Snapshot.ID != id {
		return async.WorkerPresence{}, async.ErrUnavailable
	}
	out.Status = async.PresenceStatus(fields[1])
	if out.Status != async.PresenceOnline && out.Status != async.PresenceLost && out.Status != async.PresenceOffline {
		return async.WorkerPresence{}, async.ErrUnavailable
	}
	observed, e1 := strconv.ParseInt(fields[2], 10, 64)
	expires, e2 := strconv.ParseInt(fields[3], 10, 64)
	if e1 != nil || e2 != nil || observed <= 0 || expires < observed {
		return async.WorkerPresence{}, async.ErrUnavailable
	}
	out.ObservedAt, out.ExpiresAt = time.UnixMilli(observed).UTC(), time.UnixMilli(expires).UTC()
	return out, nil
}

var _ async.WorkerPresenceStore = (*Workers)(nil)
