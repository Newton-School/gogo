package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

const workerGroup = "gogo-workers"

type Broker struct {
	Connection *connector.Connection
	Queues     []string
	MaxDepth   int64
	DedupeTTL  time.Duration
	// ReclaimBatch bounds reservations returned to a caller that already owns
	// an execution slot. The default is one; larger prefetch is explicit.
	ReclaimBatch int
	reclaimMu    sync.Mutex
	reclaimNext  map[string]string
}

func (b *Broker) queuePrefix(queue string) string {
	return b.Connection.Namespace() + ":queue:{" + connector.Digest(queue) + "}"
}
func (b *Broker) stream(queue string, priority int) string {
	return b.queuePrefix(queue) + ":p" + strconv.Itoa(priority)
}
func (b *Broker) allowed(queue string) bool {
	if len(b.Queues) == 0 {
		return queue == "default"
	}
	for _, q := range b.Queues {
		if q == queue {
			return true
		}
	}
	return false
}
func (b *Broker) valid() error {
	if b.Connection == nil || b.Connection.Role() != connector.TaskRole {
		return async.ErrInvalid
	}
	return nil
}

var publishScript = redigo.NewScript(`
local old=redis.call('GET',KEYS[2]);if old then
 local parsed=cjson.decode(old);if parsed.digest~=ARGV[2] then return 'CONFLICT' end;return parsed.receipt
end
if redis.call('XLEN',KEYS[1])>=tonumber(ARGV[3]) then return 'FULL' end
local receipt=redis.call('XADD',KEYS[1],'*','body',ARGV[1])
redis.call('SET',KEYS[2],cjson.encode({digest=ARGV[2],receipt=receipt}),'PX',ARGV[4]);return receipt
`)

func (b *Broker) Publish(ctx context.Context, e async.Envelope) error {
	if err := b.valid(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if !b.allowed(e.Queue) {
		return async.ErrInvalid
	}
	depth := b.MaxDepth
	if depth == 0 {
		depth = 100000
	}
	ttl := b.DedupeTTL
	if ttl == 0 {
		ttl = 7 * 24 * time.Hour
	}
	if depth < 1 || ttl < 7*24*time.Hour {
		return async.ErrInvalid
	}
	body, _ := json.Marshal(e)
	key := b.queuePrefix(e.Queue) + ":published:" + e.ID + ":" + strconv.Itoa(e.Retries)
	out, err := b.Connection.Atomic(ctx, publishScript, []string{b.stream(e.Queue, e.Priority), key}, body, e.DispatchDigest(), depth, ttl.Milliseconds())
	if err != nil {
		return err
	}
	switch out {
	case "CONFLICT":
		return async.ErrConflict
	case "FULL":
		return async.ErrBusy
	}
	return nil
}
func (b *Broker) ensure(ctx context.Context, stream string) error {
	err := b.Connection.Client().XGroupCreateMkStream(ctx, stream, workerGroup, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}
func (b *Broker) Consume(ctx context.Context, o async.ConsumeOptions) (async.Delivery, error) {
	if err := b.valid(); err != nil {
		return async.Delivery{}, err
	}
	if o.Consumer == "" || len(o.Queues) == 0 || o.Wait < 0 {
		return async.Delivery{}, async.ErrInvalid
	}
	deadline := time.Now().Add(o.Wait)
	for {
		for priority := 9; priority >= 0; priority-- {
			for _, queue := range o.Queues {
				if !b.allowed(queue) {
					return async.Delivery{}, async.ErrInvalid
				}
				stream := b.stream(queue, priority)
				if err := b.ensure(ctx, stream); err != nil {
					return async.Delivery{}, err
				}
				messages, err := b.Connection.Client().XReadGroup(ctx, &redigo.XReadGroupArgs{Group: workerGroup, Consumer: o.Consumer, Streams: []string{stream, ">"}, Count: 1, Block: -1}).Result()
				if errors.Is(err, redigo.Nil) {
					continue
				}
				if err != nil {
					return async.Delivery{}, err
				}
				for _, batch := range messages {
					for _, message := range batch.Messages {
						return streamDelivery(queue, stream, message, 1), nil
					}
				}
			}
		}
		if !time.Now().Before(deadline) {
			return async.Delivery{}, async.ErrNotFound
		}
		select {
		case <-ctx.Done():
			return async.Delivery{}, ctx.Err()
		case <-time.After(min(20*time.Millisecond, time.Until(deadline))):
		}
	}
}
func streamDelivery(queue, stream string, message redigo.XMessage, count int64) async.Delivery {
	body, _ := message.Values["body"].(string)
	return async.Delivery{Receipt: stream + "\x00" + message.ID, Queue: queue, Body: []byte(body), DeliveryCount: count}
}
func (b *Broker) receipt(d async.Delivery) (string, string, error) {
	if err := b.valid(); err != nil {
		return "", "", err
	}
	stream, id, ok := strings.Cut(d.Receipt, "\x00")
	if !ok || !b.allowed(d.Queue) {
		return "", "", async.ErrInvalid
	}
	valid := false
	for p := 0; p < 10; p++ {
		valid = valid || stream == b.stream(d.Queue, p)
	}
	if !valid || strings.ContainsAny(id, "\x00\r\n") {
		return "", "", async.ErrInvalid
	}
	return stream, id, nil
}

var ackScript = redigo.NewScript(`local n=redis.call('XACK',KEYS[1],ARGV[1],ARGV[2]);if n>0 then redis.call('XDEL',KEYS[1],ARGV[2]) end;return n`)

func (b *Broker) Ack(ctx context.Context, d async.Delivery) error {
	stream, id, err := b.receipt(d)
	if err != nil {
		return err
	}
	_, err = b.Connection.Atomic(ctx, ackScript, []string{stream}, workerGroup, id)
	return err
}

var rejectScript = redigo.NewScript(`
local rows=redis.call('XRANGE',KEYS[1],ARGV[2],ARGV[2]);if #rows==0 then return 0 end
if ARGV[3]=='1' then
 redis.call('XADD',KEYS[1],'*',unpack(rows[1][2]))
else
 redis.call('XADD',KEYS[2],'*','receipt',ARGV[2],'code',ARGV[4],'digest',ARGV[5])
end
redis.call('XACK',KEYS[1],ARGV[1],ARGV[2]);redis.call('XDEL',KEYS[1],ARGV[2]);return 1
`)

func (b *Broker) Reject(ctx context.Context, d async.Delivery, reason string, requeue bool) error {
	stream, id, err := b.receipt(d)
	if err != nil {
		return err
	}
	switch reason {
	case "invalid_envelope", "unknown_task", "invalid_arguments", "task_identity_conflict", "rejected":
	default:
		return async.ErrInvalid
	}
	_, err = b.Connection.Atomic(ctx, rejectScript, []string{stream, b.queuePrefix(d.Queue) + ":quarantine"}, workerGroup, id, map[bool]string{true: "1", false: "0"}[requeue], reason, connector.Digest(string(d.Body)))
	return err
}
func (b *Broker) Reclaim(ctx context.Context, o async.ConsumeOptions, idle time.Duration) ([]async.Delivery, error) {
	if err := b.valid(); err != nil {
		return nil, err
	}
	batch := b.ReclaimBatch
	if batch == 0 {
		batch = 1
	}
	if idle <= 0 || o.Consumer == "" || len(o.Queues) == 0 || batch < 1 || batch > 1000 || o.ReclaimLimit < 0 || o.ReclaimLimit > 1000 {
		return nil, async.ErrInvalid
	}
	if o.ReclaimLimit > 0 {
		batch = min(batch, o.ReclaimLimit)
	}
	b.reclaimMu.Lock()
	defer b.reclaimMu.Unlock()
	if b.reclaimNext == nil {
		b.reclaimNext = map[string]string{}
	}
	var out []async.Delivery
	for _, queue := range o.Queues {
		if !b.allowed(queue) {
			return nil, async.ErrInvalid
		}
		for p := 9; p >= 0; p-- {
			stream := b.stream(queue, p)
			if err := b.ensure(ctx, stream); err != nil {
				return nil, err
			}
			start := b.reclaimNext[stream]
			if start == "" {
				start = "0-0"
			}
			messages, next, err := b.Connection.Client().XAutoClaim(ctx, &redigo.XAutoClaimArgs{Stream: stream, Group: workerGroup, Consumer: o.Consumer, MinIdle: idle, Start: start, Count: int64(batch - len(out))}).Result()
			if err != nil && !errors.Is(err, redigo.Nil) {
				return nil, err
			}
			b.reclaimNext[stream] = next
			for _, message := range messages {
				out = append(out, streamDelivery(queue, stream, message, 2))
			}
			if len(out) >= batch {
				return out, nil
			}
		}
	}
	return out, nil
}
func (b *Broker) Inspect(ctx context.Context, queues []string) ([]async.QueueStats, error) {
	if err := b.valid(); err != nil {
		return nil, err
	}
	var out []async.QueueStats
	for _, queue := range queues {
		if !b.allowed(queue) {
			return nil, async.ErrDenied
		}
		stats := async.QueueStats{Queue: queue}
		for p := 0; p < 10; p++ {
			stream := b.stream(queue, p)
			count, err := b.Connection.Client().XLen(ctx, stream).Result()
			if err != nil {
				return nil, err
			}
			stats.Queued += count
			pending, err := b.Connection.Client().XPending(ctx, stream, workerGroup).Result()
			if err == nil {
				stats.Pending += pending.Count
			} else if !strings.Contains(err.Error(), "NOGROUP") {
				return nil, err
			}
		}
		stats.Queued -= stats.Pending
		q, err := b.Connection.Client().XLen(ctx, b.queuePrefix(queue)+":quarantine").Result()
		if err != nil {
			return nil, err
		}
		stats.Quarantined = q
		out = append(out, stats)
	}
	return out, nil
}

// Close releases the role-owned connection. Call once for shared adapter roles.
func (b *Broker) Close() error {
	if b.Connection == nil {
		return fmt.Errorf("%w: connection", async.ErrInvalid)
	}
	return b.Connection.Close()
}

var _ async.Broker = (*Broker)(nil)
