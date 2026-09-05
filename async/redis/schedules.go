package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

type Schedules struct{ Connection *connector.Connection }

func (s *Schedules) keys(partition int) []string {
	prefix := s.Connection.Namespace() + fmt.Sprintf(":schedule:{schedule-p%02d}:", partition)
	return []string{prefix + "due", prefix + "items"}
}
func delayedID(e async.Envelope) string { return e.ID + ":" + strconv.Itoa(e.Retries) }

var schedulePut = redigo.NewScript(`
local old=redis.call('HGET',KEYS[2],ARGV[1]);if old then local item=cjson.decode(old);if item.format~=1 then return redis.error_reply('GOGO_INVALID_SCHEDULE_METADATA') end;if item.digest~=ARGV[3] then return 0 end;return 1 end
redis.call('HSET',KEYS[2],ARGV[1],ARGV[2]);redis.call('ZADD',KEYS[1],ARGV[4],ARGV[1]);return 1
`)

func (s *Schedules) Schedule(ctx context.Context, e async.Envelope) error {
	if s.Connection == nil || s.Connection.Role() == connector.CacheRole {
		return async.ErrInvalid
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if e.ETA.IsZero() {
		return async.ErrInvalid
	}
	b, err := marshalDocument(opaqueDocument{ID: delayedID(e), Digest: e.DispatchDigest()}, e)
	if err != nil {
		return err
	}
	out, err := s.Connection.Atomic(ctx, schedulePut, s.keys(connector.Partition(e.ID)), delayedID(e), b, e.DispatchDigest(), e.ETA.UnixMilli())
	if err != nil {
		return err
	}
	if out.(int64) != 1 {
		return async.ErrConflict
	}
	return nil
}

var scheduleLease = redigo.NewScript(incrementCounterLua + `
local tm=redis.call('TIME');local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
local ids=redis.call('ZRANGEBYSCORE',KEYS[1],'-inf',now,'LIMIT',0,ARGV[2]);local out={};local updates={}
for _,id in ipairs(ids) do
 local raw=redis.call('HGET',KEYS[2],id);if not raw then return redis.error_reply('GOGO_MISSING_SCHEDULE') end
 local item=cjson.decode(raw)
 if item.format~=1 then return redis.error_reply('GOGO_INVALID_SCHEDULE_METADATA') end
 if (item.lease_millis or 0)<=now then item.owner=ARGV[1];item.fence=incrementCounter(item.fence);item.lease_millis=now+tonumber(ARGV[3]);raw=cjson.encode(item);table.insert(updates,{id=id,raw=raw,lease=item.lease_millis});table.insert(out,raw) end
end
for _,item in ipairs(updates) do redis.call('HSET',KEYS[2],item.id,item.raw);redis.call('ZADD',KEYS[1],item.lease,item.id) end
return out
`)

func (s *Schedules) LeaseDue(ctx context.Context, owner string, limit int, lease time.Duration) ([]async.DelayedItem, error) {
	if s.Connection == nil || owner == "" || limit < 1 || limit > 1000 || lease < time.Millisecond {
		return nil, async.ErrInvalid
	}
	var out []async.DelayedItem
	for partition := 0; partition < 64 && len(out) < limit; partition++ {
		values, err := s.Connection.Atomic(ctx, scheduleLease, s.keys(partition), owner, limit-len(out), lease.Milliseconds())
		if err != nil {
			return nil, err
		}
		for _, value := range values.([]any) {
			document, err := unmarshalDocument([]byte(value.(string)))
			if err != nil {
				return nil, err
			}
			var envelope async.Envelope
			if err := json.Unmarshal(document.Payload, &envelope); err != nil || document.ID != delayedID(envelope) || document.Digest != envelope.DispatchDigest() {
				return nil, async.ErrUnavailable
			}
			fence, _ := strconv.ParseUint(document.Fence, 10, 64)
			out = append(out, async.DelayedItem{Envelope: envelope, Owner: document.Owner, Fence: fence, LeaseUntil: time.UnixMilli(document.LeaseMillis)})
		}
	}
	return out, nil
}

var scheduleCommit = redigo.NewScript(`
local raw=redis.call('HGET',KEYS[2],ARGV[1]);if not raw then return 1 end
local item=cjson.decode(raw);local tm=redis.call('TIME');local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
if item.format~=1 then return redis.error_reply('GOGO_INVALID_SCHEDULE_METADATA') end
if item.owner~=ARGV[2] or tostring(item.fence)~=ARGV[3] or (item.lease_millis or 0)<=now then return 0 end
redis.call('ZREM',KEYS[1],ARGV[1]);redis.call('HDEL',KEYS[2],ARGV[1]);return 1
`)

func (s *Schedules) CommitFire(ctx context.Context, item async.DelayedItem) error {
	out, err := s.Connection.Atomic(ctx, scheduleCommit, s.keys(connector.Partition(item.Envelope.ID)), delayedID(item.Envelope), item.Owner, strconv.FormatUint(item.Fence, 10))
	if err != nil {
		return err
	}
	if out.(int64) != 1 {
		return async.ErrLeaseLost
	}
	return nil
}

var _ async.ScheduleStore = (*Schedules)(nil)
