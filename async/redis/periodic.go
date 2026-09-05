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

func (s *Schedules) periodicKeys(partition int) []string {
	prefix := s.Connection.Namespace() + fmt.Sprintf(":schedule:{schedule-p%02d}:", partition)
	return []string{prefix + "periodic-due", prefix + "periodic-items"}
}

var periodicUpsert = redigo.NewScript(`
local raw=redis.call('HGET',KEYS[2],ARGV[1]);local revision=0;if raw then revision=cjson.decode(raw).revision end
if tostring(revision)~=ARGV[2] then return 0 end
redis.call('HSET',KEYS[2],ARGV[1],ARGV[3]);if ARGV[5]=='1' then redis.call('ZADD',KEYS[1],ARGV[4],ARGV[1]) else redis.call('ZREM',KEYS[1],ARGV[1]) end;return 1
`)

func (s *Schedules) UpsertSchedule(ctx context.Context, p async.PeriodicSchedule, expected uint64) error {
	if s.Connection == nil || s.Connection.Role() == connector.CacheRole {
		return async.ErrInvalid
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Revision != expected+1 {
		return async.ErrInvalid
	}
	p.Owner = ""
	p.LeaseUntil = time.Time{}
	b, err := json.Marshal(p)
	if err != nil || len(b) > async.MaxPayloadBytes {
		return async.ErrInvalid
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

var periodicLease = redigo.NewScript(`
local tm=redis.call('TIME');local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
local ids=redis.call('ZRANGEBYSCORE',KEYS[1],'-inf',now,'LIMIT',0,ARGV[2]);local out={}
for _,id in ipairs(ids) do
 local raw=redis.call('HGET',KEYS[2],id);if not raw then return redis.error_reply('GOGO_MISSING_SCHEDULE') end
 local item=cjson.decode(raw)
 if item.enabled and (item.lease_millis or 0)<=now then item.owner=ARGV[1];item.fence=item.fence+1;item.revision=item.revision+1;item.lease_millis=now+tonumber(ARGV[3]);item.observed_millis=now;raw=cjson.encode(item);redis.call('HSET',KEYS[2],id,raw);redis.call('ZADD',KEYS[1],item.lease_millis,id);table.insert(out,raw) end
end;return out
`)

func (s *Schedules) LeaseSchedules(ctx context.Context, owner string, limit int, lease time.Duration) ([]async.PeriodicSchedule, error) {
	if s.Connection == nil || owner == "" || limit < 1 || limit > 1000 || lease <= 0 {
		return nil, async.ErrInvalid
	}
	var out []async.PeriodicSchedule
	for p := 0; p < 64 && len(out) < limit; p++ {
		values, err := s.Connection.Atomic(ctx, periodicLease, s.periodicKeys(p), owner, limit-len(out), lease.Milliseconds())
		if err != nil {
			return nil, err
		}
		for _, value := range values.([]any) {
			var item struct {
				async.PeriodicSchedule
				LeaseMillis    int64 `json:"lease_millis"`
				ObservedMillis int64 `json:"observed_millis"`
			}
			if err := json.Unmarshal([]byte(value.(string)), &item); err != nil {
				return nil, async.ErrUnavailable
			}
			item.LeaseUntil = time.UnixMilli(item.LeaseMillis)
			item.ObservedAt = time.UnixMilli(item.ObservedMillis)
			out = append(out, item.PeriodicSchedule)
		}
	}
	return out, nil
}

var periodicCommit = redigo.NewScript(`
local raw=redis.call('HGET',KEYS[2],ARGV[1]);if not raw then return 'MISSING' end
local old=cjson.decode(raw);local tm=redis.call('TIME');local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
if tostring(old.revision)~=ARGV[2] or tostring(old.fence)~=ARGV[3] or old.owner~=ARGV[4] or (old.lease_millis or 0)<=now then return 'LEASE' end
redis.call('HSET',KEYS[2],ARGV[1],ARGV[5]);if ARGV[8]=='1' then redis.call('ZADD',KEYS[1],ARGV[6],ARGV[1]) else redis.call('ZREM',KEYS[1],ARGV[1]) end
local intents=cjson.decode(ARGV[7]);for _,intent in ipairs(intents) do
 if redis.call('HEXISTS',KEYS[3],intent.id)==0 then redis.call('HSET',KEYS[3],intent.id,cjson.encode(intent));redis.call('ZADD',KEYS[4],now,intent.source_id..':'..intent.id) end
end;return 'OK'
`)

func (s *Schedules) CommitOccurrence(ctx context.Context, p async.PeriodicSchedule, next time.Time, lastID string, intents []async.Intent) error {
	if next.IsZero() || !next.After(p.NextDue) {
		return async.ErrInvalid
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
	pb, err := json.Marshal(p)
	if err != nil {
		return err
	}
	ib, err := json.Marshal(intents)
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
	if limit < 1 || limit > 1000 || s.Connection == nil {
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
				var p async.PeriodicSchedule
				if err := json.Unmarshal([]byte(values[i]), &p); err != nil {
					return nil, async.ErrUnavailable
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
	if s.Connection == nil {
		return async.ErrInvalid
	}
	raw, err := s.Connection.Client().HGet(ctx, s.periodicKeys(connector.Partition(id))[1], id).Bytes()
	if err != nil {
		return err
	}
	var p async.PeriodicSchedule
	if err := json.Unmarshal(raw, &p); err != nil {
		return async.ErrUnavailable
	}
	if p.Revision != expected {
		return async.ErrConflict
	}
	p.Enabled = false
	p.Revision++
	return s.UpsertSchedule(ctx, p, expected)
}
func (s *Schedules) intentBackend() intentBackend { return intentBackend{s.Connection, "schedule"} }
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
