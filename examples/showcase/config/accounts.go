package config

import (
	"context"

	"example.com/gogo-showcase/apps/catalog"
	"example.com/gogo-showcase/apps/fieldlab"
	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// This showcase is a single-organization application: only verified, active
// staff superusers administer all records. It is NOT a tenant isolation example.
// Every boundary rechecks persisted auth_version; posted account IDs and flags
// never establish the actor's authority.
func (c *Connections) authorizeStaff(ctx context.Context, p auth.Principal) error {
	if !p.Authenticated || !p.Active || !p.Staff || !p.Superuser || p.AuthVersion == 0 {
		return auth.ErrPermissionDenied
	}
	user, err := orm.For(c.Store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", p.ID)).Get(ctx)
	if err != nil {
		return auth.ErrPermissionDenied
	}
	if !user.Active || !user.Staff || !user.Superuser || uint64(user.AuthVersion) != p.AuthVersion {
		return auth.ErrPermissionDenied
	}
	return nil
}

func (c *Connections) accountStore() (*admin.AccountStore, error) {
	factories := catalog.Factories()
	for key, factory := range fieldlab.Factories() {
		factories[key] = factory
	}
	return admin.NewAccountStore(admin.AccountStoreConfig{
		ORM: admin.ORMConfig{Store: c.Store, Factories: factories,
			QueryScope: func(ctx context.Context, p auth.Principal, schema models.Schema) (admin.QueryScope, error) {
				if err := c.authorizeStaff(ctx, p); err != nil {
					return admin.QueryScope{}, err
				}
				return admin.QueryScope{Identity: "showcase-administration", Predicate: orm.Q(schema.PKFields()[0].Name+"__isnull", false)}, nil
			},
			ValidateWrite: func(ctx context.Context, p auth.Principal, _ models.Record) error { return c.authorizeStaff(ctx, p) },
		},
		Accounts: auth.AccountsConfig{Authorize: func(ctx context.Context, _ auth.AccountChange) error {
			return c.authorizeStaff(ctx, auth.FromContext(ctx))
		}},
	})
}
