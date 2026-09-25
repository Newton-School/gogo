package redis

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

// DashboardCatalog reads the same connections used by the actual execution
// stores. It owns no credentials, execution goroutines, or result cache.
// Configure once; do not change the stores while requests are in flight.
type DashboardCatalog struct {
	Results   *Results
	Workflows *Workflows
	Workers   *Workers
	Schedules *Schedules
	Beats     *Beats
}

func dashboardCursor(kind, cursor string) (int, string, error) {
	if cursor == "" {
		return 0, "", nil
	}
	parts := strings.SplitN(cursor, "/", 4)
	if len(cursor) > 512 || len(parts) != 4 || parts[0] != "dash1" || parts[1] != kind {
		return 0, "", async.ErrInvalid
	}
	p, err := strconv.Atoi(parts[2])
	if err != nil || p < 0 || p >= 64 || strconv.Itoa(p) != parts[2] {
		return 0, "", async.ErrInvalid
	}
	if kind == "workers" || kind == "schedulers" {
		offset, err := strconv.ParseUint(parts[3], 10, 32)
		if err != nil || strconv.FormatUint(offset, 10) != parts[3] {
			return 0, "", async.ErrInvalid
		}
	} else if parts[3] != "" && (async.DashboardIDs{IDs: []string{parts[3]}}).Validate(kind, 1) != nil {
		return 0, "", async.ErrInvalid
	}
	return p, parts[3], nil
}

// ListDashboard reads one indexed partition, never KEYS/SCAN. Task/workflow
// indexes already accompany state CAS writes. Schedule discovery starts with
// the next save/tick after upgrading; known-ID lookup also works on older data.
// Worker/Beat indexes retain 24h of observations and are pruned on writes.
func (d *DashboardCatalog) ListDashboard(ctx context.Context, kind, cursor string, limit int) (async.DashboardIDs, error) {
	if ctx == nil || d == nil || limit < 1 || limit > 100 {
		return async.DashboardIDs{}, async.ErrInvalid
	}
	partition, after, err := dashboardCursor(kind, cursor)
	if err != nil {
		return async.DashboardIDs{}, err
	}
	var connection *connector.Connection
	var family, suffix string
	presence := false
	switch kind {
	case "tasks":
		if d.Results != nil {
			connection = d.Results.Connection
		}
		family, suffix = "task", "records-v1"
	case "workflows":
		if d.Workflows != nil {
			connection = d.Workflows.Connection
		}
		family, suffix = "workflow", "records-v1"
	case "beat":
		if d.Schedules != nil {
			connection = d.Schedules.Connection
		}
		family, suffix = "schedule", "dashboard-v1"
	case "workers":
		if d.Workers != nil {
			connection = d.Workers.Connection
		}
		family, suffix, presence = "worker", "dashboard-v1", true
	case "schedulers":
		if d.Beats != nil {
			connection = d.Beats.Connection
		}
		family, suffix, presence = "beat", "dashboard-v1", true
	default:
		return async.DashboardIDs{}, async.ErrInvalid
	}
	if connection == nil || connection.Role() == connector.CacheRole {
		return async.DashboardIDs{}, async.ErrUnavailable
	}
	key, err := connection.PartitionIndex(family, partition, suffix)
	if err != nil {
		return async.DashboardIDs{}, err
	}
	var ids []string
	if presence {
		now, err := connector.ServerTime(ctx, connection.Client())
		if err != nil {
			return async.DashboardIDs{}, err
		}
		offset, _ := strconv.ParseInt(after, 10, 64)
		ids, err = connection.Client().ZRangeByScore(ctx, key, &redigo.ZRangeBy{Min: "(" + strconv.FormatInt(now.UnixMilli(), 10), Max: "+inf", Offset: offset, Count: int64(limit)}).Result()
		if err != nil {
			return async.DashboardIDs{}, err
		}
		after = strconv.FormatInt(offset+int64(len(ids)), 10)
	} else {
		minimum := "-"
		if after != "" {
			minimum = "(" + after
		}
		ids, err = connection.Client().ZRangeByLex(ctx, key, &redigo.ZRangeBy{Min: minimum, Max: "+", Count: int64(limit)}).Result()
		if err != nil {
			return async.DashboardIDs{}, err
		}
		if len(ids) > 0 {
			after = ids[len(ids)-1]
		}
	}
	page := async.DashboardIDs{IDs: ids}
	if len(ids) == limit {
		page.NextCursor = fmt.Sprintf("dash1/%s/%d/%s", kind, partition, after)
	} else if partition < 63 {
		position := ""
		if presence {
			position = "0"
		}
		page.NextCursor = fmt.Sprintf("dash1/%s/%d/%s", kind, partition+1, position)
	}
	if page.Validate(kind, limit) != nil {
		return async.DashboardIDs{}, async.ErrUnavailable
	}
	for _, id := range ids {
		if connector.Partition(id) != partition {
			return async.DashboardIDs{}, async.ErrUnavailable
		}
	}
	return page, nil
}

func (d *DashboardCatalog) ReadDashboardSchedule(ctx context.Context, id string) (async.PeriodicSchedule, error) {
	if ctx == nil || d == nil || d.Schedules == nil || !d.Schedules.valid() || (async.DashboardIDs{IDs: []string{id}}).Validate("beat", 1) != nil {
		return async.PeriodicSchedule{}, async.ErrInvalid
	}
	raw, err := d.Schedules.Connection.Client().HGet(ctx, d.Schedules.periodicKeys(connector.Partition(id))[1], id).Bytes()
	if err == redigo.Nil {
		return async.PeriodicSchedule{}, async.ErrNotFound
	}
	if err != nil {
		return async.PeriodicSchedule{}, err
	}
	p, err := unmarshalPeriodic(raw)
	if err != nil || p.ID != id {
		return async.PeriodicSchedule{}, async.ErrUnavailable
	}
	return p, nil
}

func (d *DashboardCatalog) ReadDashboardBeat(ctx context.Context, id string) (async.BeatObservation, error) {
	if d == nil || d.Beats == nil {
		return async.BeatObservation{}, async.ErrUnavailable
	}
	return d.Beats.Lookup(ctx, id)
}

// Beats stores best-effort scheduler health separately from schedule leases.
// Use a unique Beat.ID per runner; these records never grant scheduler authority.
type Beats struct{ Connection *connector.Connection }

var beatObserve = redigo.NewScript(`
for i,key in ipairs(KEYS) do local k=redis.call('TYPE',key).ok;local wanted='hash';if i==2 then wanted='zset' end;if k~='none' and k~=wanted then return redis.error_reply('invalid beat monitor key') end end
local t=redis.call('TIME');local now=tonumber(t[1])*1000+math.floor(tonumber(t[2])/1000)
redis.call('HSET',KEYS[1],'status',ARGV[2],'observed',string.format('%.0f',now),'expires',string.format('%.0f',now+tonumber(ARGV[3])))
redis.call('PEXPIRE',KEYS[1],86400000)
redis.call('ZREMRANGEBYSCORE',KEYS[2],'-inf',now)
redis.call('ZADD',KEYS[2],now+86400000,ARGV[1]);redis.call('PEXPIRE',KEYS[2],86400000)
return 'OK'
`)

func (b *Beats) ObserveBeat(ctx context.Context, id, status string, ttl time.Duration) error {
	if ctx == nil || b == nil || b.Connection == nil || b.Connection.Role() == connector.CacheRole || !async.ValidWorkerID(id) || ttl < time.Millisecond || ttl > time.Hour || status != "online" && status != "error" && status != "offline" {
		return async.ErrInvalid
	}
	index, _ := b.Connection.PartitionIndex("beat", connector.Partition(id), "dashboard-v1")
	_, err := b.Connection.Atomic(ctx, beatObserve, []string{b.Connection.PartitionKey("beat", id, "presence"), index}, id, status, ttl.Milliseconds())
	return err
}

func (b *Beats) Lookup(ctx context.Context, id string) (async.BeatObservation, error) {
	if ctx == nil || b == nil || b.Connection == nil || b.Connection.Role() == connector.CacheRole || !async.ValidWorkerID(id) {
		return async.BeatObservation{}, async.ErrInvalid
	}
	values, err := b.Connection.Client().HMGet(ctx, b.Connection.PartitionKey("beat", id, "presence"), "status", "observed", "expires").Result()
	if err != nil {
		return async.BeatObservation{}, err
	}
	if values[0] == nil {
		return async.BeatObservation{}, async.ErrNotFound
	}
	status, ok := values[0].(string)
	observed, ok1 := values[1].(string)
	expires, ok2 := values[2].(string)
	o, e1 := strconv.ParseInt(observed, 10, 64)
	e, e2 := strconv.ParseInt(expires, 10, 64)
	if !ok || !ok1 || !ok2 || e1 != nil || e2 != nil || o <= 0 || e <= o || status != "online" && status != "error" && status != "offline" {
		return async.BeatObservation{}, async.ErrUnavailable
	}
	now, err := connector.ServerTime(ctx, b.Connection.Client())
	if err != nil {
		return async.BeatObservation{}, err
	}
	if status != "offline" && e <= now.UnixMilli() {
		status = "lost"
	}
	return async.BeatObservation{ID: id, Status: status, ObservedAt: time.UnixMilli(o).UTC(), ExpiresAt: time.UnixMilli(e).UTC()}, nil
}

var _ async.DashboardCatalog = (*DashboardCatalog)(nil)
var _ async.BeatMonitor = (*Beats)(nil)
