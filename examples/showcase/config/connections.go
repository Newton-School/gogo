package config

import (
	"context"
	"errors"
	"slices"

	"github.com/Newton-School/gogo/connectors/postgres"
	connector "github.com/Newton-School/gogo/connectors/redis"
	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// Each process owns its connections. Core never discovers adapters by import.
type Connections struct {
	Database                        *postgres.Backend
	Store                           *orm.Store
	Cache, Sessions, Tasks, Results *connector.Connection
}

func (c *Connections) Resources(settings conf.Values, names []string) ([]app.Resource, error) {
	var resources []app.Resource
	if slices.Contains(names, "database") {
		schema := settings.String("GOGO_SHOWCASE_SCHEMA")
		if !models.ValidIdentifier(schema) {
			return nil, errors.New("GOGO_SHOWCASE_SCHEMA must be a database identifier")
		}
		resources = append(resources, app.Resource{Name: "database", Open: func(ctx context.Context) (func(context.Context) error, error) {
			registry, err := ModelRegistry()
			if err != nil {
				return nil, err
			}
			backend, err := postgres.Open(ctx, postgres.Config{
				DSN: settings.Secret("GOGO_DATABASE_URL").Reveal(), SearchPath: schema,
				Production: settings.String("GOGO_ENV") == "production",
				MaxOpen:    int(settings.Int("GOGO_DB_MAX_OPEN")), MaxIdle: int(settings.Int("GOGO_DB_MAX_IDLE")), MaxLifetime: settings.Duration("GOGO_DB_MAX_LIFETIME"),
			})
			if err != nil {
				return nil, errors.New("PostgreSQL connection failed; check database configuration")
			}
			c.Database, c.Store = backend, orm.New(backend, registry)
			return func(context.Context) error { return backend.Close() }, nil
		}})
	}
	if slices.Contains(names, "redis") {
		for _, role := range []connector.Role{connector.CacheRole, connector.SessionRole, connector.TaskRole, connector.ResultRole} {
			resources = append(resources, app.Resource{Name: "redis_" + string(role), Open: func(ctx context.Context) (func(context.Context) error, error) {
				connection, err := connector.Open(ctx, connector.Config{
					URL: settings.Secret("GOGO_REDIS_URL").Reveal(), Namespace: settings.String("GOGO_REDIS_NAMESPACE"), Role: role,
					Development: settings.String("GOGO_ENV") != "production", Production: settings.String("GOGO_ENV") == "production",
					Timeout: settings.Duration("GOGO_REDIS_OPERATION_TIMEOUT"),
				})
				if err != nil {
					return nil, errors.New("Redis connection failed; check URL, namespace, version and durability configuration")
				}
				switch role {
				case connector.CacheRole:
					c.Cache = connection
				case connector.SessionRole:
					c.Sessions = connection
				case connector.TaskRole:
					c.Tasks = connection
				case connector.ResultRole:
					c.Results = connection
				}
				return func(context.Context) error { return connection.Close() }, nil
			}})
		}
	}
	return resources, nil
}
