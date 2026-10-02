package database

import (
	"context"
	"errors"
	"example.com/gogo-services/apps/catalog"
	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"slices"
)

type Connection struct {
	Backend *postgres.Backend
	Store   *orm.Store
}

func (c *Connection) Resources(settings conf.Values, names []string) ([]app.Resource, error) {
	if !slices.Contains(names, "database") {
		return nil, nil
	}
	return []app.Resource{{Name: "database", Open: func(ctx context.Context) (func(context.Context) error, error) {
		registry := &models.Registry{}
		if err := registry.Register((&catalog.Product{}).Schema()); err != nil {
			return nil, err
		}
		if err := registry.Freeze(); err != nil {
			return nil, err
		}
		backend, err := postgres.Open(ctx, postgres.Config{DSN: settings.Secret("GOGO_DATABASE_URL").Reveal(), Production: settings.String("GOGO_ENV") == "production", MaxOpen: int(settings.Int("GOGO_DB_MAX_OPEN")), MaxIdle: int(settings.Int("GOGO_DB_MAX_IDLE")), MaxLifetime: settings.Duration("GOGO_DB_MAX_LIFETIME")})
		if err != nil {
			return nil, errors.New("database connection failed")
		}
		c.Backend, c.Store = backend, orm.New(backend, registry)
		return func(context.Context) error { return backend.Close() }, nil
	}}}, nil
}
