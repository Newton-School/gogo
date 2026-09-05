package admin

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
)

// AccountGrantEditor applies stock selectors through the Accounts domain in
// the caller's active Admin transaction. Submitted identities require current
// target-model, target-object and row-scope authority, including unchanged
// submitted choices. Hidden existing links are retained server-side, but the
// Accounts authorizer must still approve the complete resulting grant set.
// Implementations must not expose retained identities through returned errors,
// audit changes or display choices. Missing map entries leave a family intact.
type AccountGrantEditor interface {
	AccountGrantReader
	SaveAccountGrants(context.Context, Object, map[AccountGrantKind][]string, AccountGrantAuthorizer) (Object, error)
}

func accountGrantKinds(object Object) []AccountGrantKind {
	if object.Record == nil {
		return nil
	}
	switch object.Record.Schema().Key() {
	case (&auth.User{}).Schema().Key():
		return []AccountGrantKind{UserGroups, UserPermissions}
	case (&auth.Group{}).Schema().Key():
		return []AccountGrantKind{GroupPermissions}
	default:
		return nil
	}
}

func (s *accountScoped) resolveGrant(ctx context.Context, kind AccountGrantKind, id string, authorize AccountGrantAuthorizer) (Object, error) {
	descriptor, err := describeGrant(kind)
	if err != nil {
		return Object{}, err
	}
	key, err := grantKey(descriptor.target, id)
	if err != nil {
		return Object{}, err
	}
	target, err := s.grantTarget(ctx, descriptor)
	if err != nil {
		return Object{}, err
	}
	object, err := target.Get(ctx, key, true)
	if err != nil {
		return Object{}, err
	}
	if err := checkGrantTarget(ctx, kind, object, authorize); err != nil {
		return Object{}, err
	}
	return s.recheckGrantTarget(ctx, kind, object)
}

func (s *accountScoped) recheckGrantTarget(ctx context.Context, kind AccountGrantKind, object Object) (Object, error) {
	descriptor, err := describeGrant(kind)
	if err != nil {
		return Object{}, err
	}
	// A read-only policy may call application code. Refresh both the trusted
	// scope and persisted projection after it; do not accept a changed target
	// snapshot or invoke another callback after this final verification.
	target, err := s.grantTarget(ctx, descriptor)
	if err != nil {
		return Object{}, err
	}
	current, err := target.Get(ctx, object.ID, db.InTransaction(ctx, s.owner.config.Store.Backend.Alias()))
	if err != nil {
		return Object{}, err
	}
	if current.ID != object.ID || current.Version != object.Version {
		return Object{}, ErrConflict
	}
	return current, ctx.Err()
}

func (s *accountScoped) grantState(ctx context.Context, object Object) (map[AccountGrantKind][]string, error) {
	state := make(map[AccountGrantKind][]string)
	for _, kind := range accountGrantKinds(object) {
		descriptor, _ := describeGrant(kind)
		ids, err := s.currentGrantIDs(ctx, object, descriptor)
		if err != nil {
			return nil, err
		}
		state[kind] = ids
	}
	return state, nil
}

func (s *accountScoped) recheckGrantState(ctx context.Context, object Object, expected map[AccountGrantKind][]string) error {
	actual, err := s.grantState(ctx, object)
	if err != nil {
		return err
	}
	for kind, ids := range expected {
		if !slices.Equal(actual[kind], ids) {
			return ErrConflict
		}
	}
	return ctx.Err()
}

func grantScalarState(object Object) (map[string]any, int64, error) {
	values := make(map[string]any)
	var version int64
	for _, field := range object.Record.Schema().Fields {
		value, err := object.Record.Get(field.Name)
		if err != nil {
			return nil, 0, err
		}
		if object.Record.Schema().Key() == (&auth.User{}).Schema().Key() {
			if field.Name == "auth_version" {
				var ok bool
				version, ok = value.(int64)
				if !ok {
					return nil, 0, auth.ErrPermissionDenied
				}
				continue
			}
			if field.Name == "updated_at" {
				continue
			}
		}
		values[field.Name] = value
	}
	return values, version, nil
}

func (s *accountScoped) SaveAccountGrants(ctx context.Context, object Object, submitted map[AccountGrantKind][]string, authorize AccountGrantAuthorizer) (Object, error) {
	kinds := accountGrantKinds(object)
	if len(kinds) == 0 || object.Record.Schema().Key() != s.schema.Key() || !object.Record.State().Persisted || authorize == nil || !db.InTransaction(ctx, s.owner.config.Store.Backend.Alias()) {
		return Object{}, auth.ErrPermissionDenied
	}
	for kind := range submitted {
		if !slices.Contains(kinds, kind) {
			return Object{}, auth.ErrPermissionDenied
		}
	}
	// Freeze caller-owned slices before the first application callback.
	frozen := make(map[AccountGrantKind][]string, len(submitted))
	for kind, ids := range submitted {
		frozen[kind] = slices.Clone(ids)
	}
	submitted = frozen
	ctx = auth.WithPrincipal(ctx, s.principal)
	var written Object
	err := s.Atomic(ctx, func(ctx context.Context) error {
		// Check all target models before reading even the parent's grant state.
		for _, kind := range kinds {
			if _, present := submitted[kind]; present {
				if err := checkGrantModel(ctx, kind, authorize); err != nil {
					return err
				}
			}
		}
		current, err := s.checkedAccount(ctx, object.ID)
		if err != nil {
			return err
		}
		if current.Version != object.Version {
			return ErrConflict
		}
		before, err := s.grantState(ctx, current)
		if err != nil {
			return err
		}
		proposed := make(map[AccountGrantKind][]string, len(before))
		revalidate := make(map[AccountGrantKind][]string)
		for _, kind := range kinds {
			proposed[kind] = slices.Clone(before[kind])
			posted, present := submitted[kind]
			if !present {
				continue
			}
			if len(posted) > maxAccountGrantChoices {
				return auth.ErrPermissionDenied
			}
			posted = slices.Clone(posted)
			slices.Sort(posted)
			if len(slices.Compact(slices.Clone(posted))) != len(posted) {
				return auth.ErrPermissionDenied
			}
			for _, id := range posted {
				if _, err := s.resolveGrant(ctx, kind, id, authorize); err != nil {
					if errors.Is(err, ErrNotFound) {
						return auth.ErrPermissionDenied
					}
					return err
				}
			}
			proposed[kind] = slices.Clone(posted)
			revalidate[kind] = slices.Clone(posted)
			for _, id := range before[kind] {
				if slices.Contains(posted, id) {
					continue
				}
				if _, err := s.resolveGrant(ctx, kind, id, authorize); err != nil {
					if errors.Is(err, ErrNotFound) || errors.Is(err, auth.ErrPermissionDenied) {
						proposed[kind] = append(proposed[kind], id)
						continue
					}
					return err
				}
				// Visible removals must remain authorized after domain hooks too.
				revalidate[kind] = append(revalidate[kind], id)
			}
			slices.Sort(proposed[kind])
		}
		if err := s.recheckAccount(ctx, current); err != nil {
			return err
		}
		if err := s.recheckGrantState(ctx, current, before); err != nil {
			return err
		}
		beforeScalars, version, err := grantScalarState(current)
		if err != nil {
			return err
		}
		id, err := current.Record.Get("id")
		if err != nil {
			return err
		}
		for _, kind := range kinds {
			if slices.Equal(before[kind], proposed[kind]) {
				continue
			}
			if kind == UserGroups {
				err = s.accounts.SetUserGroups(ctx, id.(string), proposed[kind])
			} else {
				ids := make([]int64, len(proposed[kind]))
				for i, value := range proposed[kind] {
					ids[i], err = strconv.ParseInt(value, 10, 64)
					if err != nil {
						return auth.ErrPermissionDenied
					}
				}
				if kind == UserPermissions {
					err = s.accounts.SetUserPermissions(ctx, id.(string), ids)
				} else {
					err = s.accounts.SetGroupPermissions(ctx, id.(string), ids)
				}
			}
			if err != nil {
				return err
			}
			if kind != GroupPermissions {
				version++
			}
		}
		written, err = s.checkedAccount(ctx, current.ID)
		if err != nil {
			return err
		}
		afterScalars, afterVersion, err := grantScalarState(written)
		if err != nil {
			return err
		}
		if afterVersion != version || !reflect.DeepEqual(beforeScalars, afterScalars) {
			return ErrConflict
		}
		var finalTargets []struct {
			kind   AccountGrantKind
			object Object
		}
		for _, kind := range kinds {
			if _, present := submitted[kind]; !present {
				continue
			}
			if err := checkGrantModel(ctx, kind, authorize); err != nil {
				return err
			}
			for _, id := range revalidate[kind] {
				target, err := s.resolveGrant(ctx, kind, id, authorize)
				if err != nil {
					if errors.Is(err, ErrNotFound) {
						return auth.ErrPermissionDenied
					}
					return err
				}
				finalTargets = append(finalTargets, struct {
					kind   AccountGrantKind
					object Object
				}{kind, target})
			}
		}
		// A later target callback may change eligibility for an earlier posted
		// target without changing the parent or links. Recheck every approved
		// projection after all callbacks, across every submitted family.
		for _, target := range finalTargets {
			if _, err := s.recheckGrantTarget(ctx, target.kind, target.object); err != nil {
				if errors.Is(err, ErrNotFound) {
					return auth.ErrPermissionDenied
				}
				return err
			}
		}
		if err := s.recheckAccount(ctx, written); err != nil {
			return err
		}
		return s.recheckGrantState(ctx, written, proposed)
	})
	if err != nil {
		return Object{}, err
	}
	return written, nil
}

var _ AccountGrantEditor = (*accountScoped)(nil)
