package auth

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type PermissionDefinition struct{ Codename, Name string }

var ErrAccountChanged = errors.New("auth: account changed; retry the operation")

// SyncPermissions is privileged post-migration maintenance, not a grant API.
// It installs view/add/change/delete and explicitly declared custom permissions
// without assigning them to anyone or deleting stale identities.
func SyncPermissions(ctx context.Context, store *orm.Store, registry *models.Registry, custom map[string][]PermissionDefinition) error {
	if store == nil || store.Backend == nil || registry == nil {
		return errors.New("auth: permission synchronization requires model registry")
	}
	schemas := registry.All()
	declarations := map[string][]PermissionDefinition{}
	for _, schema := range schemas {
		if schema.Abstract || schema.AutoCreatedBy != "" {
			continue
		}
		var definitions []PermissionDefinition
		for _, action := range []string{"view", "add", "change", "delete"} {
			definitions = append(definitions, PermissionDefinition{action + "_" + strings.ToLower(schema.Name), "Can " + action + " " + schema.Name})
		}
		definitions = append(definitions, custom[schema.Key()]...)
		seen := map[string]bool{}
		for _, definition := range definitions {
			if !models.ValidIdentifier(definition.Codename) || len(definition.Codename) > 128 || seen[definition.Codename] {
				return errors.New("auth: invalid or duplicate permission declaration")
			}
			if _, err := models.CharField("name", models.WithMaxLength(255)).Clean(ctx, definition.Name); err != nil {
				return errors.New("auth: invalid permission name")
			}
			seen[definition.Codename] = true
		}
		declarations[schema.Key()] = definitions
	}
	for key := range custom {
		if _, ok := declarations[key]; !ok {
			return errors.New("auth: custom permissions refer to an unregistered model")
		}
	}
	return db.Atomic(ctx, store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		for _, schema := range schemas {
			if len(declarations[schema.Key()]) == 0 {
				continue
			}
			typeRecord, err := contenttypes.ForModel(ctx, store, schema)
			if err != nil {
				return err
			}
			for _, definition := range declarations[schema.Key()] {
				_, _, err := orm.For(store, func() *Permission { return &Permission{} }).UpdateOrCreate(ctx, orm.UniqueKey{Constraint: "gogo_permissions_identity", Values: map[string]any{"content_type_id": typeRecord.ID, "codename": definition.Codename}}, orm.Defaults{"name": definition.Name})
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (a *Accounts) withUser(ctx context.Context, change AccountChange, apply func(context.Context, *User) error) error {
	if err := a.permit(ctx, change); err != nil {
		return err
	}
	if !a.models.validID(change.UserID) {
		return orm.ErrNotFound
	}
	return db.Atomic(ctx, a.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		user, err := orm.For(a.store, a.models.User).Filter(orm.Q("id", change.UserID)).SelectForUpdate(false, false).Get(ctx)
		if err != nil {
			return err
		}
		if err := a.permit(ctx, change); err != nil {
			return err
		}
		// A policy may invoke another account operation inside this same
		// transaction. Refresh after that extension point rather than applying
		// the requested change to a stale pre-policy user snapshot.
		user, err = orm.For(a.store, a.models.User).Filter(orm.Q("id", change.UserID)).Get(ctx)
		if err != nil {
			return err
		}
		return apply(ctx, user)
	})
}

func (a *Accounts) ChangePassword(ctx context.Context, userID, password string) error {
	change := AccountChange{Action: "change_password", UserID: userID}
	if err := a.permit(ctx, change); err != nil {
		return err
	}
	principal, err := a.LoadPrincipal(ctx, userID)
	if err != nil {
		return err
	}
	hash, err := a.hash(ctx, password, principal)
	if err != nil {
		return err
	}
	return a.withUser(ctx, change, func(ctx context.Context, user *User) error {
		if uint64(user.AuthVersion) != principal.AuthVersion {
			return ErrAccountChanged
		}
		if err := bumpVersion(user); err != nil {
			return err
		}
		user.PasswordHash = &hash
		return a.saveUser(ctx, user, orm.SaveOptions{UpdateFields: []string{"password_hash", "auth_version", "updated_at"}})
	})
}

func (a *Accounts) SetUnusablePassword(ctx context.Context, userID string) error {
	return a.withUser(ctx, AccountChange{Action: "change_password", UserID: userID, Unusable: true}, func(ctx context.Context, user *User) error {
		marker, err := UnusablePassword()
		if err != nil {
			return err
		}
		if err := bumpVersion(user); err != nil {
			return err
		}
		user.PasswordHash = &marker
		return a.saveUser(ctx, user, orm.SaveOptions{UpdateFields: []string{"password_hash", "auth_version", "updated_at"}})
	})
}

func (a *Accounts) SetAccountFlags(ctx context.Context, userID string, active, staff, superuser bool) error {
	return a.withUser(ctx, AccountChange{Action: "change_account_flags", UserID: userID, Active: active, Staff: staff, Superuser: superuser}, func(ctx context.Context, user *User) error {
		if user.Active == active && user.Staff == staff && user.Superuser == superuser {
			return nil
		}
		if err := bumpVersion(user); err != nil {
			return err
		}
		user.Active, user.Staff, user.Superuser = active, staff, superuser
		return a.saveUser(ctx, user, orm.SaveOptions{UpdateFields: []string{"active", "staff", "superuser", "auth_version", "updated_at"}})
	})
}

func permissionIDs(values []int64) ([]int64, error) {
	if len(values) > 1000 {
		return nil, errors.New("auth: permission mutation bound exceeded")
	}
	values = slices.Clone(values)
	slices.Sort(values)
	for i, value := range values {
		if value < 1 || i > 0 && values[i-1] == value {
			return nil, errors.New("auth: invalid or duplicate permission")
		}
	}
	return values, nil
}

func (a *Accounts) validatePermissions(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	found, err := orm.For(a.store, func() *Permission { return &Permission{} }).Filter(orm.Q("id__in", ids)).Count(ctx)
	if err != nil {
		return err
	}
	if found != int64(len(ids)) {
		return orm.ErrNotFound
	}
	return nil
}

func (a *Accounts) SetUserPermissions(ctx context.Context, userID string, values []int64) error {
	ids, err := permissionIDs(values)
	if err != nil {
		return err
	}
	return a.withUser(ctx, AccountChange{Action: "set_user_permissions", UserID: userID, PermissionIDs: ids}, func(ctx context.Context, user *User) error {
		if err := a.validatePermissions(ctx, ids); err != nil {
			return err
		}
		query := orm.For(a.store, func() *UserPermission { return &UserPermission{} }).Filter(orm.Q("user_id", user.ID))
		current, err := query.Limit(maxAccountGrants + 1).All(ctx)
		if err != nil {
			return err
		}
		if len(current) > maxAccountGrants {
			return errors.New("auth: account grant bound exceeded")
		}
		existing := make([]int64, len(current))
		for i, item := range current {
			existing[i] = item.PermissionID
		}
		slices.Sort(existing)
		if slices.Equal(existing, ids) {
			return nil
		}
		collector := orm.DeleteCollector{Store: a.store, Scope: func(_ context.Context, schema models.Schema) (db.Predicate, error) {
			if schema.Key() != (&UserPermission{}).Schema().Key() {
				return db.Predicate{}, ErrPermissionDenied
			}
			return orm.Q("user_id", userID), nil
		}}
		for _, link := range current {
			record, err := models.Bind(link)
			if err != nil {
				return err
			}
			if _, err := collector.Execute(ctx, record); err != nil {
				return err
			}
		}
		for _, id := range ids {
			link := &UserPermission{UserID: user.ID, PermissionID: id}
			if err := a.store.Save(ctx, link, orm.SaveOptions{ForceInsert: true, Guard: func(context.Context, models.Record) error {
				if link.UserID != userID || link.PermissionID != id {
					return ErrPermissionDenied
				}
				return nil
			}}); err != nil {
				return err
			}
			if link.UserID != userID || link.PermissionID != id {
				return ErrPermissionDenied
			}
			persisted, err := orm.For(a.store, func() *UserPermission { return &UserPermission{} }).Filter(orm.Q("user_id", userID), orm.Q("permission_id", id)).Get(ctx)
			if err != nil {
				if errors.Is(err, orm.ErrNotFound) {
					return ErrPermissionDenied
				}
				return err
			}
			if persisted.ID != link.ID {
				return ErrPermissionDenied
			}
		}
		if err := bumpVersion(user); err != nil {
			return err
		}
		return a.saveUser(ctx, user, orm.SaveOptions{UpdateFields: []string{"auth_version", "updated_at"}})
	})
}

// Security-sensitive identity values are fixed by the authorized operation.
// Ordinary model hooks may observe them but cannot retarget the account or
// smuggle another privilege/credential effect into this transaction.
func (a *Accounts) saveUser(ctx context.Context, user *User, options orm.SaveOptions) error {
	id, identifier, encoded, version := user.ID, user.Identifier, storedHash(user), user.AuthVersion
	active, staff, superuser := user.Active, user.Staff, user.Superuser
	var previous *User
	if !options.ForceInsert {
		var err error
		previous, err = orm.For(a.store, a.models.User).Filter(orm.Q("id", id)).Get(ctx)
		if errors.Is(err, orm.ErrNotFound) {
			return ErrAccountChanged
		}
		if err != nil {
			return err
		}
		expectedVersion := version
		if slices.Contains(options.UpdateFields, "auth_version") {
			expectedVersion--
		}
		if previous.AuthVersion != expectedVersion {
			return ErrAccountChanged
		}
	}
	guard := options.Guard
	options.Guard = func(ctx context.Context, record models.Record) error {
		if guard != nil {
			if err := guard(ctx, record); err != nil {
				return err
			}
		}
		if user.identity != a.models || user.ID != id || user.Identifier != identifier || storedHash(user) != encoded || user.AuthVersion != version || user.Active != active || user.Staff != staff || user.Superuser != superuser {
			return ErrPermissionDenied
		}
		if previous != nil {
			current, err := orm.For(a.store, a.models.User).Filter(orm.Q("id", id)).Get(ctx)
			if errors.Is(err, orm.ErrNotFound) {
				return ErrAccountChanged
			}
			if err != nil {
				return err
			}
			if current.AuthVersion != previous.AuthVersion || storedHash(current) != storedHash(previous) || current.Identifier != previous.Identifier || current.Active != previous.Active || current.Staff != previous.Staff || current.Superuser != previous.Superuser {
				return ErrAccountChanged
			}
		}
		return nil
	}
	if err := a.saveIdentity(ctx, user, options, &id); err != nil {
		return err
	}
	if user.identity != a.models || user.ID != id || user.Identifier != identifier || storedHash(user) != encoded || user.AuthVersion != version || user.Active != active || user.Staff != staff || user.Superuser != superuser {
		return ErrPermissionDenied
	}
	// An AfterSave hook can restore the in-memory values after BeforeSave
	// changed the actual INSERT/UPDATE. Check persisted state before commit.
	persisted, err := orm.For(a.store, a.models.User).Filter(orm.Q("id", id)).Get(ctx)
	if errors.Is(err, orm.ErrNotFound) {
		return ErrPermissionDenied
	}
	if err != nil {
		return err
	}
	if persisted.Identifier != identifier || storedHash(persisted) != encoded || persisted.AuthVersion != version || persisted.Active != active || persisted.Staff != staff || persisted.Superuser != superuser {
		return ErrPermissionDenied
	}
	return nil
}
