// Package cache defines cache backends and optional read-through caching.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

var ErrMiss = errors.New("cache miss")

type Store interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
	Add(context.Context, string, []byte, time.Duration) (bool, error)
	Delete(context.Context, string) error
	Increment(context.Context, string, int64) (int64, error)
}

// Key encodes unambiguous dimensions; callers must include authorization scope,
// locale and every Vary dimension before lookup.
func Key(namespace string, version uint64, dimensions ...string) string {
	b, _ := json.Marshal(struct {
		Namespace  string
		Version    uint64
		Dimensions []string
	}{namespace, version, dimensions})
	sum := sha256.Sum256(b)
	return namespace + ":" + hex.EncodeToString(sum[:])
}

type call struct {
	done  chan struct{}
	value []byte
	err   error
}
type Cache struct {
	Store Store
	TTL   time.Duration
	// BypassUnavailable applies only to optional cache data, never auth/session/job state.
	BypassUnavailable bool
	mu                sync.Mutex
	pending           map[string]*call
}

func (c *Cache) GetOrSet(ctx context.Context, key string, loader func(context.Context) ([]byte, error)) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.Store == nil || loader == nil {
		return nil, errors.New("cache backend and loader required")
	}
	value, err := c.Store.Get(ctx, key)
	if err == nil {
		return append([]byte(nil), value...), nil
	}
	if !errors.Is(err, ErrMiss) && !c.BypassUnavailable {
		return nil, err
	}
	c.mu.Lock()
	if c.pending == nil {
		c.pending = make(map[string]*call)
	}
	if p := c.pending[key]; p != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.done:
			return append([]byte(nil), p.value...), p.err
		}
	}
	p := &call{done: make(chan struct{})}
	c.pending[key] = p
	c.mu.Unlock()
	defer func() {
		panicked := recover()
		if panicked != nil {
			p.err = errors.New("cache loader panicked")
		}
		c.mu.Lock()
		delete(c.pending, key)
		close(p.done)
		c.mu.Unlock()
		if panicked != nil {
			panic(panicked)
		}
	}()
	// A previous loader may have completed between the first miss and acquiring
	// leadership. Recheck after installing our flight so late callers coalesce.
	if value, err := c.Store.Get(ctx, key); err == nil {
		p.value = append([]byte(nil), value...)
		return append([]byte(nil), value...), nil
	} else if !errors.Is(err, ErrMiss) && !c.BypassUnavailable {
		p.err = err
		return nil, err
	}
	p.value, p.err = loader(ctx)
	if p.err == nil {
		ttl := c.TTL
		if ttl == 0 {
			ttl = 300 * time.Second
		}
		err = c.Store.Set(ctx, key, p.value, ttl)
		if err != nil && !c.BypassUnavailable {
			p.err = err
		}
	}
	return append([]byte(nil), p.value...), p.err
}

func GetMany(ctx context.Context, store Store, keys []string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(keys))
	for _, key := range keys {
		v, err := store.Get(ctx, key)
		if errors.Is(err, ErrMiss) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result[key] = v
	}
	return result, nil
}
func SetMany(ctx context.Context, store Store, values map[string][]byte, ttl time.Duration) error {
	var errs []error
	for key, value := range values {
		if err := store.Set(ctx, key, value, ttl); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
