// Package redis implements Async ports using Redis Streams and same-slot fenced
// transitions. Broker, results, workflow and schedule roles may use separate servers.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

type Results struct {
	Connection   *connector.Connection
	ResultTTL    time.Duration
	TombstoneTTL time.Duration
}

func (r *Results) valid() error {
	if r.Connection == nil || r.Connection.Role() == connector.CacheRole {
		return async.ErrInvalid
	}
	return nil
}
func (r *Results) keys(id string) []string {
	index, _ := r.Connection.PartitionIndex("task", connector.Partition(id), "pending-intents")
	return []string{r.Connection.PartitionKey("task", id, "state"), r.Connection.PartitionKey("task", id, "intents"), index}
}
func (r *Results) read(ctx context.Context, id string) (async.Record, error) {
	var record async.Record
	if err := r.valid(); err != nil {
		return record, err
	}
	values, err := r.Connection.Client().HMGet(ctx, r.keys(id)[0], "record", "lease_millis").Result()
	if err != nil {
		return record, err
	}
	if values[0] == nil {
		return record, async.ErrNotFound
	}
	if err := json.Unmarshal([]byte(values[0].(string)), &record); err != nil {
		return record, async.ErrUnavailable
	}
	if values[1] != nil {
		n, err := strconv.ParseInt(values[1].(string), 10, 64)
		if err != nil {
			return record, async.ErrUnavailable
		}
		if n > 0 {
			record.LeaseUntil = time.UnixMilli(n)
		} else {
			record.LeaseUntil = time.Time{}
		}
	}
	return record, nil
}
func (r *Results) Lookup(ctx context.Context, id string) (async.Record, error) {
	record, err := r.read(ctx, id)
	if err != nil {
		return record, err
	}
	if record.State.Terminal() && !record.Pinned {
		now, err := connector.ServerTime(ctx, r.Connection.Client())
		if err != nil {
			return record, err
		}
		if !record.PayloadExpiresAt.After(now) {
			record.Output = nil
		}
	}
	return record, nil
}

var recordCAS = redigo.NewScript(`
local old=redis.call('HGET',KEYS[1],'revision')
if not old then old='0' end
if old~=ARGV[1] then return 'CONFLICT' end
local tm=redis.call('TIME');local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
local lease=tonumber(redis.call('HGET',KEYS[1],'lease_millis') or '0')
if ARGV[6]=='claim' and lease>now then return 'LEASE' end
if ARGV[6]=='live' then
 if lease<=now or redis.call('HGET',KEYS[1],'owner')~=ARGV[7] or redis.call('HGET',KEYS[1],'fence')~=ARGV[8] then return 'LEASE' end
end
local newlease=tonumber(ARGV[4]);if ARGV[5]~='0' then newlease=now+tonumber(ARGV[5]) end
redis.call('HSET',KEYS[1],'record',ARGV[2],'revision',ARGV[3],'lease_millis',newlease,'owner',ARGV[9],'fence',ARGV[10])
local intents=cjson.decode(ARGV[11]);for _,intent in ipairs(intents) do
 if redis.call('HEXISTS',KEYS[2],intent.id)==0 then redis.call('HSET',KEYS[2],intent.id,cjson.encode(intent));redis.call('ZADD',KEYS[3],now,intent.source_id..':'..intent.id) end
end
return 'OK'
`)

func (r *Results) cas(ctx context.Context, expected uint64, record async.Record, mode string, oldFence uint64, oldOwner string, lease time.Duration, intents []async.Intent) error {
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if intents == nil {
		intents = []async.Intent{}
	}
	ib, err := json.Marshal(intents)
	if err != nil {
		return err
	}
	newLease := int64(0)
	if !record.LeaseUntil.IsZero() {
		newLease = record.LeaseUntil.UnixMilli()
	}
	out, err := r.Connection.Atomic(ctx, recordCAS, r.keys(record.Envelope.ID), strconv.FormatUint(expected, 10), b, strconv.FormatUint(record.Revision, 10), newLease, lease.Milliseconds(), mode, oldOwner, strconv.FormatUint(oldFence, 10), record.Owner, strconv.FormatUint(record.Fence, 10), ib)
	if err != nil {
		return err
	}
	switch out {
	case "OK":
		return nil
	case "LEASE":
		return async.ErrLeaseLost
	default:
		return async.ErrConflict
	}
}
func (r *Results) Register(ctx context.Context, e async.Envelope, state async.State) error {
	if err := r.valid(); err != nil {
		return err
	}
	record, err := async.InitialRecord(e, state)
	if err != nil {
		return err
	}
	err = r.cas(ctx, 0, record, "create", 0, "", 0, nil)
	if errors.Is(err, async.ErrConflict) {
		old, err := r.read(ctx, e.ID)
		if err != nil {
			return err
		}
		if old.Digest != record.Digest {
			return async.ErrConflict
		}
		return nil
	}
	return err
}
func (r *Results) Claim(ctx context.Context, e async.Envelope, owner string, lease time.Duration) (async.Claim, error) {
	if err := r.valid(); err != nil {
		return async.Claim{}, err
	}
	for attempt := 0; attempt < 16; attempt++ {
		now, err := connector.ServerTime(ctx, r.Connection.Client())
		if err != nil {
			return async.Claim{}, err
		}
		record, err := r.read(ctx, e.ID)
		expected := record.Revision
		if errors.Is(err, async.ErrNotFound) {
			record, err = async.InitialRecord(e, async.Queued)
			expected = 0
		}
		if err != nil {
			return async.Claim{}, err
		}
		claim, err := async.ClaimRecord(record, e, owner, now, lease)
		if err != nil || !claim.Acquired {
			return claim, err
		}
		err = r.cas(ctx, expected, claim.Record, "claim", 0, "", lease, nil)
		if errors.Is(err, async.ErrConflict) || errors.Is(err, async.ErrLeaseLost) {
			continue
		}
		return claim, err
	}
	return async.Claim{}, async.ErrBusy
}
func (r *Results) Renew(ctx context.Context, id string, fence uint64, owner string, lease time.Duration) (async.Record, error) {
	for attempt := 0; attempt < 16; attempt++ {
		now, err := connector.ServerTime(ctx, r.Connection.Client())
		if err != nil {
			return async.Record{}, err
		}
		record, err := r.read(ctx, id)
		if err != nil {
			return record, err
		}
		if err := async.ValidateLease(record, fence, owner, now); err != nil {
			return record, err
		}
		expected := record.Revision
		record.Revision++
		record.LeaseUntil = now.Add(lease)
		err = r.cas(ctx, expected, record, "live", fence, owner, lease, nil)
		if errors.Is(err, async.ErrConflict) {
			continue
		}
		return record, err
	}
	return async.Record{}, async.ErrBusy
}
func (r *Results) Transition(ctx context.Context, t async.Transition) error {
	if err := r.valid(); err != nil {
		return err
	}
	resultTTL := r.ResultTTL
	if resultTTL == 0 {
		resultTTL = 24 * time.Hour
	}
	tombstoneTTL := r.TombstoneTTL
	if tombstoneTTL == 0 {
		tombstoneTTL = 7 * 24 * time.Hour
	}
	if resultTTL <= 0 || tombstoneTTL < resultTTL {
		return async.ErrInvalid
	}
	for attempt := 0; attempt < 16; attempt++ {
		now, err := connector.ServerTime(ctx, r.Connection.Client())
		if err != nil {
			return err
		}
		record, err := r.read(ctx, t.ID)
		if err != nil {
			return err
		}
		next, err := async.ApplyTransition(record, t, now, resultTTL, tombstoneTTL)
		if err != nil {
			return err
		}
		err = r.cas(ctx, record.Revision, next, "live", t.Fence, t.Owner, 0, t.Intents)
		if errors.Is(err, async.ErrConflict) {
			continue
		}
		return err
	}
	return async.ErrBusy
}
func (r *Results) RecordProgress(ctx context.Context, id string, fence uint64, owner string, b json.RawMessage) error {
	if len(b) > 16<<10 || !json.Valid(b) {
		return async.ErrInvalid
	}
	for attempt := 0; attempt < 16; attempt++ {
		now, err := connector.ServerTime(ctx, r.Connection.Client())
		if err != nil {
			return err
		}
		record, err := r.read(ctx, id)
		if err != nil {
			return err
		}
		if err := async.ValidateLease(record, fence, owner, now); err != nil {
			return err
		}
		expected := record.Revision
		record.Progress = append(json.RawMessage(nil), b...)
		record.Revision++
		err = r.cas(ctx, expected, record, "live", fence, owner, 0, nil)
		if errors.Is(err, async.ErrConflict) {
			continue
		}
		return err
	}
	return async.ErrBusy
}
func (r *Results) RequestCancel(ctx context.Context, id, scope string) error {
	for attempt := 0; attempt < 16; attempt++ {
		record, err := r.read(ctx, id)
		if err != nil {
			return err
		}
		if record.Envelope.Scope != scope {
			return async.ErrDenied
		}
		if record.State.Terminal() {
			return nil
		}
		expected := record.Revision
		record.CancelRequested = true
		record.Revision++
		err = r.cas(ctx, expected, record, "update", 0, "", 0, nil)
		if errors.Is(err, async.ErrConflict) {
			continue
		}
		return err
	}
	return async.ErrBusy
}
func (r *Results) Forget(ctx context.Context, id string) error {
	for attempt := 0; attempt < 16; attempt++ {
		record, err := r.read(ctx, id)
		if err != nil {
			return err
		}
		if record.Pinned || !record.State.Terminal() {
			return async.ErrPinned
		}
		expected := record.Revision
		record.Output = nil
		record.Progress = nil
		record.Revision++
		err = r.cas(ctx, expected, record, "update", 0, "", 0, nil)
		if errors.Is(err, async.ErrConflict) {
			continue
		}
		return err
	}
	return async.ErrBusy
}
func (r *Results) ReleasePin(ctx context.Context, id string) error {
	for attempt := 0; attempt < 16; attempt++ {
		record, err := r.read(ctx, id)
		if err != nil {
			return err
		}
		if !record.State.Terminal() {
			return async.ErrPinned
		}
		expected := record.Revision
		record.Pinned = false
		record.Revision++
		err = r.cas(ctx, expected, record, "update", 0, "", 0, nil)
		if errors.Is(err, async.ErrConflict) {
			continue
		}
		return err
	}
	return async.ErrBusy
}

type intentBackend struct {
	connection *connector.Connection
	family     string
}

func (r *Results) intentBackend() intentBackend { return intentBackend{r.Connection, "task"} }
func (r *Results) ListIntents(ctx context.Context, limit int) ([]async.Intent, error) {
	return r.intentBackend().list(ctx, limit)
}
func (r *Results) ClaimIntent(ctx context.Context, source, id, owner string, lease time.Duration) (async.Intent, error) {
	return r.intentBackend().claim(ctx, source, id, owner, lease)
}
func (r *Results) MarkIntentDelivered(ctx context.Context, source, id string, fence uint64, owner string) error {
	return r.intentBackend().mark(ctx, source, id, fence, owner)
}
func (b intentBackend) keys(source string) []string {
	index, _ := b.connection.PartitionIndex(b.family, connector.Partition(source), "pending-intents")
	return []string{b.connection.PartitionKey(b.family, source, "intents"), index}
}
func (b intentBackend) list(ctx context.Context, limit int) ([]async.Intent, error) {
	if limit < 1 || limit > 1000 {
		return nil, async.ErrInvalid
	}
	var out []async.Intent
	for p := 0; p < 64 && len(out) < limit; p++ {
		index, _ := b.connection.PartitionIndex(b.family, p, "pending-intents")
		ids, err := b.connection.Client().ZRange(ctx, index, 0, int64(limit-len(out)-1)).Result()
		if err != nil {
			return nil, err
		}
		for _, reference := range ids {
			source, id, ok := strings.Cut(reference, ":")
			if !ok {
				return nil, async.ErrUnavailable
			}
			raw, err := b.connection.Client().HGet(ctx, b.keys(source)[0], id).Result()
			if err != nil {
				return nil, err
			}
			var intent async.Intent
			if err := json.Unmarshal([]byte(raw), &intent); err != nil {
				return nil, async.ErrUnavailable
			}
			out = append(out, intent)
		}
	}
	return out, nil
}

var intentClaim = redigo.NewScript(`
local raw=redis.call('HGET',KEYS[1],ARGV[1]);if not raw then return 'MISSING' end
local item=cjson.decode(raw);if item.delivered then return raw end
local tm=redis.call('TIME');local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
if (item.lease_millis or 0)>now then return 'BUSY' end
item.lease_millis=now+tonumber(ARGV[3]);item.fence=item.fence+1;item.owner=ARGV[2]
raw=cjson.encode(item);redis.call('HSET',KEYS[1],ARGV[1],raw);return raw
`)

func (b intentBackend) claim(ctx context.Context, source, id, owner string, lease time.Duration) (async.Intent, error) {
	if owner == "" || lease <= 0 {
		return async.Intent{}, async.ErrInvalid
	}
	out, err := b.connection.Atomic(ctx, intentClaim, b.keys(source), id, owner, lease.Milliseconds())
	if err != nil {
		return async.Intent{}, err
	}
	raw := out.(string)
	if raw == "BUSY" {
		return async.Intent{}, async.ErrBusy
	}
	if raw == "MISSING" {
		return async.Intent{}, async.ErrNotFound
	}
	var item struct {
		async.Intent
		LeaseMillis int64 `json:"lease_millis"`
	}
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return async.Intent{}, async.ErrUnavailable
	}
	item.LeaseUntil = time.UnixMilli(item.LeaseMillis)
	return item.Intent, nil
}

var intentMark = redigo.NewScript(`
local raw=redis.call('HGET',KEYS[1],ARGV[1]);if not raw then return 'MISSING' end
local item=cjson.decode(raw);if item.delivered then return 'OK' end
local tm=redis.call('TIME');local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
if item.owner~=ARGV[3] or tostring(item.fence)~=ARGV[2] or (item.lease_millis or 0)<=now then return 'LEASE' end
item.delivered=true;redis.call('HSET',KEYS[1],ARGV[1],cjson.encode(item));redis.call('ZREM',KEYS[2],item.source_id..':'..item.id);return 'OK'
`)

func (b intentBackend) mark(ctx context.Context, source, id string, fence uint64, owner string) error {
	out, err := b.connection.Atomic(ctx, intentMark, b.keys(source), id, strconv.FormatUint(fence, 10), owner)
	if err != nil {
		return err
	}
	switch out {
	case "OK":
		return nil
	case "LEASE":
		return async.ErrLeaseLost
	default:
		return async.ErrNotFound
	}
}

var _ async.ResultStore = (*Results)(nil)
