package auth

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// TokenBackend authenticates a header credential to a current, constrained
// identity. Custom verifiers (including JWT/OIDC adapters) must verify their
// issuer/audience/algorithm allowlists and apply ConstrainPrincipal themselves.
// No JWT algorithm or remote identity provider is selected implicitly.
type TokenBackend interface {
	AuthenticateToken(context.Context, string) (Principal, error)
}

type TokenChange struct {
	Action, ID, UserID string
	Scopes             []string
	ExpiresAt          time.Time
}

type TokensConfig struct {
	Accounts *Accounts
	// Authorize is mandatory for mutations. It evaluates current actor
	// authority; an arbitrary submitted user ID is never an authorization.
	Authorize func(context.Context, TokenChange) error
	TTL       time.Duration // zero selects 24h; between one minute and 365 days
}

type Tokens struct {
	accounts  *Accounts
	authorize func(context.Context, TokenChange) error
	ttl       time.Duration
}

type TokenMutationState string

const (
	TokenUnchanged     TokenMutationState = "unchanged"
	TokenChanged       TokenMutationState = "changed"
	TokenChangeUnknown TokenMutationState = "unknown"
)

// TokenIssue carries a credential only after a confirmed successful commit.
// A committed callback failure is Changed without Secret; an unknown commit
// is Unknown without Secret. Do not automatically repeat either operation.
type TokenIssue struct {
	State  TokenMutationState
	ID     string
	Secret TokenSecret
}

func NewTokens(config TokensConfig) (*Tokens, error) {
	if config.Accounts == nil {
		return nil, errors.New("auth: token accounts required")
	}
	want := (&APITokenRecord{}).Schema()
	actual, ok := config.Accounts.store.Registry.Get(want.Key())
	wanted, e1 := want.Fingerprint()
	got, e2 := actual.Fingerprint()
	if !ok || e1 != nil || e2 != nil || wanted != got {
		return nil, errors.New("auth: matching API token model must be registered")
	}
	if config.TTL == 0 {
		config.TTL = 24 * time.Hour
	}
	if config.TTL < time.Minute || config.TTL > 365*24*time.Hour {
		return nil, errors.New("auth: invalid API token lifetime")
	}
	return &Tokens{accounts: config.Accounts, authorize: config.Authorize, ttl: config.TTL}, nil
}

func (t *Tokens) permit(ctx context.Context, change TokenChange) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.authorize == nil {
		return ErrPermissionDenied
	}
	actor := FromContext(ctx)
	if actor.tokenScopes != nil {
		action := ""
		switch change.Action {
		case "issue_token":
			action = "add"
		case "revoke_token":
			action = "delete"
		default:
			return ErrPermissionDenied
		}
		if !actor.Authenticated || !actor.Active {
			return ErrUnauthenticated
		}
		if err := CheckTokenScope(actor, action, Resource{App: "gogo_authtokens", Model: "APIToken"}); err != nil {
			return err
		}
	}
	change.Scopes = slices.Clone(change.Scopes)
	err := t.authorize(ctx, change)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func tokenState(err error) TokenMutationState {
	if err == nil {
		return TokenChanged
	}
	var committed *db.CommittedCallbackError
	if errors.As(err, &committed) {
		return TokenChanged
	}
	if db.IsCode(err, db.UnknownCommit) {
		return TokenChangeUnknown
	}
	return TokenUnchanged
}

// Issue uses an owned transaction so no secret escapes a pending outer commit.
// Scopes must be explicit current subject grants (or a current superuser's
// permission ceiling); authentication and resource policies recheck them later.
// Rotation is explicit: Issue a replacement, hand it off safely, then Revoke
// the old ID. Those are separate commits; neither claims atomic handoff.
func (t *Tokens) Issue(ctx context.Context, userID string, scopes []string) (TokenIssue, error) {
	result := TokenIssue{State: TokenUnchanged}
	if db.InTransaction(ctx, t.accounts.store.Backend.Alias()) {
		return result, errors.New("auth: token issuance requires an owned commit boundary")
	}
	if !t.accounts.models.validID(userID) {
		return result, ErrUnauthenticated
	}
	values, err := tokenScopes(scopes)
	if err != nil {
		return result, err
	}
	actor := FromContext(ctx)
	if actor.tokenScopes != nil {
		if actor.ID != userID {
			return result, ErrPermissionDenied
		}
		for _, scope := range values {
			if !slices.Contains(actor.tokenScopes, scope) {
				return result, ErrPermissionDenied
			}
		}
	}
	change := TokenChange{Action: "issue_token", UserID: userID, Scopes: values}
	if err := t.permit(ctx, change); err != nil {
		return result, err
	}
	id, secret, digest, err := newAPIToken()
	if err != nil {
		return result, err
	}
	issued := time.Now().UTC().Truncate(time.Microsecond)
	record := &APITokenRecord{ID: id, UserID: userID, SecretDigest: digest, Scopes: values, CreatedAt: issued, ExpiresAt: issued.Add(t.ttl)}
	change.ID, change.ExpiresAt = id, record.ExpiresAt
	err = db.Atomic(ctx, t.accounts.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		if _, err := orm.For(t.accounts.store, t.accounts.models.User).Filter(orm.Q("id", userID)).SelectForUpdate(false, false).Get(ctx); err != nil {
			return err
		}
		if err := t.permit(ctx, change); err != nil {
			return err
		}
		_, principal, err := t.accounts.snapshot(ctx, orm.Q("id", userID))
		if err != nil {
			return err
		}
		if !principal.Active || !principal.Authenticated {
			return ErrUnauthenticated
		}
		if actor.tokenScopes != nil && actor.AuthVersion != principal.AuthVersion {
			return ErrAccountChanged
		}
		for _, scope := range values {
			if !principal.Superuser && !slices.Contains(principal.Permissions, scope) {
				return ErrPermissionDenied
			}
		}
		record.AuthVersion = int64(principal.AuthVersion)
		return t.save(ctx, record, orm.SaveOptions{ForceInsert: true}, func(ctx context.Context, _ bool) error {
			if err := t.permit(ctx, change); err != nil {
				return err
			}
			current, err := t.accounts.LoadPrincipal(ctx, userID)
			if err != nil {
				return err
			}
			if !current.Active || current.AuthVersion != principal.AuthVersion {
				return ErrAccountChanged
			}
			return nil
		})
	})
	result.State = tokenState(err)
	if result.State != TokenUnchanged {
		result.ID = id
	}
	if err == nil {
		result.Secret = secret
	}
	return result, err
}

func copyAPIToken(record *APITokenRecord) APITokenRecord {
	copy := *record
	copy.Scopes = slices.Clone(record.Scopes)
	if record.RevokedAt != nil {
		value := *record.RevokedAt
		copy.RevokedAt = &value
	}
	return copy
}

func sameAPIToken(a, b *APITokenRecord) bool {
	return a.ID == b.ID && a.UserID == b.UserID && a.SecretDigest == b.SecretDigest && a.AuthVersion == b.AuthVersion && slices.Equal(a.Scopes, b.Scopes) && a.CreatedAt.Equal(b.CreatedAt) && a.ExpiresAt.Equal(b.ExpiresAt) && ((a.RevokedAt == nil && b.RevokedAt == nil) || (a.RevokedAt != nil && b.RevokedAt != nil && a.RevokedAt.Equal(*b.RevokedAt)))
}

func (t *Tokens) save(ctx context.Context, record *APITokenRecord, options orm.SaveOptions, check func(context.Context, bool) error) error {
	expected := copyAPIToken(record)
	options.Guard = func(ctx context.Context, _ models.Record) error {
		if check != nil {
			if err := check(ctx, false); err != nil {
				return err
			}
		}
		if !sameAPIToken(record, &expected) {
			return ErrAccountChanged
		}
		return nil
	}
	if err := t.accounts.store.Save(ctx, record, options); err != nil {
		return err
	}
	if check != nil {
		if err := check(ctx, true); err != nil {
			return err
		}
	}
	if !sameAPIToken(record, &expected) {
		return ErrAccountChanged
	}
	actual, err := orm.For(t.accounts.store, func() *APITokenRecord { return &APITokenRecord{} }).Filter(orm.Q("id", expected.ID)).Get(ctx)
	if err != nil {
		return err
	}
	if !sameAPIToken(actual, &expected) {
		return ErrAccountChanged
	}
	return ctx.Err()
}

// Revoke is idempotent but always reauthorizes the exact token owner/scopes.
// It does not alter other credentials or claim any in-flight request stopped.
func (t *Tokens) Revoke(ctx context.Context, id string) (TokenMutationState, error) {
	if !validUUID(id) {
		return TokenUnchanged, ErrToken
	}
	if db.InTransaction(ctx, t.accounts.store.Backend.Alias()) {
		return TokenUnchanged, errors.New("auth: token revocation requires an owned commit boundary")
	}
	attempted := false
	err := db.Atomic(ctx, t.accounts.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		record, err := orm.For(t.accounts.store, func() *APITokenRecord { return &APITokenRecord{} }).Filter(orm.Q("id", id)).SelectForUpdate(false, false).Get(ctx)
		if err != nil {
			return err
		}
		expected := copyAPIToken(record)
		change := TokenChange{Action: "revoke_token", ID: id, UserID: record.UserID, Scopes: record.Scopes, ExpiresAt: record.ExpiresAt}
		if err := t.permit(ctx, change); err != nil {
			return err
		}
		record, err = orm.For(t.accounts.store, func() *APITokenRecord { return &APITokenRecord{} }).Filter(orm.Q("id", id)).Get(ctx)
		if err != nil {
			return err
		}
		if !sameAPIToken(record, &expected) {
			return ErrAccountChanged
		}
		if record.RevokedAt != nil {
			return nil
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		record.RevokedAt = &now
		attempted = true
		if err := t.save(ctx, record, orm.SaveOptions{UpdateFields: []string{"revoked_at"}}, func(ctx context.Context, after bool) error {
			if err := t.permit(ctx, change); err != nil {
				return err
			}
			current, err := orm.For(t.accounts.store, func() *APITokenRecord { return &APITokenRecord{} }).Filter(orm.Q("id", id)).Get(ctx)
			if err != nil {
				return err
			}
			want := copyAPIToken(&expected)
			if after {
				value := now
				want.RevokedAt = &value
			}
			if !sameAPIToken(current, &want) {
				return ErrAccountChanged
			}
			return nil
		}); err != nil {
			return err
		}
		return nil
	})
	if !attempted {
		// Callback-only transactions cannot have changed this token. An
		// OnCommit error or uncertain read-only commit is not a revocation.
		return TokenUnchanged, err
	}
	return tokenState(err), err
}

func (t *Tokens) AuthenticateToken(ctx context.Context, bearer string) (Principal, error) {
	id, digest, err := parseAPIToken(bearer)
	if err != nil {
		return Principal{}, ErrToken
	}
	record, err := orm.For(t.accounts.store, func() *APITokenRecord { return &APITokenRecord{} }).Filter(orm.Q("id", id)).Get(ctx)
	if errors.Is(err, orm.ErrNotFound) {
		return Principal{}, ErrToken
	}
	if err != nil {
		return Principal{}, err
	}
	if !resetDigestEqual(record.SecretDigest, digest) || record.RevokedAt != nil || !time.Now().Before(record.ExpiresAt) || record.AuthVersion < 1 {
		return Principal{}, ErrToken
	}
	scopes, err := tokenScopes(record.Scopes)
	if err != nil {
		return Principal{}, ErrToken
	}
	principal, err := t.accounts.LoadPrincipal(ctx, record.UserID)
	if errors.Is(err, ErrUnauthenticated) {
		return Principal{}, ErrToken
	}
	if err != nil {
		return Principal{}, err
	}
	if !principal.Authenticated || !principal.Active || principal.AuthVersion != uint64(record.AuthVersion) {
		return Principal{}, ErrToken
	}
	current, err := orm.For(t.accounts.store, func() *APITokenRecord { return &APITokenRecord{} }).Filter(orm.Q("id", id)).Get(ctx)
	if errors.Is(err, orm.ErrNotFound) {
		return Principal{}, ErrToken
	}
	if err != nil {
		return Principal{}, err
	}
	if !sameAPIToken(record, current) || !time.Now().Before(current.ExpiresAt) {
		return Principal{}, ErrToken
	}
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	return ConstrainPrincipal(principal, scopes)
}
