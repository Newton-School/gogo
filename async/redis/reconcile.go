package redis

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

type Cursor struct {
	Partition int
	AfterID   string
	// Position belongs to the old SSCAN cursor format. Nonzero values are
	// rejected; they cannot be interpreted as lexicographic positions.
	Position uint64
}
type CleanupReport struct {
	Visited           int
	PayloadsRemoved   int
	TombstonesRemoved int
	Pinned            int
}

var cleanupScript = redigo.NewScript(`
local kinds={'hash','hash','zset','zset'}
for i,key in ipairs(KEYS) do local kind=redis.call('TYPE',key).ok;if kind~='none' and kind~=kinds[i] then error('invalid task cleanup key type') end end
local revision=redis.call('HGET',KEYS[1],'revision');if not revision then return 'MISSING' end
if revision~=ARGV[1] then return 'CONFLICT' end
if ARGV[2]=='delete' then
 if redis.call('HLEN',KEYS[2])>1000 then return 'PINNED' end
 local intents=redis.call('HGETALL',KEYS[2]);local pending=false
 for i=1,#intents,2 do
  local item=cjson.decode(intents[i+1])
  if type(item)~='table' or item.format~=1 or type(item.id)~='string' or #item.id~=36 or item.id~=intents[i] or item.source_id~=ARGV[4] or type(item.payload)~='string' or #item.payload==0 or type(item.delivered)~='boolean' then error('invalid task cleanup intent') end
  if not item.delivered then pending=true end
 end
 if pending then return 'PINNED' end
 redis.call('DEL',KEYS[1],KEYS[2]);redis.call('ZREM',KEYS[4],ARGV[4]);return 'DELETED'
end
if ARGV[2]~='payload' then error('invalid task cleanup mode') end
redis.call('HSET',KEYS[1],'record',ARGV[3],'revision',ARGV[5]);return 'CLEARED'
`)

// Cleanup reads at most count task IDs from one configured partition's sorted
// inventory. Retain the returned cursor; restarting each call can starve later
// records. Concurrent inserts before AfterID are visited on the next pass.
// Active and workflow-pinned records never expire here. This is a trusted
// maintenance port, not a user-facing enumeration API. Errors retain the input
// cursor and can include already-applied, idempotent per-record cleanup; this is
// not an atomic whole-page transaction. Legacy inventories require migration.
func (r *Results) Cleanup(ctx context.Context, cursor Cursor, count int) (Cursor, CleanupReport, error) {
	var report CleanupReport
	if err := r.valid(); err != nil {
		return cursor, report, err
	}
	if ctx == nil || cursor.Partition < 0 || cursor.Partition >= 64 || cursor.Position != 0 || count < 1 || count > 1000 {
		return cursor, report, async.ErrInvalid
	}
	if cursor.AfterID != "" && ((async.WorkflowInventoryPage{IDs: []string{cursor.AfterID}}).Validate(1) != nil || connector.Partition(cursor.AfterID) != cursor.Partition) {
		return cursor, report, async.ErrInvalid
	}
	legacy, _ := r.Connection.PartitionIndex("task", cursor.Partition, "records")
	exists, err := r.Connection.Client().Exists(ctx, legacy).Result()
	if err != nil {
		return cursor, report, err
	}
	if exists != 0 {
		return cursor, report, errors.New("async: legacy task inventory requires explicit migration")
	}
	index, _ := r.Connection.PartitionIndex("task", cursor.Partition, "records-v1")
	minimum := "-"
	if cursor.AfterID != "" {
		minimum = "(" + cursor.AfterID
	}
	ids, err := r.Connection.Client().ZRangeByLex(ctx, index, &redigo.ZRangeBy{Min: minimum, Max: "+", Count: int64(count)}).Result()
	if err != nil {
		return cursor, report, err
	}
	if (async.WorkflowInventoryPage{IDs: ids}).Validate(count) != nil {
		return cursor, report, async.ErrUnavailable
	}
	for _, id := range ids {
		if connector.Partition(id) != cursor.Partition {
			return cursor, report, async.ErrUnavailable
		}
	}
	now, err := connector.ServerTime(ctx, r.Connection.Client())
	if err != nil {
		return cursor, report, err
	}
	for _, id := range ids {
		record, err := r.read(ctx, id)
		if errors.Is(err, async.ErrNotFound) {
			return cursor, report, errors.New("async: authoritative task record missing from inventory")
		}
		if err != nil {
			return cursor, report, err
		}
		if record.Envelope.ID != id || record.Revision == 0 || record.Envelope.Validate() != nil || record.Digest != record.Envelope.Digest() {
			return cursor, report, async.ErrUnavailable
		}
		if !record.State.Terminal() && record.State != async.Scheduled && record.State != async.Queued && record.State != async.Running && record.State != async.RetryWait {
			return cursor, report, async.ErrUnavailable
		}
		if record.State.Terminal() && (record.FinishedAt.IsZero() || record.PayloadExpiresAt.IsZero() || record.TombstoneUntil.IsZero() || record.TombstoneUntil.Before(record.PayloadExpiresAt)) {
			return cursor, report, async.ErrUnavailable
		}
		report.Visited++
		if !record.State.Terminal() || record.Pinned {
			report.Pinned++
			continue
		}
		mode := "payload"
		if !record.TombstoneUntil.After(now) {
			mode = "delete"
		} else if record.PayloadExpiresAt.After(now) || len(record.Output) == 0 && len(record.Progress) == 0 {
			continue
		}
		expected := record.Revision
		if expected == math.MaxUint64 {
			return cursor, report, async.ErrUnavailable
		}
		record.Output = nil
		record.Progress = nil
		record.Revision++
		raw, err := json.Marshal(record)
		if err != nil {
			return cursor, report, err
		}
		out, err := r.Connection.Atomic(ctx, cleanupScript, r.keys(id), strconv.FormatUint(expected, 10), mode, raw, id, strconv.FormatUint(record.Revision, 10))
		if err != nil {
			return cursor, report, err
		}
		switch out {
		case "CLEARED":
			report.PayloadsRemoved++
		case "DELETED":
			report.TombstonesRemoved++
		case "PINNED":
			report.Pinned++
		case "CONFLICT":
			continue
		case "MISSING":
			return cursor, report, async.ErrNotFound
		default:
			return cursor, report, async.ErrUnavailable
		}
	}
	if len(ids) == count {
		cursor.AfterID = ids[len(ids)-1]
	} else {
		cursor.AfterID = ""
		cursor.Partition = (cursor.Partition + 1) % 64
	}
	return cursor, report, nil
}

// Reconciler composes the same idempotent recovery operations used during normal
// execution. A missing authoritative record is reported, never guessed complete.
type Reconciler struct {
	Worker        *async.Worker
	Relay         *async.IntentRelay
	Delayed       *async.DelayedDispatcher
	Results       *Results
	Workflows     *async.WorkflowReconciler
	CleanupCursor Cursor
	CleanupBatch  int
}

func (r *Reconciler) Tick(ctx context.Context) error {
	if r.Worker != nil {
		if err := r.Worker.Reclaim(ctx); err != nil {
			return err
		}
	}
	if r.Relay != nil {
		if err := r.Relay.Tick(ctx); err != nil {
			return err
		}
	}
	if r.Delayed != nil {
		if err := r.Delayed.Tick(ctx); err != nil {
			return err
		}
	}
	if r.Results != nil {
		count := r.CleanupBatch
		if count == 0 {
			count = 100
		}
		cursor, _, err := r.Results.Cleanup(ctx, r.CleanupCursor, count)
		if err != nil {
			return err
		}
		r.CleanupCursor = cursor
	}
	if r.Workflows != nil {
		if err := r.Workflows.Tick(ctx); err != nil {
			return err
		}
	}
	return nil
}
func (r *Reconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := r.Tick(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
