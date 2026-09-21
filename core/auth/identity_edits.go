package auth

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// ChangeIdentifier normalizes the submitted identity exactly once, requires
// explicit authority for that normalized value, and invalidates existing
// sessions/tokens by advancing auth_version in the same transaction. It returns
// no session-eligible proof; a self edit requires fresh authentication.
func (a *Accounts) ChangeIdentifier(ctx context.Context, userID, raw string) error {
	identifier, err := a.identifier(raw)
	if err != nil {
		return err
	}
	change := AccountChange{Action: "change_identifier", UserID: userID, Name: identifier}
	return a.withUser(ctx, change, func(ctx context.Context, user *User) error {
		if user.Identifier == identifier {
			return nil
		}
		user.Identifier = identifier
		if err := bumpVersion(user); err != nil {
			return err
		}
		if err := a.saveUser(ctx, user, orm.SaveOptions{UpdateFields: []string{"identifier", "auth_version", "updated_at"}, Guard: func(ctx context.Context, _ models.Record) error { return a.permit(ctx, change) }}); err != nil {
			return err
		}
		return a.permitSavedUser(ctx, change, user)
	})
}

// A final authorizer may call another account operation in the same
// transaction. Freeze values (not the mutable password pointer) before that
// extension point, then inspect both the returned object and stored row. There
// must be no write or further callback after this verification.
func (a *Accounts) permitSavedUser(ctx context.Context, change AccountChange, user *User) error {
	type snapshot struct {
		id, identifier, hash              string
		version                           int64
		hashSet, active, staff, superuser bool
	}
	capture := func(row *User) snapshot {
		return snapshot{row.ID, row.Identifier, storedHash(row), row.AuthVersion, row.PasswordHash != nil, row.Active, row.Staff, row.Superuser}
	}
	expected := capture(user)
	if err := a.permit(ctx, change); err != nil {
		return err
	}
	if capture(user) != expected {
		return ErrAccountChanged
	}
	persisted, err := orm.For(a.store, a.models.User).Filter(orm.Q("id", expected.id)).Get(ctx)
	if errors.Is(err, orm.ErrNotFound) {
		return ErrAccountChanged
	}
	if err != nil {
		return err
	}
	if capture(persisted) != expected {
		return ErrAccountChanged
	}
	return ctx.Err()
}

// RenameGroup retains group identity and grants. It locks the group and bounded
// member set, then invalidates every affected user's cached identity in the
// same transaction. Membership or account changes during model hooks reject
// the stale operation; deadlock/serialization outcomes require a deliberate
// whole-operation retry by the caller, never an internal partial retry.
func (a *Accounts) RenameGroup(ctx context.Context, groupID, name string) error {
	name = strings.TrimSpace(name)
	if _, err := models.CharField("name", models.WithMaxLength(150)).Clean(ctx, name); err != nil {
		return err
	}
	change := AccountChange{Action: "rename_group", GroupID: groupID, Name: name}
	if err := a.permit(ctx, change); err != nil {
		return err
	}
	if !a.models.validID(groupID) {
		return orm.ErrNotFound
	}
	return db.Atomic(ctx, a.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		group, err := orm.For(a.store, a.models.Group).Filter(orm.Q("id", groupID)).SelectForUpdate(false, false).Get(ctx)
		if err != nil {
			return err
		}
		if err := a.permit(ctx, change); err != nil {
			return err
		}
		// Authorization is an extension point and may itself perform a nested
		// domain operation. Read the protected group again after it returns.
		group, err = orm.For(a.store, a.models.Group).Filter(orm.Q("id", groupID)).Get(ctx)
		if err != nil {
			return err
		}
		if group.Name == name {
			return nil
		}
		previous := group.Name
		members, err := a.groupMemberIDs(ctx, groupID)
		if err != nil {
			return err
		}
		var users []*User
		if len(members) > 0 {
			users, err = orm.For(a.store, a.models.User).Filter(orm.Q("id__in", members)).OrderBy("id").SelectForUpdate(false, false).All(ctx)
			if err != nil {
				return err
			}
			if len(users) != len(members) {
				return ErrAccountChanged
			}
		}
		group.Name = name
		verify := func(ctx context.Context, expected string) error {
			if group.ID != groupID || group.Name != name {
				return ErrPermissionDenied
			}
			persisted, err := orm.For(a.store, a.models.Group).Filter(orm.Q("id", groupID)).Get(ctx)
			if errors.Is(err, orm.ErrNotFound) {
				return ErrAccountChanged
			}
			if err != nil {
				return err
			}
			if persisted.Name != expected {
				return ErrAccountChanged
			}
			current, err := a.groupMemberIDs(ctx, groupID)
			if err != nil {
				return err
			}
			if !slices.Equal(current, members) {
				return ErrAccountChanged
			}
			return nil
		}
		if err := a.store.Save(ctx, group, orm.SaveOptions{UpdateFields: []string{"name"}, Guard: func(ctx context.Context, _ models.Record) error {
			if err := a.permit(ctx, change); err != nil {
				return err
			}
			return verify(ctx, previous)
		}}); err != nil {
			return err
		}
		if err := verify(ctx, name); err != nil {
			return err
		}
		for _, user := range users {
			if err := bumpVersion(user); err != nil {
				return err
			}
			if err := a.saveUser(ctx, user, orm.SaveOptions{UpdateFields: []string{"auth_version", "updated_at"}}); err != nil {
				return err
			}
		}
		if err := a.permit(ctx, change); err != nil {
			return err
		}
		return verify(ctx, name)
	})
}

func (a *Accounts) groupMemberIDs(ctx context.Context, groupID string) ([]string, error) {
	links, err := orm.For(a.store, func() *UserGroup { return &UserGroup{} }).Filter(orm.Q("group_id", groupID)).Limit(maxAccountGrants + 1).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(links) > maxAccountGrants {
		return nil, errors.New("auth: group identity edit requires a bounded maintenance batch")
	}
	ids := make([]string, len(links))
	for index, link := range links {
		ids[index] = link.UserID
	}
	slices.Sort(ids)
	return ids, nil
}
