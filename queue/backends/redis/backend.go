// Package redis provides a real Redis-backed queue result backend.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Newton-School/gogo/queue"
	"github.com/Newton-School/gogo/queue/backends"
	redisclient "github.com/redis/go-redis/v9"
)

type Config struct {
	URL    string
	Prefix string
	Now    func() time.Time
	Client *redisclient.Client
}

type Backend struct {
	config Config
	client *redisclient.Client
	closed atomic.Bool
}

type Keys struct {
	Result   string
	Children string
	Group    string
	Chord    string
	Notify   string
}

func init() {
	queue.RegisterResultBackendFactory("redis", func(config queue.RuntimeConfig) (queue.ResultBackend, error) {
		return NewBackendFromURL(config.ResultBackend)
	})
	queue.RegisterResultBackendFactory("rediss", func(config queue.RuntimeConfig) (queue.ResultBackend, error) {
		return NewBackendFromURL(config.ResultBackend)
	})
}

func NewBackend(config Config) *Backend {
	backend, _ := newBackend(config, false)
	return backend
}

func NewBackendFromURL(rawURL string) (*Backend, error) {
	return newBackend(Config{URL: rawURL}, true)
}

func newBackend(config Config, strictURL bool) (*Backend, error) {
	if config.Prefix == "" {
		config.Prefix = "gogo"
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	client := config.Client
	if client == nil && strings.TrimSpace(config.URL) != "" {
		options, err := redisclient.ParseURL(config.URL)
		if err != nil {
			if strictURL {
				return nil, fmt.Errorf("%w: parse Redis result backend URL %q: %v", queue.ErrUnsupportedRuntimeURL, config.URL, err)
			}
		} else {
			client = redisclient.NewClient(options)
		}
	}
	return &Backend{config: config, client: client}, nil
}

func (b *Backend) Keys(id string) Keys {
	return Keys{
		Result:   b.config.Prefix + ":result:" + id,
		Children: b.config.Prefix + ":result:" + id + ":children",
		Group:    b.config.Prefix + ":group:" + id,
		Chord:    b.config.Prefix + ":chord:" + id,
		Notify:   b.config.Prefix + ":result:" + id + ":notify",
	}
}

func (b *Backend) StoreResult(ctx context.Context, result queue.Result) error {
	if err := b.ensureOpen(); err != nil {
		return err
	}
	client, err := b.redisClient()
	if err != nil {
		return err
	}
	now := b.config.Now().UTC()
	if result.CreatedAt.IsZero() {
		result.CreatedAt = now
	}
	result.UpdatedAt = now
	encoded, err := json.Marshal(result.Clone())
	if err != nil {
		return err
	}
	keys := b.Keys(result.TaskID)
	expiration := time.Duration(0)
	if result.ExpiresAt != nil {
		expiration = time.Until(result.ExpiresAt.UTC())
		if expiration <= 0 {
			expiration = time.Millisecond
		}
	}
	pipe := client.Pipeline()
	pipe.Set(ctx, keys.Result, encoded, expiration)
	if len(result.Children) > 0 {
		pipe.Del(ctx, keys.Children)
		values := make([]any, len(result.Children))
		for index, child := range result.Children {
			values[index] = child
		}
		pipe.RPush(ctx, keys.Children, values...)
		if expiration > 0 {
			pipe.Expire(ctx, keys.Children, expiration)
		}
	} else {
		pipe.Del(ctx, keys.Children)
	}
	pipe.Publish(ctx, keys.Notify, "stored")
	_, err = pipe.Exec(ctx)
	return err
}

func (b *Backend) GetResult(ctx context.Context, taskID string) (queue.Result, error) {
	if err := b.ensureOpen(); err != nil {
		return queue.Result{}, err
	}
	client, err := b.redisClient()
	if err != nil {
		return queue.Result{}, err
	}
	raw, err := client.Get(ctx, b.Keys(taskID).Result).Bytes()
	if errors.Is(err, redisclient.Nil) {
		return queue.Result{}, fmt.Errorf("%w: %s", backends.ErrResultNotFound, taskID)
	}
	if err != nil {
		return queue.Result{}, err
	}
	var result queue.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return queue.Result{}, err
	}
	if result.ExpiresAt != nil && b.config.Now().After(*result.ExpiresAt) {
		_ = b.Forget(ctx, taskID)
		return queue.Result{}, fmt.Errorf("%w: %s", backends.ErrResultExpired, taskID)
	}
	return result.Clone(), nil
}

func (b *Backend) Forget(ctx context.Context, taskID string) error {
	if err := b.ensureOpen(); err != nil {
		return err
	}
	client, err := b.redisClient()
	if err != nil {
		return err
	}
	keys := b.Keys(taskID)
	return client.Del(ctx, keys.Result, keys.Children, keys.Notify).Err()
}

func (b *Backend) Wait(ctx context.Context, taskID string, timeout time.Duration) (queue.Result, error) {
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err := b.GetResult(ctx, taskID)
		if err == nil && result.State.Terminal() {
			return result, nil
		}
		if err != nil && !errors.Is(err, backends.ErrResultNotFound) {
			return queue.Result{}, err
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return queue.Result{}, fmt.Errorf("%w: wait timeout for %s", backends.ErrResultNotFound, taskID)
		}
		select {
		case <-ctx.Done():
			return queue.Result{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (b *Backend) Children(ctx context.Context, taskID string) ([]string, error) {
	if err := b.ensureOpen(); err != nil {
		return nil, err
	}
	client, err := b.redisClient()
	if err != nil {
		return nil, err
	}
	children, err := client.LRange(ctx, b.Keys(taskID).Children, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	if len(children) > 0 {
		return append([]string(nil), children...), nil
	}
	result, err := b.GetResult(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), result.Children...), nil
}

func (b *Backend) GroupResult(ctx context.Context, groupID string, children []string) (queue.GroupResult, error) {
	if err := b.ensureOpen(); err != nil {
		return queue.GroupResult{}, err
	}
	client, err := b.redisClient()
	if err != nil {
		return queue.GroupResult{}, err
	}
	group := queue.GroupResult{ID: groupID, Children: append([]string(nil), children...), CreatedAt: b.config.Now().UTC()}
	encoded, err := json.Marshal(group.Clone())
	if err != nil {
		return queue.GroupResult{}, err
	}
	if err := client.Set(ctx, b.Keys(groupID).Group, encoded, 0).Err(); err != nil {
		return queue.GroupResult{}, err
	}
	return group, nil
}

func (b *Backend) ChordCounter(ctx context.Context, chordID string, delta int) (int, error) {
	if err := b.ensureOpen(); err != nil {
		return 0, err
	}
	client, err := b.redisClient()
	if err != nil {
		return 0, err
	}
	count, err := client.IncrBy(ctx, b.Keys(chordID).Chord, int64(delta)).Result()
	if err != nil {
		return 0, err
	}
	return int(count), nil
}

func (b *Backend) Ping(ctx context.Context) error {
	if err := b.ensureOpen(); err != nil {
		return err
	}
	client, err := b.redisClient()
	if err != nil {
		return err
	}
	return client.Ping(ctx).Err()
}

func (b *Backend) Close() error {
	b.closed.Store(true)
	if b.client == nil {
		return nil
	}
	return b.client.Close()
}

func (b *Backend) ensureOpen() error {
	if b.closed.Load() {
		return fmt.Errorf("%w: Redis result backend closed", queue.ErrUnsupportedRuntimeURL)
	}
	return nil
}

func (b *Backend) redisClient() (*redisclient.Client, error) {
	if b.client == nil {
		return nil, fmt.Errorf("%w: Redis result backend requires URL or client", queue.ErrUnsupportedRuntimeURL)
	}
	return b.client, nil
}
