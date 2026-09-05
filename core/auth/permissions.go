// Package auth provides authentication and authorization contracts.
package auth

import (
	"context"
	"errors"
	"slices"
)

var (
	ErrUnauthenticated  = errors.New("authentication required")
	ErrPermissionDenied = errors.New("permission denied")
)

// Principal is a verified identity, never a claim copied directly from a client.
type Principal struct {
	ID                                      string
	Authenticated, Active, Staff, Superuser bool
	AuthVersion                             uint64
	Permissions                             []string
}

type Resource struct {
	App, Model string
	ID         any
	Object     any
}

// Policy is invoked after scoped lookup and before disclosing or mutating data.
// A policy must not broaden the resource store's tenant/object scope.
type Policy interface {
	Authorize(context.Context, Principal, string, Resource) error
}
type PolicyFunc func(context.Context, Principal, string, Resource) error

func (f PolicyFunc) Authorize(ctx context.Context, p Principal, a string, r Resource) error {
	if f == nil {
		return ErrPermissionDenied
	}
	return f(ctx, p, a, r)
}

// ModelPolicy checks app.action_model grants. Superusers do not bypass scope;
// their model permission bypass must also be enabled explicitly here.
type ModelPolicy struct{ AllowSuperuser bool }

func (m ModelPolicy) Authorize(ctx context.Context, p Principal, action string, r Resource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.Authenticated {
		return ErrUnauthenticated
	}
	if !p.Active {
		return ErrPermissionDenied
	}
	if m.AllowSuperuser && p.Superuser {
		return nil
	}
	if r.App != "" && r.Model != "" && action != "" && slices.Contains(p.Permissions, r.App+"."+action+"_"+r.Model) {
		return nil
	}
	return ErrPermissionDenied
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	p.Permissions = slices.Clone(p.Permissions)
	return context.WithValue(ctx, principalKey{}, p)
}
func FromContext(ctx context.Context) Principal {
	p, _ := ctx.Value(principalKey{}).(Principal)
	p.Permissions = slices.Clone(p.Permissions)
	return p
}
