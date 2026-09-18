package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

func (s *Schedules) periodicKeys(partition int) []string {
	prefix := fmt.Sprintf("schedule:{schedule-p%02d}:", partition)
	return []string{prefix + "periodic-due", prefix + "periodic-items"}
}

var periodicUpsert = redigo.NewScript(scheduleMetadataLua + `
validateScheduleKeys({'zset','hash'})
local raw=redis.call('HGET',KEYS[2],ARGV[1]);local revision='0';if raw then local old=readSchedule(raw,ARGV[1],true);revision=old.revision end
if revision~=ARGV[2] then return 0 end
readSchedule(ARGV[3],ARGV[1],true);local due=scheduleMillis(ARGV[4])
redis.call('HSET',KEYS[2],ARGV[1],ARGV[3]);if ARGV[5]=='1' then redis.call('ZADD',KEYS[1],due,ARGV[1]) else redis.call('ZREM',KEYS[1],ARGV[1]) end;return 1
`)

func (s *Schedules) UpsertSchedule(ctx context.Context, p async.PeriodicSchedule, expected uint64) error {
	if ctx == nil || !s.valid() {
		return async.ErrInvalid
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if expected == math.MaxUint64 || p.Revision != expected+1 {
		return async.ErrInvalid
	}
	p.Owner = ""
	p.LeaseUntil = time.Time{}
	payload, err := json.Marshal(p)
	if err != nil || len(payload) > async.MaxPayloadBytes {
		return async.ErrInvalid
	}
	b, err := marshalPeriodic(p)
	if err != nil {
		return err
	}
	enabled := "0"
	if p.Enabled {
		enabled = "1"
	}
	out, err := s.Connection.Atomic(ctx, periodicUpsert, s.periodicKeys(connector.Partition(p.ID)), p.ID, strconv.FormatUint(expected, 10), b, p.NextDue.UnixMilli(), enabled)
	if err != nil {
		return err
	}
	if out.(int64) != 1 {
		return async.ErrConflict
	}
	return nil
}

var periodicLease = redigo.NewScript(scheduleMetadataLua + incrementCounterLua + `
validateScheduleKeys({'zset','hash'})
local tm=redis.call('TIME');local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
local lease=scheduleMillis(now+scheduleMillis(ARGV[3]))
local ids=redis.call('ZRANGEBYSCORE',KEYS[1],'-inf',now,'LIMIT',0,ARGV[2]);local out={};local updates={}
for _,id in ipairs(ids) do
 local raw=redis.call('HGET',KEYS[2],id);if not raw then return redis.error_reply('GOGO_MISSING_SCHEDULE') end
 local item=readSchedule(raw,id,true)
 if item.enabled and item.lease_millis<=now then item.owner=ARGV[1];item.fence=incrementCounter(item.fence);item.revision=incrementCounter(item.revision);item.lease_millis=lease;item.observed_millis=now;raw=cjson.encode(item);table.insert(updates,{id=id,raw=raw,lease=item.lease_millis});table.insert(out,raw) end
end
for _,item in ipairs(updates) do redis.call('HSET',KEYS[2],item.id,item.raw);redis.call('ZADD',KEYS[1],item.lease,item.id) end
return out
`)

func (s *Schedules) LeaseSchedules(ctx context.Context, owner string, limit int, lease time.Duration) ([]async.PeriodicSchedule, error) {
	if ctx == nil || !s.valid() || owner == "" || limit < 1 || limit > 1000 || lease < time.Millisecond {
		return nil, async.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []async.PeriodicSchedule
	start := s.periodicRotation.start()
	for offset := 0; offset < 64 && len(out) < limit; offset++ {
		p := (start + offset) % 64
		values, err := s.Connection.Atomic(ctx, periodicLease, s.periodicKeys(p), owner, limit-len(out), lease.Milliseconds())
		if err != nil {
			return nil, err
		}
		for _, value := range values.([]any) {
			item, err := unmarshalPeriodic([]byte(value.(string)))
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
	}
	return out, nil
}

var periodicCommit = redigo.NewScript(scheduleMetadataLua + `
validateScheduleKeys({'zset','hash','hash','zset'})
local raw=redis.call('HGET',KEYS[2],ARGV[1]);if not raw then return 'MISSING' end
local old=readSchedule(raw,ARGV[1],true);local tm=redis.call('TIME');local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
if old.revision~=ARGV[2] or old.fence~=ARGV[3] or old.owner~=ARGV[4] or old.lease_millis<=now then return 'LEASE' end
readSchedule(ARGV[5],ARGV[1],true);local due=scheduleMillis(ARGV[6])
local intents=cjson.decode(ARGV[7]);if type(intents)~='table' then error('invalid occurrence intent batch') end
local staged={};local seen={}
for _,intent in ipairs(intents) do
 if type(intent)~='table' or intent.format~=1 or type(intent.id)~='string' or #intent.id~=36 or seen[intent.id] or intent.source_id~=ARGV[1] or type(intent.payload)~='string' or #intent.payload==0 then error('invalid occurrence intent identity') end
 seen[intent.id]=true;table.insert(staged,{id=intent.id,raw=cjson.encode(intent),index=intent.source_id..':'..intent.id})
end
redis.call('HSET',KEYS[2],ARGV[1],ARGV[5]);if ARGV[8]=='1' then redis.call('ZADD',KEYS[1],due,ARGV[1]) else redis.call('ZREM',KEYS[1],ARGV[1]) end
for _,intent in ipairs(staged) do
 if redis.call('HEXISTS',KEYS[3],intent.id)==0 then redis.call('HSET',KEYS[3],intent.id,intent.raw);redis.call('ZADD',KEYS[4],now,intent.index) end
end;return 'OK'
`)

func (s *Schedules) CommitOccurrence(ctx context.Context, p async.PeriodicSchedule, next time.Time, lastID string, intents []async.Intent) error {
	if ctx == nil || !s.valid() || p.Validate() != nil || p.Owner == "" || p.Fence == 0 || next.IsZero() || !next.After(p.NextDue) || p.Revision == math.MaxUint64 {
		return async.ErrInvalid
	}
	if lastID != "" && !validIntentIDs(p.ID, lastID) {
		return async.ErrInvalid
	}
	seen := map[string]bool{}
	for _, intent := range intents {
		if intent.SourceID != p.ID || !validIntentIDs(p.ID, intent.ID) || seen[intent.ID] {
			return async.ErrInvalid
		}
		seen[intent.ID] = true
	}
	original := p
	p.NextDue = next
	p.LastTaskID = lastID
	p.Revision++
	p.Owner = ""
	p.LeaseUntil = time.Time{}
	if !p.EndAt.IsZero() && !next.Before(p.EndAt) {
		p.Enabled = false
	}
	if intents == nil {
		intents = []async.Intent{}
	}
	pb, err := marshalPeriodic(p)
	if err != nil {
		return err
	}
	ib, err := marshalIntents(intents)
	if err != nil {
		return err
	}
	keys := s.periodicKeys(connector.Partition(p.ID))
	index, _ := s.Connection.PartitionIndex("schedule", connector.Partition(p.ID), "pending-intents")
	keys = append(keys, s.Connection.PartitionKey("schedule", p.ID, "intents"), index)
	enabled := "0"
	if p.Enabled {
		enabled = "1"
	}
	out, err := s.Connection.Atomic(ctx, periodicCommit, keys, p.ID, strconv.FormatUint(original.Revision, 10), strconv.FormatUint(original.Fence, 10), original.Owner, pb, next.UnixMilli(), ib, enabled)
	if err != nil {
		return err
	}
	if out != "OK" {
		return async.ErrLeaseLost
	}
	return nil
}
func (s *Schedules) List(ctx context.Context, limit int) ([]async.PeriodicSchedule, error) {
	if ctx == nil || !s.valid() || limit < 1 || limit > 1000 {
		return nil, async.ErrInvalid
	}
	var out []async.PeriodicSchedule
	for partition := 0; partition < 64 && len(out) < limit; partition++ {
		var cursor uint64
		for {
			values, next, err := s.Connection.Client().HScan(ctx, s.periodicKeys(partition)[1], cursor, "*", int64(limit-len(out))).Result()
			if err != nil {
				return nil, err
			}
			for i := 1; i < len(values) && len(out) < limit; i += 2 {
				p, err := unmarshalPeriodic([]byte(values[i]))
				if err != nil {
					return nil, err
				}
				out = append(out, p)
			}
			if next == 0 || len(out) >= limit {
				break
			}
			cursor = next
		}
	}
	return out, nil
}
func (s *Schedules) Disable(ctx context.Context, id string, expected uint64) error {
	if ctx == nil || !s.valid() || !validIntentIDs(id, id) {
		return async.ErrInvalid
	}
	raw, err := s.Connection.Client().HGet(ctx, s.periodicKeys(connector.Partition(id))[1], id).Bytes()
	if err != nil {
		return err
	}
	p, err := unmarshalPeriodic(raw)
	if err != nil {
		return err
	}
	if p.Revision != expected {
		return async.ErrConflict
	}
	p.Enabled = false
	p.Revision++
	return s.UpsertSchedule(ctx, p, expected)
}
func (s *Schedules) intentBackend() intentBackend {
	return intentBackend{s.Connection, "schedule", &s.intentRotation}
}
func (s *Schedules) ListIntents(ctx context.Context, limit int) ([]async.Intent, error) {
	return s.intentBackend().list(ctx, limit)
}
func (s *Schedules) ClaimIntent(ctx context.Context, source, id, owner string, lease time.Duration) (async.Intent, error) {
	return s.intentBackend().claim(ctx, source, id, owner, lease)
}
func (s *Schedules) MarkIntentDelivered(ctx context.Context, source, id string, fence uint64, owner string) error {
	return s.intentBackend().mark(ctx, source, id, fence, owner)
}

var _ async.PeriodicStore = (*Schedules)(nil)
