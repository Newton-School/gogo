package admin

import (
	"context"
	"reflect"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// GroupAdmin returns an explicitly registered default group editor. Name
// creation and changes go through Accounts, not generic ORM writes. The Site
// policy and scoped store still govern every page and object; account authority
// separately approves the precise domain operation. Deletion remains denied
// until a domain-backed deletion policy is explicitly implemented.
func (s *AccountStore) GroupAdmin() ModelAdmin {
	return ModelAdmin{
		groupForms:  true,
		Schema:      (&auth.Group{}).Schema(),
		Fields:      []string{"name", "permissions"},
		ListDisplay: []string{"name"}, ListDisplayLinks: []string{"name"},
		SearchFields: []string{"name"}, Ordering: []string{"name"},
		ConstraintChecker: s.base.config.Store,
		Authorize: func(_ context.Context, _ auth.Principal, action string, _ Object) error {
			if action != "view" && action != "add" && action != "change" {
				return auth.ErrPermissionDenied
			}
			return nil
		},
	}
}

func (s *accountScoped) newGroup() (Object, error) {
	record, err := models.Bind(&auth.Group{})
	if err != nil {
		return Object{}, err
	}
	// Identity and grants are domain-owned; generic Initialize hooks may not
	// preassign a group ID or bypass Accounts.CreateGroup.
	return Object{Record: record, Label: s.schema.Name}, nil
}

func (s *accountScoped) saveGroupFields(ctx context.Context, object Object) (Object, error) {
	if s.schema.Key() != (&auth.Group{}).Schema().Key() || !db.InTransaction(ctx, s.owner.config.Store.Backend.Alias()) || object.Record == nil || object.Record.Schema().Key() != s.schema.Key() {
		return Object{}, auth.ErrPermissionDenied
	}
	ctx = auth.WithPrincipal(ctx, s.principal)
	var written Object
	err := s.Atomic(ctx, func(ctx context.Context) error {
		nameValue, err := object.Record.Get("name")
		if err != nil {
			return err
		}
		name, ok := nameValue.(string)
		if !ok {
			return auth.ErrPermissionDenied
		}
		if !object.Record.State().Persisted {
			id, err := object.Record.Get("id")
			if err != nil || id != "" || object.ID != "" {
				return auth.ErrPermissionDenied
			}
			prospective, err := objectFromRecord(object.Record)
			if err != nil {
				return err
			}
			if err := s.validateAccountProjection(ctx, prospective); err != nil {
				return err
			}
			group, err := s.accounts.CreateGroup(ctx, name)
			if err != nil {
				return err
			}
			record, err := models.Bind(group)
			if err != nil {
				return err
			}
			created, err := objectFromRecord(record)
			if err != nil {
				return err
			}
			written, err = s.checkedAccount(ctx, created.ID)
			return err
		}
		current, err := s.checkedAccount(ctx, object.ID)
		if err != nil {
			return err
		}
		if current.Version != object.Version {
			return ErrConflict
		}
		beforeID, err := current.Record.Get("id")
		if err != nil {
			return err
		}
		afterID, err := object.Record.Get("id")
		if err != nil || !reflect.DeepEqual(beforeID, afterID) {
			return auth.ErrPermissionDenied
		}
		if err := s.validateAccountProjection(ctx, object); err != nil {
			return err
		}
		if err := s.recheckAccount(ctx, current); err != nil {
			return err
		}
		beforeName, err := current.Record.Get("name")
		if err != nil {
			return err
		}
		if beforeName != name {
			if err := s.accounts.RenameGroup(ctx, beforeID.(string), name); err != nil {
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
