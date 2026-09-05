package redis

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/Newton-School/gogo/core/cache"
	redigo "github.com/redis/go-redis/v9"
)

type Cache struct {
	Connection *Connection
	Version    uint64
	MaxBytes   int
}

func (c *Cache) key(key string) string {
	return c.Connection.namespace + ":cache:" + strconv.FormatUint(c.Version, 10) + ":" + Digest(key)
}
func (c *Cache) validate(value []byte, ttl time.Duration) error {
	maxBytes := c.MaxBytes
	if maxBytes == 0 {
		maxBytes = 1 << 20
	}
	if c.Connection == nil || len(value) > maxBytes || ttl < 0 {
		return ErrInvalid
	}
	return nil
}
func (c *Cache) Get(ctx context.Context, key string) ([]byte, error) {
	if c.Connection == nil {
		return nil, ErrInvalid
	}
	value, err := c.Connection.client.Get(ctx, c.key(key)).Bytes()
	if errors.Is(err, redigo.Nil) {
		return nil, cache.ErrMiss
	}
	return value, err
}
func (c *Cache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := c.validate(value, ttl); err != nil {
		return err
	}
	return c.Connection.client.Set(ctx, c.key(key), value, ttl).Err()
}
func (c *Cache) Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if err := c.validate(value, ttl); err != nil {
		return false, err
	}
	return c.Connection.client.SetNX(ctx, c.key(key), value, ttl).Result()
}
func (c *Cache) Delete(ctx context.Context, key string) error {
	if c.Connection == nil {
		return ErrInvalid
	}
	return c.Connection.client.Del(ctx, c.key(key)).Err()
}

var incrementScript = redigo.NewScript(`if redis.call('EXISTS',KEYS[1])==0 then return {0,0} end; return {1,redis.call('INCRBY',KEYS[1],ARGV[1])}`)

func (c *Cache) Increment(ctx context.Context, key string, delta int64) (int64, error) {
	if c.Connection == nil {
		return 0, ErrInvalid
	}
	v, err := c.Connection.Atomic(ctx, incrementScript, []string{c.key(key)}, delta)
	if err != nil {
		return 0, err
	}
	values := v.([]any)
	if values[0].(int64) == 0 {
		return 0, cache.ErrMiss
	}
	return values[1].(int64), nil
}
func (c *Cache) Touch(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if c.Connection == nil || ttl <= 0 {
		return false, ErrInvalid
	}
	return c.Connection.client.PExpire(ctx, c.key(key), ttl).Result()
}
func (c *Cache) GetMany(ctx context.Context, keys []string) (map[string][]byte, error) {
	return cache.GetMany(ctx, c, keys)
}
func (c *Cache) SetMany(ctx context.Context, values map[string][]byte, ttl time.Duration) error {
	return cache.SetMany(ctx, c, values, ttl)
}

var _ cache.Store = (*Cache)(nil)
