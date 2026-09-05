package main

import (
	"context"
	"strings"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

// These identities are populated before HTTP starts and never changed after
// serving begins. The temporary bootstrap authority is disabled before serving.
type demoAccountScope struct {
	bootstrap             bool
	reviewerID, managedID string
}

func newDemoAccountStore(store *orm.Store, scope *demoAccountScope) (*admin.AccountStore, error) {
	return admin.NewAccountStore(admin.AccountStoreConfig{
		ORM: admin.ORMConfig{
			Store: store,
			Factories: map[string]func() models.Model{
				"catalog.Product":     func() models.Model { return &Product{} },
				"catalog.ProductNote": func() models.Model { return &ProductNote{} },
			},
			QueryScope: func(_ context.Context, p auth.Principal, schema models.Schema) (admin.QueryScope, error) {
				if schema.Key() == (&auth.User{}).Schema().Key() {
					if p.ID != scope.reviewerID {
						return admin.QueryScope{}, auth.ErrPermissionDenied
					}
					return admin.QueryScope{Predicate: orm.Q("id__in", []string{scope.reviewerID, scope.managedID}), Identity: p.ID}, nil
				}
				return admin.QueryScope{Predicate: orm.Q("tenant", p.ID), Identity: p.ID}, nil
			},
			ValidateWrite: func(_ context.Context, p auth.Principal, record models.Record) error {
				if record.Schema().Key() == (&auth.User{}).Schema().Key() {
					id, err := record.Get("id")
					if err != nil || p.ID != scope.reviewerID || id != scope.managedID {
						return auth.ErrPermissionDenied
					}
					return nil
				}
				tenant, err := record.Get("tenant")
				if err != nil || tenant != p.ID {
					return auth.ErrPermissionDenied
				}
				return nil
			},
			Initialize: func(_ context.Context, p auth.Principal, record models.Record) error {
				if err := record.Set("tenant", p.ID); err != nil {
					return err
				}
				if record.Schema().Key() != "catalog.Product" {
					return nil
				}
				token, err := security.RandomToken(16)
				if err != nil {
					return err
				}
				return record.Set("sku", strings.ToUpper(token[:8]))
			},
		},
		Accounts: auth.AccountsConfig{Authorize: func(ctx context.Context, change auth.AccountChange) error {
			actor := auth.FromContext(ctx)
			if scope.bootstrap && actor.ID == "fixture-bootstrap" {
				return nil
			}
			// Core asks only after proof of a live, unused reset secret. The
			// fixture's sole verified delivery target belongs to the reviewer.
			if change.Action == "reset_password" && change.UserID == scope.reviewerID {
				return nil
			}
			if !actor.Authenticated || !actor.Active || !actor.Staff || actor.ID != scope.reviewerID || actor.AuthVersion == 0 {
				return auth.ErrPermissionDenied
			}
			current, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", actor.ID)).Get(ctx)
			if err != nil {
				return err
			}
			if !current.Active || !current.Staff || uint64(current.AuthVersion) != actor.AuthVersion {
				return auth.ErrPermissionDenied
			}
			if change.Action == "change_own_password" && change.UserID == actor.ID {
				return nil
			}
			if change.Action == "change_account_flags" && change.UserID == scope.managedID && !change.Superuser {
				return nil
			}
			return auth.ErrPermissionDenied
		}},
	})
}
