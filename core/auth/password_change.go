package auth

import (
	"context"
	"crypto/subtle"
	"errors"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type PasswordChangeState string

const (
	PasswordUnchanged     PasswordChangeState = "unchanged"
	PasswordChanged       PasswordChangeState = "changed"
	PasswordChangeUnknown PasswordChangeState = "unknown"
)

// PasswordChangeResult distinguishes a rejected change from a durable change
// whose after-commit effects failed, and from an unknown commit outcome. Never
// repeat a change automatically after Unknown. Principal is provided only after
// confirmed successful commit and carries exactly that transition's version,
// not a later account version whose credentials were not verified here.
type PasswordChangeResult struct {
	State     PasswordChangeState
	Principal Principal
}

// ChangeOwnPassword requires a currently authenticated principal, its current
// password, and explicit "change_own_password" authority. It never accepts a
// user ID from submitted form data. Password work is bounded; a concurrent
// credential/grant transition invalidates the earlier verification. This method
// owns its durable transaction so a returned principal may safely be used to
// rotate the current session. Use ChangePassword for privileged caller-owned
// transactions instead; it does not assert that self-service proof occurred.
func (a *Accounts) ChangeOwnPassword(ctx context.Context, oldPassword, newPassword string) (PasswordChangeResult, error) {
	result := PasswordChangeResult{State: PasswordUnchanged}
	actor := FromContext(ctx)
	if !actor.Authenticated || !actor.Active || !validAccountID(actor.ID) || actor.AuthVersion == 0 {
		return result, ErrUnauthenticated
	}
	if db.InTransaction(ctx, a.store.Backend.Alias()) {
		return result, errors.New("auth: self password change requires an owned commit boundary")
	}
	change := AccountChange{Action: "change_own_password", UserID: actor.ID}
	if err := a.permit(ctx, change); err != nil {
		return result, err
	}
	user, principal, err := a.snapshot(ctx, orm.Q("id", actor.ID))
	if err != nil {
		return result, err
	}
	if !principal.Active || principal.AuthVersion != actor.AuthVersion {
		return result, ErrAccountChanged
	}
	previous := storedHash(user)
	if err := a.verifyCurrentPassword(ctx, oldPassword, previous); err != nil {
		return result, err
	}
	var verified Principal
	err = a.withUser(ctx, change, func(ctx context.Context, current *User) error {
		if !current.Active || uint64(current.AuthVersion) != actor.AuthVersion || subtle.ConstantTimeCompare([]byte(previous), []byte(storedHash(current))) != 1 {
			return ErrAccountChanged
		}
		// Validators see the locked, version-matched principal. Run them before
		// any write; rejection leaves both account and session unchanged.
		hash, err := a.hash(ctx, newPassword, principal)
		if err != nil {
			return err
		}
		if err := bumpVersion(current); err != nil {
			return err
		}
		current.PasswordHash = &hash
		if err := a.saveUser(ctx, current, orm.SaveOptions{UpdateFields: []string{"password_hash", "auth_version", "updated_at"}, Guard: func(ctx context.Context, _ models.Record) error {
			// Trusted callbacks may themselves invoke account services using a
			// nested savepoint. The locked in-memory object then becomes stale.
			// Recheck persisted proof after validators and all BeforeSave hooks,
			// immediately before SQL, not only before calling those extensions.
			persisted, err := orm.For(a.store, func() *User { return &User{} }).Filter(orm.Q("id", actor.ID)).Only("active", "auth_version", "password_hash").Get(ctx)
			if errors.Is(err, orm.ErrNotFound) {
				return ErrAccountChanged
			}
			if err != nil {
				return err
			}
			if !persisted.Active || uint64(persisted.AuthVersion) != actor.AuthVersion || subtle.ConstantTimeCompare([]byte(previous), []byte(storedHash(persisted))) != 1 {
				return ErrAccountChanged
			}
			return nil
		}}); err != nil {
			return err
		}
		verified = principal
		verified.AuthVersion = uint64(current.AuthVersion)
		verified.Authenticated = true
		return nil
	})
	if err != nil {
		var committed *db.CommittedCallbackError
		switch {
		case errors.As(err, &committed):
			result.State = PasswordChanged
		case db.IsCode(err, db.UnknownCommit):
			result.State = PasswordChangeUnknown
		}
		return result, err
	}
	result.State = PasswordChanged
	result.Principal = verified
	return result, nil
}

func (a *Accounts) verifyCurrentPassword(ctx context.Context, password, encoded string) error {
	if password == "" || len(password) > 4096 {
		return ErrCredentials
	}
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	valid, _, err := VerifyPassword(password, encoded)
	if err != nil {
		// An unusable/malformed credential must not become a cheap verification
		// path. The result is intentionally discarded and never persisted.
		_, _ = HashPassword("unusable-self-change-verification")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !valid {
		return ErrCredentials
	}
	return nil
}
