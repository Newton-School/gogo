// Package redis provides a real Redis-backed queue broker for Gogo workers.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/brokers"
	redisclient "github.com/redis/go-redis/v9"
)

const defaultVisibilityTimeout = time.Minute

type Config struct {
	URL               string
	Prefix            string
	VisibilityTimeout time.Duration
	PriorityBuckets   int
	Client            *redisclient.Client
}

type Broker struct {
	config Config
	client *redisclient.Client
	closed atomic.Bool
}

type Keys struct {
	Ready      string
	Delayed    string
	Unacked    string
	DeadLetter string
	Payloads   string
	Queues     string
	Meta       string
	InFlight   string

	prefix string
	queue  string
}

func init() {
	queue.RegisterBrokerFactory("redis", func(config queue.RuntimeConfig) (queue.Broker, error) {
		return NewBrokerFromURL(config.BrokerURL)
	})
	queue.RegisterBrokerFactory("rediss", func(config queue.RuntimeConfig) (queue.Broker, error) {
		return NewBrokerFromURL(config.BrokerURL)
	})
}

func NewBroker(config Config) *Broker {
	broker, _ := newBroker(config, false)
	return broker
}

func NewBrokerFromURL(rawURL string) (*Broker, error) {
	return newBroker(Config{URL: rawURL}, true)
}

func newBroker(config Config, strictURL bool) (*Broker, error) {
	if config.Prefix == "" {
		config.Prefix = "gogo"
	}
	if config.PriorityBuckets <= 0 {
		config.PriorityBuckets = 10
	}
	if config.VisibilityTimeout <= 0 {
		config.VisibilityTimeout = defaultVisibilityTimeout
	}
	client := config.Client
	if client == nil && strings.TrimSpace(config.URL) != "" {
		options, err := redisclient.ParseURL(config.URL)
		if err != nil {
			if strictURL {
				return nil, fmt.Errorf("%w: parse Redis broker URL %q: %v", queue.ErrUnsupportedRuntimeURL, config.URL, err)
			}
		} else {
			client = redisclient.NewClient(options)
		}
	}
	return &Broker{config: config, client: client}, nil
}

func (b *Broker) Keys(queueName string) Keys {
	queueName = normalizeQueueName(queueName)
	base := b.config.Prefix + ":queue:" + queueName
	return Keys{
		Ready:      base + ":ready",
		Delayed:    base + ":delayed",
		Unacked:    base + ":unacked",
		DeadLetter: base + ":dead",
		Payloads:   base + ":payloads",
		Queues:     b.config.Prefix + ":queues",
		Meta:       base + ":meta",
		InFlight:   base + ":inflight",
		prefix:     b.config.Prefix,
		queue:      queueName,
	}
}

func (k Keys) Priority(priority int) string {
	if priority < 0 {
		priority = 0
	}
	return k.prefix + ":queue:" + k.queue + ":priority:" + strconvItoa(priority)
}

func EncodeEnvelope(envelope queue.Envelope) ([]byte, error) {
	registry := queue.NewSerializationRegistry(queue.SerializationOptions{})
	payload, err := registry.Encode("json", envelope, queue.CompressionNone)
	if err != nil {
		return nil, err
	}
	return payload.Body, nil
}

func DecodeEnvelope(data []byte) (queue.Envelope, error) {
	registry := queue.NewSerializationRegistry(queue.SerializationOptions{})
	payload := queue.Payload{Serializer: "json", Compression: queue.CompressionNone, Body: data}
	var envelope queue.Envelope
	if err := registry.Decode(payload, &envelope); err != nil {
		return queue.Envelope{}, err
	}
	return envelope, nil
}

func (b *Broker) Publish(ctx context.Context, queueName string, envelope queue.Envelope, options brokers.PublishOptions) (brokers.Message, error) {
	if err := b.ensureOpen(); err != nil {
		return brokers.Message{}, err
	}
	client, err := b.redisClient()
	if err != nil {
		return brokers.Message{}, err
	}
	queueName = normalizeQueueName(queueName)
	if envelope.ID == "" {
		sequence, err := client.Incr(ctx, b.Keys(queueName).Meta+":ids").Result()
		if err != nil {
			return brokers.Message{}, err
		}
		envelope.ID = fmt.Sprintf("redis-%d", sequence)
	}
	now := time.Now().UTC()
	visibleAt := now
	if envelope.ETA != nil {
		visibleAt = envelope.ETA.UTC()
	}
	message := brokers.Message{
		Queue:     queueName,
		Envelope:  envelope,
		Priority:  options.Priority,
		Attempts:  envelope.Retries,
		VisibleAt: visibleAt,
	}
	encoded, err := encodeMessage(message)
	if err != nil {
		return brokers.Message{}, err
	}
	keys := b.Keys(queueName)
	priority := b.priorityBucket(options.Priority)
	pipe := client.Pipeline()
	pipe.SAdd(ctx, keys.Queues, queueName)
	pipe.HSet(ctx, keys.Payloads, envelope.ID, encoded)
	pipe.ZAdd(ctx, keys.Priority(priority), redisclient.Z{Score: timeScore(visibleAt), Member: envelope.ID})
	if visibleAt.After(now) {
		pipe.ZAdd(ctx, keys.Delayed, redisclient.Z{Score: timeScore(visibleAt), Member: envelope.ID})
	} else {
		pipe.ZRem(ctx, keys.Delayed, envelope.ID)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return brokers.Message{}, err
	}
	return message, nil
}

func (b *Broker) Consume(ctx context.Context, queueName string, options brokers.ConsumeOptions) (brokers.Message, error) {
	if err := b.ensureOpen(); err != nil {
		return brokers.Message{}, err
	}
	client, err := b.redisClient()
	if err != nil {
		return brokers.Message{}, err
	}
	queueName = normalizeQueueName(queueName)
	keys := b.Keys(queueName)
	now := time.Now().UTC()
	if err := b.reclaimExpired(ctx, queueName, keys, now); err != nil {
		return brokers.Message{}, err
	}
	taskID, err := b.popReadyTask(ctx, keys, now)
	if err != nil {
		return brokers.Message{}, err
	}
	encoded, err := client.HGet(ctx, keys.Payloads, taskID).Bytes()
	if errors.Is(err, redisclient.Nil) {
		return brokers.Message{}, brokers.ErrQueueEmpty
	}
	if err != nil {
		return brokers.Message{}, err
	}
	message, err := decodeMessage(encoded)
	if err != nil {
		return brokers.Message{}, err
	}
	sequence, err := client.Incr(ctx, keys.Meta+":deliveries").Result()
	if err != nil {
		return brokers.Message{}, err
	}
	timeout := options.VisibilityTimeout
	if timeout <= 0 {
		timeout = b.queueVisibilityTimeout(ctx, client, keys)
	}
	deadline := now.Add(timeout)
	message.Queue = queueName
	message.Attempts++
	message.DeliveryID = fmt.Sprintf("%s-%d", message.Envelope.ID, sequence)
	message.Deadline = deadline
	message.VisibleAt = now
	encoded, err = encodeMessage(message)
	if err != nil {
		return brokers.Message{}, err
	}
	pipe := client.Pipeline()
	pipe.HSet(ctx, keys.Payloads, taskID, encoded)
	pipe.HSet(ctx, keys.InFlight, message.DeliveryID, encoded)
	pipe.ZAdd(ctx, keys.Unacked, redisclient.Z{Score: timeScore(deadline), Member: message.DeliveryID})
	pipe.ZRem(ctx, keys.Delayed, taskID)
	if _, err := pipe.Exec(ctx); err != nil {
		return brokers.Message{}, err
	}
	return message, nil
}

func (b *Broker) Ack(ctx context.Context, message brokers.Message) error {
	return b.drop(ctx, message)
}

func (b *Broker) Nack(ctx context.Context, message brokers.Message, requeue bool) error {
	if requeue {
		return b.Requeue(ctx, message, 0)
	}
	return b.drop(ctx, message)
}

func (b *Broker) Requeue(ctx context.Context, message brokers.Message, delay time.Duration) error {
	if err := b.ensureOpen(); err != nil {
		return err
	}
	client, err := b.redisClient()
	if err != nil {
		return err
	}
	queueName := normalizeQueueName(message.Queue)
	keys := b.Keys(queueName)
	deliveryID := message.DeliveryID
	visibleAt := time.Now().UTC().Add(delay)
	message.DeliveryID = ""
	message.Deadline = time.Time{}
	message.VisibleAt = visibleAt
	encoded, err := encodeMessage(message)
	if err != nil {
		return err
	}
	priority := b.priorityBucket(message.Priority)
	pipe := client.Pipeline()
	if deliveryID != "" {
		pipe.HDel(ctx, keys.InFlight, deliveryID)
		pipe.ZRem(ctx, keys.Unacked, deliveryID)
	}
	pipe.HSet(ctx, keys.Payloads, message.Envelope.ID, encoded)
	pipe.ZAdd(ctx, keys.Priority(priority), redisclient.Z{Score: timeScore(visibleAt), Member: message.Envelope.ID})
	if delay > 0 {
		pipe.ZAdd(ctx, keys.Delayed, redisclient.Z{Score: timeScore(visibleAt), Member: message.Envelope.ID})
	} else {
		pipe.ZRem(ctx, keys.Delayed, message.Envelope.ID)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (b *Broker) DeclareQueue(ctx context.Context, queueName string, options brokers.QueueOptions) error {
	if err := b.ensureOpen(); err != nil {
		return err
	}
	client, err := b.redisClient()
	if err != nil {
		return err
	}
	queueName = normalizeQueueName(queueName)
	keys := b.Keys(queueName)
	visibility := options.VisibilityTimeout
	if visibility <= 0 {
		visibility = b.config.VisibilityTimeout
	}
	_, err = client.Pipelined(ctx, func(pipe redisclient.Pipeliner) error {
		pipe.SAdd(ctx, keys.Queues, queueName)
		pipe.HSet(ctx, keys.Meta, "durable", boolString(options.Durable), "visibility_timeout_ms", visibility.Milliseconds())
		return nil
	})
	return err
}

func (b *Broker) PurgeQueue(ctx context.Context, queueName string) (int, error) {
	if err := b.ensureOpen(); err != nil {
		return 0, err
	}
	client, err := b.redisClient()
	if err != nil {
		return 0, err
	}
	queueName = normalizeQueueName(queueName)
	keys := b.Keys(queueName)
	count, err := client.HLen(ctx, keys.Payloads).Result()
	if err != nil {
		return 0, err
	}
	deleteKeys := []string{keys.Ready, keys.Delayed, keys.Unacked, keys.DeadLetter, keys.Payloads, keys.Meta, keys.InFlight, keys.Meta + ":ids", keys.Meta + ":deliveries"}
	for priority := 0; priority < b.config.PriorityBuckets; priority++ {
		deleteKeys = append(deleteKeys, keys.Priority(priority))
	}
	if err := client.Del(ctx, deleteKeys...).Err(); err != nil {
		return 0, err
	}
	return int(count), nil
}

func (b *Broker) InspectQueues(ctx context.Context) ([]brokers.QueueInfo, error) {
	if err := b.ensureOpen(); err != nil {
		return nil, err
	}
	client, err := b.redisClient()
	if err != nil {
		return nil, err
	}
	names, err := client.SMembers(ctx, b.config.Prefix+":queues").Result()
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	now := time.Now().UTC()
	infos := make([]brokers.QueueInfo, 0, len(names))
	for _, name := range names {
		keys := b.Keys(name)
		var ready int64
		for priority := 0; priority < b.config.PriorityBuckets; priority++ {
			count, err := client.ZCount(ctx, keys.Priority(priority), "-inf", fmt.Sprintf("%f", timeScore(now))).Result()
			if err != nil {
				return nil, err
			}
			ready += count
		}
		inFlight, err := client.ZCard(ctx, keys.Unacked).Result()
		if err != nil {
			return nil, err
		}
		durable := client.HGet(ctx, keys.Meta, "durable").Val() == "true"
		infos = append(infos, brokers.QueueInfo{Name: name, Ready: int(ready), InFlight: int(inFlight), Durable: durable})
	}
	return infos, nil
}

func (b *Broker) Close() error {
	b.closed.Store(true)
	if b.client == nil {
		return nil
	}
	return b.client.Close()
}

func (b *Broker) Ping(ctx context.Context) error {
	if err := b.ensureOpen(); err != nil {
		return err
	}
	client, err := b.redisClient()
	if err != nil {
		return err
	}
	return client.Ping(ctx).Err()
}

func (b *Broker) drop(ctx context.Context, message brokers.Message) error {
	if err := b.ensureOpen(); err != nil {
		return err
	}
	client, err := b.redisClient()
	if err != nil {
		return err
	}
	queueName := normalizeQueueName(message.Queue)
	keys := b.Keys(queueName)
	_, err = client.Pipelined(ctx, func(pipe redisclient.Pipeliner) error {
		pipe.HDel(ctx, keys.Payloads, message.Envelope.ID)
		if message.DeliveryID != "" {
			pipe.HDel(ctx, keys.InFlight, message.DeliveryID)
			pipe.ZRem(ctx, keys.Unacked, message.DeliveryID)
		}
		pipe.ZRem(ctx, keys.Delayed, message.Envelope.ID)
		for priority := 0; priority < b.config.PriorityBuckets; priority++ {
			pipe.ZRem(ctx, keys.Priority(priority), message.Envelope.ID)
		}
		return nil
	})
	return err
}

func (b *Broker) reclaimExpired(ctx context.Context, queueName string, keys Keys, now time.Time) error {
	client, err := b.redisClient()
	if err != nil {
		return err
	}
	deliveryIDs, err := client.ZRangeByScore(ctx, keys.Unacked, &redisclient.ZRangeBy{
		Min: "-inf",
		Max: fmt.Sprintf("%f", timeScore(now)),
	}).Result()
	if err != nil {
		return err
	}
	for _, deliveryID := range deliveryIDs {
		encoded, err := client.HGet(ctx, keys.InFlight, deliveryID).Bytes()
		if errors.Is(err, redisclient.Nil) {
			_, _ = client.ZRem(ctx, keys.Unacked, deliveryID).Result()
			continue
		}
		if err != nil {
			return err
		}
		message, err := decodeMessage(encoded)
		if err != nil {
			return err
		}
		message.Queue = queueName
		message.DeliveryID = ""
		message.Deadline = time.Time{}
		message.VisibleAt = now
		encoded, err = encodeMessage(message)
		if err != nil {
			return err
		}
		priority := b.priorityBucket(message.Priority)
		_, err = client.Pipelined(ctx, func(pipe redisclient.Pipeliner) error {
			pipe.HSet(ctx, keys.Payloads, message.Envelope.ID, encoded)
			pipe.ZAdd(ctx, keys.Priority(priority), redisclient.Z{Score: timeScore(now), Member: message.Envelope.ID})
			pipe.HDel(ctx, keys.InFlight, deliveryID)
			pipe.ZRem(ctx, keys.Unacked, deliveryID)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *Broker) popReadyTask(ctx context.Context, keys Keys, now time.Time) (string, error) {
	client, err := b.redisClient()
	if err != nil {
		return "", err
	}
	maxScore := fmt.Sprintf("%f", timeScore(now))
	for priority := b.config.PriorityBuckets - 1; priority >= 0; priority-- {
		key := keys.Priority(priority)
		values, err := client.ZRangeByScore(ctx, key, &redisclient.ZRangeBy{Min: "-inf", Max: maxScore, Offset: 0, Count: 1}).Result()
		if err != nil {
			return "", err
		}
		if len(values) == 0 {
			continue
		}
		removed, err := client.ZRem(ctx, key, values[0]).Result()
		if err != nil {
			return "", err
		}
		if removed == 0 {
			continue
		}
		return values[0], nil
	}
	return "", brokers.ErrQueueEmpty
}

func (b *Broker) queueVisibilityTimeout(ctx context.Context, client *redisclient.Client, keys Keys) time.Duration {
	value, err := client.HGet(ctx, keys.Meta, "visibility_timeout_ms").Int64()
	if err == nil && value > 0 {
		return time.Duration(value) * time.Millisecond
	}
	return b.config.VisibilityTimeout
}

func (b *Broker) priorityBucket(priority int) int {
	if priority < 0 {
		return 0
	}
	if priority >= b.config.PriorityBuckets {
		return b.config.PriorityBuckets - 1
	}
	return priority
}

func (b *Broker) ensureOpen() error {
	if b.closed.Load() {
		return brokers.ErrBrokerClosed
	}
	return nil
}

func (b *Broker) redisClient() (*redisclient.Client, error) {
	if b.client == nil {
		return nil, fmt.Errorf("%w: Redis broker requires URL or client", queue.ErrUnsupportedRuntimeURL)
	}
	return b.client, nil
}

func encodeMessage(message brokers.Message) ([]byte, error) {
	return json.Marshal(message)
}

func decodeMessage(data []byte) (brokers.Message, error) {
	var message brokers.Message
	if err := json.Unmarshal(data, &message); err != nil {
		return brokers.Message{}, err
	}
	return message, nil
}

func normalizeQueueName(queueName string) string {
	queueName = strings.TrimSpace(queueName)
	if queueName == "" {
		return "default"
	}
	return queueName
}

func timeScore(value time.Time) float64 {
	return float64(value.UnixMilli())
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func strconvItoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}
