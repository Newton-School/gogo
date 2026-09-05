package admin

import (
	"context"
	"reflect"
	"slices"

	"github.com/Newton-School/gogo/core/auth"
)

// Stock user forms allow these fields only. Each actual delta still belongs to
// Accounts, with its own exact authority; a user-change grant is not permission
// to rename an identity or escalate flags. The nested savepoint also prevents
// a caller from retaining an earlier delta after a later validation failure.
func (s *accountScoped) saveUserFields(ctx context.Context, object Object) (Object, error) {
	ctx, err := s.userEditContext(ctx)
	if err != nil {
		return Object{}, err
	}
	if object.Record == nil || object.Record.Schema().Key() != s.schema.Key() || !object.Record.State().Persisted {
		return Object{}, auth.ErrPermissionDenied
	}
	var written Object
	err = s.Atomic(ctx, func(ctx context.Context) error {
		current, err := s.checkedAccount(ctx, object.ID)
		if err != nil {
			return err
		}
		if current.Version != object.Version {
			return ErrConflict
		}
		for _, field := range current.Record.Schema().Fields {
			if slices.Contains([]string{"identifier", "active", "staff", "superuser"}, field.Name) {
				continue
			}
			before, err := current.Record.Get(field.Name)
			if err != nil {
				return err
			}
			after, err := object.Record.Get(field.Name)
			if err != nil || !reflect.DeepEqual(before, after) {
				return auth.ErrPermissionDenied
			}
		}
		if err := s.validateAccountProjection(ctx, object); err != nil {
			return err
		}
		if err := s.recheckAccount(ctx, current); err != nil {
			return err
		}
		id, err := current.Record.Get("id")
		if err != nil {
			return err
		}
		beforeIdentifier, err := current.Record.Get("identifier")
		if err != nil {
			return err
		}
		identifierValue, err := object.Record.Get("identifier")
		if err != nil {
			return err
		}
		identifier, ok := identifierValue.(string)
		if !ok {
			return auth.ErrPermissionDenied
		}
		flags, previousFlags := [3]bool{}, [3]bool{}
		for index, name := range []string{"active", "staff", "superuser"} {
			for _, target := range []struct {
				object Object
				flags  *[3]bool
			}{{object, &flags}, {current, &previousFlags}} {
				value, err := target.object.Record.Get(name)
				if err != nil {
					return err
				}
				flag, ok := value.(bool)
				if !ok {
					return auth.ErrPermissionDenied
				}
				target.flags[index] = flag
			}
		}
		// Domain normalization runs once for a changed raw identifier. Sending
		// the unchanged stored identity must not reapply a custom normalizer.
		if beforeIdentifier != identifier {
			if err := s.accounts.ChangeIdentifier(ctx, id.(string), identifier); err != nil {
				return err
			}
		}
		if flags != previousFlags {
			if err := s.accounts.SetAccountFlags(ctx, id.(string), flags[0], flags[1], flags[2]); err != nil {
				return err
			}
		}
		written, err = s.checkedAccount(ctx, object.ID)
		return err
	})
	if err != nil {
		return Object{}, err
	}
	return written, nil
}
