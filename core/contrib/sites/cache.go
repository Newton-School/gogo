package sites

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/Newton-School/gogo/core/cache"
)

// CacheConfig is opt-in. Namespace must identify the project/database, not
// merely a common alias such as default. Version and TTL define operator-owned
// freshness; site writes must invalidate after commit or bump this version.
type CacheConfig struct {
	Store             cache.Store
	Namespace         string
	Version           uint64
	TTL               time.Duration
	BypassUnavailable bool
}

type siteCacheStore struct {
	cache.Store
	namespace, alias string
	version          uint64
	ttl              time.Duration
}

type cacheEntry struct {
	Version                 uint64
	Backend, Kind, Selector string
	Site                    Info
	ExpiresAt               int64
}

func (s *resolverState) configureCache(config CacheConfig) error {
	if nilValue(config.Store) || len(config.Namespace) < 1 || len(config.Namespace) > 128 || config.Version == 0 || config.TTL < time.Second || config.TTL > 24*time.Hour {
		return ErrConfiguration
	}
	for _, c := range []byte(config.Namespace) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return ErrConfiguration
		}
	}
	s.cached = &siteCacheStore{Store: config.Store, namespace: config.Namespace + ".sites", alias: s.alias, version: config.Version, ttl: config.TTL}
	s.cache = &cache.Cache{Store: s.cached, TTL: config.TTL, BypassUnavailable: config.BypassUnavailable}
	return nil
}

func (s *siteCacheStore) key(kind, selector string) string {
	return cache.Key(s.namespace, s.version, s.alias, kind, selector)
}

func (s *siteCacheStore) encode(kind, selector string, info Info) ([]byte, error) {
	return json.Marshal(cacheEntry{Version: s.version, Backend: s.alias, Kind: kind, Selector: selector, Site: info, ExpiresAt: time.Now().Add(s.ttl).UnixNano()})
}

func (s *siteCacheStore) decode(key string, data []byte) (cacheEntry, bool) {
	var value cacheEntry
	if len(data) > 8192 || json.Unmarshal(data, &value) != nil {
		return value, false
	}
	// Only our canonical encoding is accepted: duplicate/unknown/case-variant
	// keys cannot silently replace identity fields. Corrupt entries are misses.
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, data) || value.Version != s.version || value.Backend != s.alias || !value.Site.Active || !validInfo(value.Site, value.Kind, value.Selector) || key != s.key(value.Kind, value.Selector) {
		return cacheEntry{}, false
	}
	now := time.Now().UnixNano()
	if value.ExpiresAt <= now || value.ExpiresAt-now > int64(s.ttl) {
		return cacheEntry{}, false
	}
	return value, true
}

func (s *siteCacheStore) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.Store.Get(ctx, key)
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	if err != nil {
		if err == cache.ErrMiss {
			return nil, cache.ErrMiss
		}
		return nil, ErrUnavailable
	}
	if len(data) > 8192 {
		return nil, cache.ErrMiss
	}
	data = append([]byte(nil), data...)
	if _, ok := s.decode(key, data); !ok {
		return nil, cache.ErrMiss
	}
	return data, nil
}

func (s *siteCacheStore) Set(ctx context.Context, key string, data []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.Store.Set(ctx, key, append([]byte(nil), data...), ttl)
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
