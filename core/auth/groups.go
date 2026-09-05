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

func (a *Accounts) CreateGroup(ctx context.Context, name string) (*Group, error) {
	name = strings.TrimSpace(name)
	if _, err := models.CharField("name", models.WithMaxLength(150)).Clean(ctx, name); err != nil {
		return nil, err
	}
	change := AccountChange{Action: "create_group", Name: name}
	if err := a.permit(ctx, change); err != nil {
		return nil, err
	}
	id, err := newAccountID()
	if err != nil {
		return nil, err
	}
	group := &Group{ID: id, Name: name}
	err = db.Atomic(ctx, a.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		if err := a.permit(ctx, change); err != nil {
			return err
		}
		if err := a.store.Save(ctx, group, orm.SaveOptions{ForceInsert: true, Guard: func(ctx context.Context, _ models.Record) error {
			if err := a.permit(ctx, change); err != nil {
				return err
			}
			if group.ID != id || group.Name != name {
				return ErrPermissionDenied
			}
			return nil
		}}); err != nil {
			return err
		}
		// A model hook may revoke authority, and authorization may itself run a
		// nested domain operation. Verify the exact newly-created snapshot only
		// after this last extension point, without any subsequent mutation.
		if err := a.permit(ctx, change); err != nil {
			return err
		}
		if group.ID != id || group.Name != name {
			return ErrPermissionDenied
		}
		persisted, err := orm.For(a.store, func() *Group { return &Group{} }).Filter(orm.Q("id", id)).Get(ctx)
		if errors.Is(err, orm.ErrNotFound) {
			return ErrPermissionDenied
		}
		if err != nil {
			return err
		}
		if persisted.Name != name {
			return ErrAccountChanged
		}
		members, err := orm.For(a.store, func() *UserGroup { return &UserGroup{} }).Filter(orm.Q("group_id", id)).Exists(ctx)
		if err != nil {
			return err
		}
		permissions, err := orm.For(a.store, func() *GroupPermission { return &GroupPermission{} }).Filter(orm.Q("group_id", id)).Exists(ctx)
		if err != nil {
			return err
		}
		if members || permissions {
			return ErrAccountChanged
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return group, nil
}

func groupIDs(values []string) ([]string, error) {
	if len(values) > 1000 {
		return nil, errors.New("auth: group mutation bound exceeded")
	}
	values = slices.Clone(values)
	slices.Sort(values)
	for i, value := range values {
		if !validAccountID(value) || i > 0 && values[i-1] == value {
			return nil, errors.New("auth: invalid or duplicate group")
		}
	}
	return values, nil
}

func (a *Accounts) removeLinks(ctx context.Context, schema models.Schema, ownerField, ownerID string, records []models.Model) error {
	collector := orm.DeleteCollector{Store: a.store, Scope: func(_ context.Context, target models.Schema) (db.Predicate, error) {
		if target.Key() != schema.Key() {
			return db.Predicate{}, ErrPermissionDenied
		}
		return orm.Q(ownerField, ownerID), nil
	}}
	for _, link := range records {
		record, err := models.Bind(link)
		if err != nil {
			return err
		}
		if _, err := collector.Execute(ctx, record); err != nil {
			return err
		}
	}
	return nil
}

func (a *Accounts) SetUserGroups(ctx context.Context, userID string, values []string) error {
	ids, err := groupIDs(values)
	if err != nil {
		return err
	}
	return a.withUser(ctx, AccountChange{Action: "set_user_groups", UserID: userID, GroupIDs: ids}, func(ctx context.Context, user *User) error {
		// All supported group-permission writers acquire the group row. Shared
		// ownership with membership changes prevents unversioned grant races.
		if len(ids) > 0 {
			found, err := orm.For(a.store, func() *Group { return &Group{} }).Filter(orm.Q("id__in", ids)).OrderBy("id").SelectForUpdate(false, false).All(ctx)
			if err != nil {
				return err
			}
			if len(found) != len(ids) {
				return orm.ErrNotFound
			}
		}
		current, err := orm.For(a.store, func() *UserGroup { return &UserGroup{} }).Filter(orm.Q("user_id", userID)).Limit(maxAccountGrants + 1).All(ctx)
		if err != nil {
			return err
		}
		if len(current) > maxAccountGrants {
			return errors.New("auth: account group bound exceeded")
		}
		existing := make([]string, len(current))
		records := make([]models.Model, len(current))
		for i, link := range current {
			existing[i], records[i] = link.GroupID, link
		}
		slices.Sort(existing)
		if slices.Equal(existing, ids) {
			return nil
		}
		if err := a.removeLinks(ctx, (&UserGroup{}).Schema(), "user_id", userID, records); err != nil {
			return err
		}
		for _, groupID := range ids {
			link := &UserGroup{UserID: userID, GroupID: groupID}
			if err := a.store.Save(ctx, link, orm.SaveOptions{ForceInsert: true, Guard: func(context.Context, models.Record) error {
				if link.UserID != userID || link.GroupID != groupID {
					return ErrPermissionDenied
				}
				return nil
			}}); err != nil {
				return err
			}
			if link.UserID != userID || link.GroupID != groupID {
				return ErrPermissionDenied
			}
			persisted, err := orm.For(a.store, func() *UserGroup { return &UserGroup{} }).Filter(orm.Q("user_id", userID), orm.Q("group_id", groupID)).Get(ctx)
			if errors.Is(err, orm.ErrNotFound) {
				return ErrPermissionDenied
			}
			if err != nil {
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

// SetGroupPermissions invalidates every current member in the same transaction
// as the grant replacement. Concurrent opposite lock ordering may return a typed
// database deadlock/serialization error; callers may retry the whole operation.
// No partially changed group or member versions are committed on that error.
func (a *Accounts) SetGroupPermissions(ctx context.Context, groupID string, values []int64) error {
	ids, err := permissionIDs(values)
	if err != nil {
		return err
	}
	change := AccountChange{Action: "set_group_permissions", GroupID: groupID, PermissionIDs: ids}
	if err := a.permit(ctx, change); err != nil {
		return err
	}
	if !validAccountID(groupID) {
		return orm.ErrNotFound
	}
	return db.Atomic(ctx, a.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		if _, err := orm.For(a.store, func() *Group { return &Group{} }).Filter(orm.Q("id", groupID)).SelectForUpdate(false, false).Get(ctx); err != nil {
			return err
		}
		if err := a.permit(ctx, change); err != nil {
			return err
		}
		if err := a.validatePermissions(ctx, ids); err != nil {
			return err
		}
		current, err := orm.For(a.store, func() *GroupPermission { return &GroupPermission{} }).Filter(orm.Q("group_id", groupID)).Limit(maxAccountGrants + 1).All(ctx)
		if err != nil {
			return err
		}
		if len(current) > maxAccountGrants {
			return errors.New("auth: group grant bound exceeded")
		}
		existing := make([]int64, len(current))
		records := make([]models.Model, len(current))
		for i, link := range current {
			existing[i], records[i] = link.PermissionID, link
		}
		slices.Sort(existing)
		if slices.Equal(existing, ids) {
			return nil
		}
		memberships, err := orm.For(a.store, func() *UserGroup { return &UserGroup{} }).Filter(orm.Q("group_id", groupID)).Limit(maxAccountGrants + 1).All(ctx)
		if err != nil {
			return err
		}
		if len(memberships) > maxAccountGrants {
			return errors.New("auth: group invalidation requires a bounded maintenance batch")
		}
		var users []*User
		if len(memberships) > 0 {
			userIDs := make([]string, len(memberships))
			for i, link := range memberships {
				userIDs[i] = link.UserID
			}
			users, err = orm.For(a.store, func() *User { return &User{} }).Filter(orm.Q("id__in", userIDs)).OrderBy("id").SelectForUpdate(false, false).All(ctx)
			if err != nil {
				return err
			}
			if len(users) != len(userIDs) {
				return ErrAccountChanged
			}
		}
		if err := a.removeLinks(ctx, (&GroupPermission{}).Schema(), "group_id", groupID, records); err != nil {
			return err
		}
		for _, permissionID := range ids {
			link := &GroupPermission{GroupID: groupID, PermissionID: permissionID}
			if err := a.store.Save(ctx, link, orm.SaveOptions{ForceInsert: true, Guard: func(context.Context, models.Record) error {
				if link.GroupID != groupID || link.PermissionID != permissionID {
					return ErrPermissionDenied
				}
				return nil
			}}); err != nil {
				return err
			}
			if link.GroupID != groupID || link.PermissionID != permissionID {
				return ErrPermissionDenied
			}
			persisted, err := orm.For(a.store, func() *GroupPermission { return &GroupPermission{} }).Filter(orm.Q("group_id", groupID), orm.Q("permission_id", permissionID)).Get(ctx)
			if errors.Is(err, orm.ErrNotFound) {
				return ErrPermissionDenied
			}
			if err != nil {
				return err
			}
			if persisted.ID != link.ID {
				return ErrPermissionDenied
			}
		}
		for _, user := range users {
			if err := bumpVersion(user); err != nil {
				return err
			}
			if err := a.saveUser(ctx, user, orm.SaveOptions{UpdateFields: []string{"auth_version", "updated_at"}}); err != nil {
				return err
			}
		}
		return nil
	})
}
