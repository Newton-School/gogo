package admin

import (
	"context"
	"errors"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// UserEditor is the privileged default-user form port. Every method requires
// the caller's active Admin transaction, so the mutation and redacted audit
// either commit together or both roll back. Implementations must apply the
// scoped store's actor, current scope and exact Accounts authority; a password
// argument must never enter display records, audit or diagnostic output.
type UserEditor interface {
	CreateUser(context.Context, string, string, auth.CreateUserOptions) (Object, error)
	ChangeUserPassword(context.Context, Object, string) (Object, error)
	SetUserUnusablePassword(context.Context, Object) (Object, error)
}

// UserWithoutPasswordCreator optionally enables the explicit password-disabled
// creation choice. This must create an unusable credential directly, not a
// temporary password followed by a second mutation.
type UserWithoutPasswordCreator interface {
	CreateUserWithoutPassword(context.Context, string, auth.CreateUserOptions) (Object, error)
}

var ErrAccountIdentifier = errors.New("admin: invalid account identifier")

func (s *accountScoped) userEditContext(ctx context.Context) (context.Context, error) {
	if s.schema.Key() != (&auth.User{}).Schema().Key() || !db.InTransaction(ctx, s.owner.config.Store.Backend.Alias()) {
		return nil, auth.ErrPermissionDenied
	}
	return auth.WithPrincipal(ctx, s.principal), ctx.Err()
}

func (s *accountScoped) CreateUser(ctx context.Context, identifier, password string, options auth.CreateUserOptions) (Object, error) {
	return s.createUser(ctx, identifier, password, options, false)
}

func (s *accountScoped) CreateUserWithoutPassword(ctx context.Context, identifier string, options auth.CreateUserOptions) (Object, error) {
	return s.createUser(ctx, identifier, "", options, true)
}

func (s *accountScoped) createUser(ctx context.Context, identifier, password string, options auth.CreateUserOptions, unusable bool) (Object, error) {
	ctx, err := s.userEditContext(ctx)
	if err != nil {
		return Object{}, err
	}
	normalized, err := s.accounts.NormalizeLoginIdentifier(identifier)
	if err != nil {
		return Object{}, ErrAccountIdentifier
	}
	model := s.accounts.Models().User()
	model.Identifier, model.Active, model.Staff, model.Superuser = normalized, !options.Inactive, options.Staff, options.Superuser
	prospective, err := models.Bind(model)
	if err != nil {
		return Object{}, err
	}
	var written Object
	err = s.Atomic(ctx, func(ctx context.Context) error {
		if err := s.owner.config.ValidateWrite(ctx, s.principal, prospective); err != nil {
			return err
		}
		// The domain service owns normalization. Keep its original raw input:
		// deterministic custom normalizers need not be idempotent.
		var user *auth.User
		var err error
		if unusable {
			user, err = s.accounts.CreateUserWithoutPassword(ctx, identifier, options)
		} else {
			user, err = s.accounts.CreateUser(ctx, identifier, password, options)
		}
		if err != nil {
			return err
		}
		record, err := models.Bind(user)
		if err != nil {
			return err
		}
		created, err := objectFromRecord(record)
		if err != nil {
			return err
		}
		written, err = s.checkedAccount(ctx, created.ID)
		return err
	})
	if err != nil {
		return Object{}, err
	}
	return written, nil
}

func (s *accountScoped) checkedAccount(ctx context.Context, key string) (Object, error) {
	written, err := s.Get(ctx, key, true)
	if errors.Is(err, ErrNotFound) {
		return Object{}, auth.ErrPermissionDenied
	}
	if err != nil {
		return Object{}, err
	}
	if err := s.validateAccountProjection(ctx, written); err != nil {
		return Object{}, err
	}
	// Validation is an application extension point. A nested domain mutation
	// must not leave a stale approved projection while changing the database.
	// This reread has no validation callback and retains the original scope.
	if err := s.recheckAccount(ctx, written); err != nil {
		return Object{}, err
	}
	return written, ctx.Err()
}

func (s *accountScoped) recheckAccount(ctx context.Context, expected Object) error {
	current, err := s.Get(ctx, expected.ID, true)
	if errors.Is(err, ErrNotFound) {
		return auth.ErrPermissionDenied
	}
	if err != nil {
		return err
	}
	if current.ID != expected.ID || current.Version != expected.Version {
		return ErrConflict
	}
	return ctx.Err()
}

func (s *accountScoped) validateAccountProjection(ctx context.Context, written Object) error {
	fingerprint, err := written.Record.Schema().Fingerprint()
	if err != nil {
		return err
	}
	before, err := objectFromRecord(written.Record)
	if err != nil {
		return err
	}
	persisted, database := written.Record.State().Persisted, written.Record.State().Database
	if err := s.owner.config.ValidateWrite(ctx, s.principal, written.Record); err != nil {
		return err
	}
	// Scope validators are read-only. A callback must not substitute a hidden
	// identity (or rewrite flags/version) in this hashless projection before
	// the subsequent account operation extracts its authorized target.
	checked, err := objectFromRecord(written.Record)
	if err != nil {
		return err
	}
	afterFingerprint, err := written.Record.Schema().Fingerprint()
	if err != nil {
		return err
	}
	if checked.ID != written.ID || checked.Version != before.Version || fingerprint != afterFingerprint || persisted != written.Record.State().Persisted || database != written.Record.State().Database {
		return auth.ErrPermissionDenied
	}
	return ctx.Err()
}

func (s *accountScoped) mutatePassword(ctx context.Context, object Object, change func(context.Context, string) error) (Object, error) {
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
		id, err := current.Record.Get("id")
		if err != nil {
			return err
		}
		if err := change(ctx, id.(string)); err != nil {
			return err
		}
		written, err = s.checkedAccount(ctx, object.ID)
		return err
	})
	if err != nil {
		return Object{}, err
	}
	return written, nil
}

func (s *accountScoped) ChangeUserPassword(ctx context.Context, object Object, password string) (Object, error) {
	return s.mutatePassword(ctx, object, func(ctx context.Context, id string) error { return s.accounts.ChangePassword(ctx, id, password) })
}

func (s *accountScoped) SetUserUnusablePassword(ctx context.Context, object Object) (Object, error) {
	return s.mutatePassword(ctx, object, s.accounts.SetUnusablePassword)
}
