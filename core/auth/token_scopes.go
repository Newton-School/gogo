package auth

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/models"
)

// TokenScopes returns an independent scope ceiling and whether it is present.
// An unconstrained session identity has no ceiling. An empty constrained set
// denies every action; neither an empty nor a wildcard token is unrestricted.
func (p Principal) TokenScopes() ([]string, bool) {
	return slices.Clone(p.tokenScopes), p.tokenScopes != nil
}

func tokenScopes(scopes []string) ([]string, error) {
	if len(scopes) < 1 || len(scopes) > 256 {
		return nil, errors.New("auth: tokens require one to 256 explicit permission scopes")
	}
	values := slices.Clone(scopes)
	slices.Sort(values)
	for i, scope := range values {
		app, codename, ok := strings.Cut(scope, ".")
		if !ok || !models.ValidIdentifier(app) || !models.ValidIdentifier(codename) || len(app) > 128 || len(codename) > 128 || i > 0 && values[i-1] == scope {
			return nil, errors.New("auth: invalid or duplicate token scope")
		}
	}
	return values, nil
}

// ConstrainPrincipal applies an additional non-widening permission ceiling to
// a verified principal. Scopes are exact app.codename grants, not wildcards or
// a grant source. ModelPolicy also checks the user's underlying authority;
// wrap a custom Policy with ConstrainPolicy. Never reconstruct a principal
// from its public fields to discard a token's restriction.
func ConstrainPrincipal(p Principal, scopes []string) (Principal, error) {
	values, err := tokenScopes(scopes)
	if err != nil {
		return Principal{}, err
	}
	if p.tokenScopes != nil {
		values = slices.DeleteFunc(values, func(value string) bool { return !slices.Contains(p.tokenScopes, value) })
	}
	p.tokenScopes = append([]string{}, values...)
	p.Permissions = slices.Clone(p.Permissions)
	return p, nil
}

// CheckTokenScope checks only the ceiling, not authentication or the current
// user's grants/object policy. Permission always requires both checks.
func CheckTokenScope(p Principal, action string, resource Resource) error {
	if p.tokenScopes == nil {
		return nil
	}
	if resource.App == "" || resource.Model == "" || action == "" || !slices.Contains(p.tokenScopes, resource.App+"."+action+"_"+strings.ToLower(resource.Model)) {
		return ErrPermissionDenied
	}
	return nil
}

type constrainedPolicy struct{ policy Policy }

func (p constrainedPolicy) Authorize(ctx context.Context, principal Principal, action string, resource Resource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.policy == nil {
		return ErrPermissionDenied
	}
	if principal.tokenScopes != nil {
		if !principal.Authenticated {
			return ErrUnauthenticated
		}
		if !principal.Active {
			return ErrPermissionDenied
		}
	}
	if err := CheckTokenScope(principal, action, resource); err != nil {
		return err
	}
	err := p.policy.Authorize(ctx, principal, action, resource)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// ConstrainPolicy enforces a token ceiling before invoking a custom policy.
// It never replaces the underlying object's or tenant's authorization.
// Unconstrained identities retain that policy's explicit anonymous-access rules.
func ConstrainPolicy(policy Policy) Policy { return constrainedPolicy{policy: policy} }

// Account service operations share the corresponding model permission ceiling;
// their explicit AccountChange policy must still authorize the exact delta.
// Unknown operations fail closed for token identities until mapped deliberately.
func checkAccountTokenScope(p Principal, change AccountChange) error {
	if p.tokenScopes == nil {
		return nil
	}
	action, model := "", ""
	switch change.Action {
	case "create_user":
		action, model = "add", "User"
	case "change_password", "change_own_password", "reset_password", "change_account_flags", "set_user_permissions", "set_user_groups", "change_identifier":
		action, model = "change", "User"
	case "create_group":
		action, model = "add", "Group"
	case "set_group_permissions", "rename_group":
		action, model = "change", "Group"
	default:
		return ErrPermissionDenied
	}
	if !p.Authenticated || !p.Active {
		return ErrUnauthenticated
	}
	return CheckTokenScope(p, action, Resource{App: "gogo_auth", Model: model})
}

// Token restrictions must not silently disappear when an identity is copied
// through JSON. Public API responses should use an explicit display DTO, not a
// serialized verified identity. Unconstrained principal behavior is unchanged.
func (p Principal) MarshalJSON() ([]byte, error) {
	if p.tokenScopes != nil {
		return nil, errors.New("auth: constrained principal cannot be serialized")
	}
	type plain Principal
	return json.Marshal(plain(p))
}
