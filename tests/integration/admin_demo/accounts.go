package main

import (
	"context"
	"slices"
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
	grantPermissions      []int64
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
				if p.ID != scope.reviewerID {
					return admin.QueryScope{}, auth.ErrPermissionDenied
				}
				if schema.Key() == (&auth.User{}).Schema().Key() {
					return admin.QueryScope{Predicate: orm.Or(orm.Q("id", scope.reviewerID), orm.Q("identifier__startswith", "demo-")), Identity: p.ID}, nil
				}
				if schema.Key() == (&auth.Group{}).Schema().Key() {
					return admin.QueryScope{Predicate: orm.Q("name__startswith", "Demo "), Identity: p.ID}, nil
				}
				if schema.Key() == (&auth.Permission{}).Schema().Key() {
					return admin.QueryScope{Predicate: orm.Q("id__in", scope.grantPermissions), Identity: p.ID}, nil
				}
				return admin.QueryScope{Predicate: orm.Q("tenant", p.ID), Identity: p.ID}, nil
			},
			ValidateWrite: func(_ context.Context, p auth.Principal, record models.Record) error {
				if record.Schema().Key() == (&auth.User{}).Schema().Key() {
					id, err := record.Get("id")
					name, nameErr := record.Get("identifier")
					if err != nil || nameErr != nil || p.ID != scope.reviewerID || id == scope.reviewerID || !strings.HasPrefix(name.(string), "demo-") {
						return auth.ErrPermissionDenied
					}
					return nil
				}
				if record.Schema().Key() == (&auth.Group{}).Schema().Key() {
					name, err := record.Get("name")
					if err != nil || p.ID != scope.reviewerID || !strings.HasPrefix(name.(string), "Demo ") {
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
			if change.Action == "create_user" {
				if strings.HasPrefix(change.Name, "demo-") && !change.Staff && !change.Superuser {
					return nil
				}
				return auth.ErrPermissionDenied
			}
			if change.Action == "create_group" {
				if strings.HasPrefix(change.Name, "Demo ") {
					return nil
				}
				return auth.ErrPermissionDenied
			}
			if change.GroupID != "" {
				group, err := orm.For(store, func() *auth.Group { return &auth.Group{} }).Filter(orm.Q("id", change.GroupID), orm.Q("name__startswith", "Demo ")).Get(ctx)
				if err != nil || group == nil {
					return auth.ErrPermissionDenied
				}
				if change.Action == "rename_group" && strings.HasPrefix(change.Name, "Demo ") {
					return nil
				}
				if change.Action == "set_group_permissions" && demoAllowedPermissions(scope, change.PermissionIDs) {
					return nil
				}
				return auth.ErrPermissionDenied
			}
			if change.UserID != "" && change.UserID != scope.reviewerID {
				user, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", change.UserID), orm.Q("identifier__startswith", "demo-")).Get(ctx)
				if err != nil || user == nil {
					return auth.ErrPermissionDenied
				}
				switch change.Action {
				case "change_account_flags":
					if !change.Superuser {
						return nil
					}
				case "change_password":
					return nil
				case "change_identifier":
					if strings.HasPrefix(change.Name, "demo-") {
						return nil
					}
				case "set_user_permissions":
					if demoAllowedPermissions(scope, change.PermissionIDs) {
						return nil
					}
				case "set_user_groups":
					if len(change.GroupIDs) == 0 {
						return nil
					}
					count, err := orm.For(store, func() *auth.Group { return &auth.Group{} }).Filter(orm.Q("id__in", change.GroupIDs), orm.Q("name__startswith", "Demo ")).Count(ctx)
					if err != nil {
						return err
					}
					if count == int64(len(change.GroupIDs)) {
						return nil
					}
				}
			}
			return auth.ErrPermissionDenied
		}},
	})
}

func demoAllowedPermissions(scope *demoAccountScope, ids []int64) bool {
	return !slices.ContainsFunc(ids, func(id int64) bool { return !slices.Contains(scope.grantPermissions, id) })
}
