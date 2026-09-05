package redis

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

type Cursor struct {
	Partition int
	Position  uint64
}
type CleanupReport struct {
	Visited           int
	PayloadsRemoved   int
	TombstonesRemoved int
	Pinned            int
}

var cleanupScript = redigo.NewScript(`
local revision=redis.call('HGET',KEYS[1],'revision');if not revision then return 'MISSING' end
if revision~=ARGV[1] then return 'CONFLICT' end
if ARGV[2]=='delete' then
 if redis.call('HLEN',KEYS[2])>1000 then return 'PINNED' end
 local intents=redis.call('HVALS',KEYS[2]);for _,raw in ipairs(intents) do if not cjson.decode(raw).delivered then return 'PINNED' end end
 redis.call('DEL',KEYS[1],KEYS[2]);redis.call('SREM',KEYS[4],ARGV[4]);return 'DELETED'
end
redis.call('HSET',KEYS[1],'record',ARGV[3],'revision',ARGV[5]);return 'CLEARED'
`)

// Cleanup scans one bounded namespace-owned inventory page. The returned cursor
// must be retained by the caller; cycling from zero on every call can starve
// later records. Active and workflow-pinned records never expire here.
func (r *Results) Cleanup(ctx context.Context, cursor Cursor, count int) (Cursor, CleanupReport, error) {
	var report CleanupReport
	if err := r.valid(); err != nil {
		return cursor, report, err
	}
	if cursor.Partition < 0 || cursor.Partition >= 64 || count < 1 || count > 1000 {
		return cursor, report, async.ErrInvalid
	}
	index, _ := r.Connection.PartitionIndex("task", cursor.Partition, "records")
	ids, next, err := r.Connection.Client().SScan(ctx, index, cursor.Position, "*", int64(count)).Result()
	if err != nil {
		return cursor, report, err
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
		}
	}
	cursor.Position = next
	if next == 0 {
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
