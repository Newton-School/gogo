// Package redis supplies named, role-specific Redis transport connections and
// Core cache, session and rate-limit adapters without an Async dependency.
package redis

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	redigo "github.com/redis/go-redis/v9"
)

type Role string

const (
	CacheRole     Role = "cache"
	SessionRole   Role = "sessions"
	TaskRole      Role = "tasks"
	ResultRole    Role = "task_results"
	SchedulerRole Role = "scheduler"
)

var ErrInvalid = errors.New("redis: invalid configuration")
var ErrUnavailable = errors.New("redis: unavailable")
var ErrDurability = errors.New("redis: durable role requires noeviction and persistence")
var namespacePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

type Config struct {
	URL              string
	Addresses        []string
	MasterName       string
	Username         string
	Password         string
	SentinelUsername string
	SentinelPassword string
	Database         int
	Namespace        string
	Role             Role
	TLS              *tls.Config
	PoolSize         int
	Timeout          time.Duration
	Cluster          bool
	// Development permits nonpersistent loopback Redis for tests/development.
	// It does not bypass version or noeviction validation for durable roles.
	Development bool
}

type Connection struct {
	client    redigo.UniversalClient
	namespace string
	role      Role
}

func Open(ctx context.Context, c Config) (*Connection, error) {
	if !namespacePattern.MatchString(c.Namespace) || c.Database < 0 {
		return nil, ErrInvalid
	}
	switch c.Role {
	case CacheRole, SessionRole, TaskRole, ResultRole, SchedulerRole:
	default:
		return nil, ErrInvalid
	}
	if c.Timeout == 0 {
		c.Timeout = 2 * time.Second
	}
	if c.Timeout <= 0 {
		return nil, ErrInvalid
	}
	if c.PoolSize == 0 {
		c.PoolSize = 20
	}
	if c.PoolSize < 1 {
		return nil, ErrInvalid
	}
	if c.TLS != nil {
		c.TLS = c.TLS.Clone()
		if c.TLS.InsecureSkipVerify {
			return nil, ErrInvalid
		}
		if c.TLS.MinVersion < tls.VersionTLS12 {
			c.TLS.MinVersion = tls.VersionTLS12
		}
	}
	var client redigo.UniversalClient
	if c.URL != "" {
		if len(c.Addresses) > 0 || c.MasterName != "" || c.Cluster {
			return nil, ErrInvalid
		}
		opts, err := redigo.ParseURL(c.URL)
		if err != nil {
			return nil, ErrInvalid
		}
		opts.DialTimeout = c.Timeout
		opts.ReadTimeout = c.Timeout
		opts.WriteTimeout = c.Timeout
		opts.PoolSize = c.PoolSize
		opts.MaxRetries = -1
		opts.ContextTimeoutEnabled = true
		if c.TLS != nil {
			opts.TLSConfig = c.TLS
		}
		if opts.TLSConfig != nil && opts.TLSConfig.InsecureSkipVerify {
			return nil, ErrInvalid
		}
		if c.Development && !loopbackAddress(opts.Addr) {
			return nil, ErrInvalid
		}
		client = redigo.NewClient(opts)
	} else {
		if len(c.Addresses) == 0 {
			return nil, ErrInvalid
		}
		if c.Development {
			for _, address := range c.Addresses {
				if !loopbackAddress(address) {
					return nil, ErrInvalid
				}
			}
		}
		if c.Cluster && c.Database != 0 {
			return nil, ErrInvalid
		}
		client = redigo.NewUniversalClient(&redigo.UniversalOptions{Addrs: append([]string(nil), c.Addresses...), MasterName: c.MasterName, Username: c.Username, Password: c.Password, SentinelUsername: c.SentinelUsername, SentinelPassword: c.SentinelPassword, DB: c.Database, TLSConfig: c.TLS, PoolSize: c.PoolSize, DialTimeout: c.Timeout, ReadTimeout: c.Timeout, WriteTimeout: c.Timeout, MaxRetries: -1, ContextTimeoutEnabled: true, IsClusterMode: c.Cluster})
	}
	connection := &Connection{client: client, namespace: c.Namespace, role: c.Role}
	if err := connection.check(ctx, c.Development); err != nil {
		_ = client.Close()
		return nil, err
	}
	return connection, nil
}

func (c *Connection) check(ctx context.Context, development bool) error {
	check := func(ctx context.Context, client *redigo.Client) error { return probe(ctx, client, c.role, development) }
	if cluster, ok := c.client.(*redigo.ClusterClient); ok {
		return cluster.ForEachMaster(ctx, check)
	}
	return probe(ctx, c.client, c.role, development)
}
func probe(ctx context.Context, client redigo.Cmdable, role Role, development bool) error {
	if err := client.Ping(ctx).Err(); err != nil {
		return ErrUnavailable
	}
	info, err := client.Info(ctx, "server").Result()
	if err != nil {
		return ErrUnavailable
	}
	var major, minor int
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "redis_version:") {
			_, _ = fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "redis_version:")), "%d.%d", &major, &minor)
		}
	}
	if major < 7 || (major == 7 && minor < 2) {
		return fmt.Errorf("%w: Redis 7.2 or later required", ErrInvalid)
	}
	if role != CacheRole {
		config := map[string]string{}
		for _, key := range []string{"maxmemory-policy", "appendonly", "save"} {
			value, err := client.ConfigGet(ctx, key).Result()
			if err != nil {
				return ErrDurability
			}
			config[key] = value[key]
		}
		if config["maxmemory-policy"] != "noeviction" {
			return ErrDurability
		}
		if !development && config["appendonly"] != "yes" && config["save"] == "" {
			return ErrDurability
		}
	}
	return nil
}

func loopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}
func (c *Connection) Client() redigo.UniversalClient { return c.client }
func (c *Connection) Namespace() string              { return c.namespace }
func (c *Connection) Role() Role                     { return c.role }
func (c *Connection) Ping(ctx context.Context) error {
	if err := c.client.Ping(ctx).Err(); err != nil {
		return ErrUnavailable
	}
	return nil
}
func (c *Connection) Close() error { return c.client.Close() }

func Digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func Partition(id string) int { sum := sha256.Sum256([]byte(id)); return int(sum[0]) % 64 }
func (c *Connection) PartitionKey(family, id, suffix string) string {
	return c.namespace + ":" + fmt.Sprintf("{%s-p%02d}", family, Partition(id)) + ":" + id + ":" + suffix
}
func (c *Connection) PartitionIndex(family string, partition int, suffix string) (string, error) {
	if partition < 0 || partition >= 64 || !namespacePattern.MatchString(family) {
		return "", ErrInvalid
	}
	return c.namespace + ":" + fmt.Sprintf("{%s-p%02d}", family, partition) + ":" + suffix, nil
}

func hashTag(key string) string {
	open := strings.IndexByte(key, '{')
	if open < 0 {
		return key
	}
	close := strings.IndexByte(key[open+1:], '}')
	if close <= 0 {
		return key
	}
	return key[open+1 : open+1+close]
}

// Atomic executes a library-owned Lua script with bounded same-slot keys. The
// driver has transport retries disabled so ambiguous writes are never repeated
// automatically; Script.Run retries only the definite NOSCRIPT response.
func (c *Connection) Atomic(ctx context.Context, script *redigo.Script, keys []string, args ...any) (any, error) {
	if script == nil || len(keys) == 0 || len(keys) > 128 {
		return nil, ErrInvalid
	}
	tag := hashTag(keys[0])
	for _, key := range keys {
		if !strings.HasPrefix(key, c.namespace+":") || hashTag(key) != tag {
			return nil, ErrInvalid
		}
	}
	return script.Run(ctx, c.client, keys, args...).Result()
}

func ServerTime(ctx context.Context, client redigo.Cmdable) (time.Time, error) {
	return client.Time(ctx).Result()
}
func Milliseconds(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return strconv.FormatInt(t.UnixMilli(), 10)
}
