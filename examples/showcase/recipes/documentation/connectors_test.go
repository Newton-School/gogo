package documentation_test

import (
	"context"
	"testing"
	"time"

	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/connectors/redis"
	"github.com/Newton-School/gogo/core/conf"
)

// docs:begin postgres-options
func openPostgres(ctx context.Context, settings conf.Values) (*postgres.Backend, error) {
	return postgres.Open(ctx, postgres.Config{
		DSN:   settings.Secret("GOGO_DATABASE_URL").Reveal(),
		Alias: "default", SearchPath: "public",
		MaxOpen: 20, MaxIdle: 5, MaxLifetime: 30 * time.Minute,
		ConnectTimeout: 5 * time.Second,
		Production:     settings.String("GOGO_ENV") == "production",
	})
}

// docs:end postgres-options

// docs:begin redis-options
func openRedis(ctx context.Context, settings conf.Values) (*redis.Connection, error) {
	return redis.Open(ctx, redis.Config{
		URL: settings.Secret("GOGO_REDIS_URL").Reveal(), Role: redis.CacheRole,
		PoolSize: 20, Timeout: 2 * time.Second,
		Development: settings.String("GOGO_ENV") == "development",
		Production:  settings.String("GOGO_ENV") == "production",
	})
}

// docs:end redis-options

func TestConnectorExamplesRequireProjectConfiguration(t *testing.T) {
	// Compile the real wiring functions and check missing configuration. No
	// connection or server is opened; live providers need integration tests.
	if _, err := openPostgres(context.Background(), conf.Values{}); err == nil {
		t.Fatal("missing PostgreSQL DSN accepted")
	}
	if _, err := openRedis(context.Background(), conf.Values{}); err == nil {
		t.Fatal("missing Redis URL accepted")
	}
}
