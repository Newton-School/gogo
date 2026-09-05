package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Newton-School/gogo/core/security"
)

func TestTokenCeilingsNeverGrantOrWidenAuthority(t *testing.T) {
	ctx := context.Background()
	resource := Resource{App: "catalog", Model: "Product"}
	plain := Principal{ID: "verified", Authenticated: true, Active: true, Superuser: true, AuthVersion: 1, Permissions: []string{"catalog.view_product"}}
	scopes := []string{"catalog.view_product", "catalog.change_product"}
	constrained, err := ConstrainPrincipal(plain, scopes)
	if err != nil {
		t.Fatal(err)
	}
	scopes[0] = "catalog.delete_product"
	for _, policy := range []Policy{ModelPolicy{AllowSuperuser: true}, ConstrainPolicy(PolicyFunc(func(context.Context, Principal, string, Resource) error { return nil }))} {
		if policy.Authorize(ctx, constrained, "view", resource) != nil || !errors.Is(policy.Authorize(ctx, constrained, "delete", resource), ErrPermissionDenied) {
			t.Fatal("superuser/custom policy bypassed scope")
		}
	}
	if (ModelPolicy{}).Authorize(ctx, constrained, "change", resource) == nil {
		t.Fatal("token scope granted an absent user permission")
	}
	narrow, err := ConstrainPrincipal(constrained, []string{"catalog.change_product", "catalog.delete_product"})
	if err != nil {
		t.Fatal(err)
	}
	got, present := narrow.TokenScopes()
	if !present || !slices.Equal(got, []string{"catalog.change_product"}) {
		t.Fatal("reconstraint broadened authority")
	}
	got[0] = "catalog.delete_product"
	stored := WithPrincipal(ctx, narrow)
	narrow.tokenScopes[0] = "catalog.delete_product"
	copy := FromContext(stored)
	copy.tokenScopes[0] = "catalog.delete_product"
	if CheckTokenScope(FromContext(stored), "delete", resource) == nil {
		t.Fatal("context scope mutation escaped snapshot")
	}
	empty, err := ConstrainPrincipal(constrained, []string{"catalog.delete_product"})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []Principal{empty, FromContext(WithPrincipal(ctx, empty))} {
		if _, ok := value.TokenScopes(); !ok || CheckTokenScope(value, "delete", resource) == nil {
			t.Fatal("empty intersection lost restriction")
		}
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("JSON copy could discard restriction")
		}
	}
	if _, err := json.Marshal(plain); err != nil {
		t.Fatal("unconstrained serialization changed", err)
	}
	for _, invalid := range [][]string{nil, {}, {"*"}, {"catalog.*"}, {"catalog.view_product", "catalog.view_product"}, {"catalog.view_product.extra"}} {
		if _, err := ConstrainPrincipal(plain, invalid); err == nil {
			t.Fatal("invalid scopes accepted")
		}
	}
}

func TestTokenPrincipalCannotBecomeUnrestrictedSession(t *testing.T) {
	p, err := ConstrainPrincipal(Principal{ID: "verified", Authenticated: true, Active: true, AuthVersion: 1}, []string{"catalog.view_product"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/login", nil)
	w := httptest.NewRecorder()
	if err := Login(w, r, p, security.CSRFConfig{}); !errors.Is(err, ErrCredentials) || len(w.Result().Cookies()) != 0 {
		t.Fatal("token promoted to session", err)
	}
	*r = *r.WithContext(WithPrincipal(r.Context(), p))
	verified := Principal{ID: p.ID, Active: true, Authenticated: true, AuthVersion: 2}
	if err := RefreshLogin(w, r, verified, security.CSRFConfig{}); !errors.Is(err, ErrCredentials) {
		t.Fatal("token refreshed as unrestricted session", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	policy := ConstrainPolicy(PolicyFunc(func(context.Context, Principal, string, Resource) error { cancel(); return nil }))
	if err := policy.Authorize(ctx, p, "view", Resource{App: "catalog", Model: "Product"}); !errors.Is(err, context.Canceled) {
		t.Fatal("policy cancellation ignored", err)
	}
}

func TestAccountServicesApplyTokenScopeBeforeCustomAuthority(t *testing.T) {
	for _, test := range []struct{ action, scope string }{
		{"create_user", "gogo_auth.add_user"},
		{"change_password", "gogo_auth.change_user"},
		{"change_own_password", "gogo_auth.change_user"},
		{"reset_password", "gogo_auth.change_user"},
		{"change_account_flags", "gogo_auth.change_user"},
		{"set_user_permissions", "gogo_auth.change_user"},
		{"set_user_groups", "gogo_auth.change_user"},
		{"create_group", "gogo_auth.add_group"},
		{"set_group_permissions", "gogo_auth.change_group"},
	} {
		calls := 0
		accounts := &Accounts{authorize: func(context.Context, AccountChange) error { calls++; return nil }}
		plain := Principal{ID: "fixture-actor", Authenticated: true, Active: true, Superuser: true}
		denied, _ := ConstrainPrincipal(plain, []string{"catalog.view_product"})
		ctx := WithPrincipal(context.Background(), denied)
		if err := accounts.permit(ctx, AccountChange{Action: test.action}); !errors.Is(err, ErrPermissionDenied) || calls != 0 {
			t.Fatal("custom authority bypassed account scope", test.action, err)
		}
		allowed, _ := ConstrainPrincipal(plain, []string{test.scope})
		if err := accounts.permit(WithPrincipal(ctx, allowed), AccountChange{Action: test.action}); err != nil || calls != 1 {
			t.Fatal("mapped account scope rejected", test.action, err)
		}
		if err := accounts.permit(WithPrincipal(ctx, allowed), AccountChange{Action: "unmapped_future_action"}); !errors.Is(err, ErrPermissionDenied) || calls != 1 {
			t.Fatal("unknown action bypassed token ceiling")
		}
	}
}
