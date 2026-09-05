package auth

import (
	"context"
	"strings"
	"testing"
)

func TestPasswordHashAndBoundedParams(t *testing.T) {
	p := PasswordParams{8192, 1, 1}
	hash, e := HashPasswordWith("a reasonably long secret", p)
	if e != nil {
		t.Fatal(e)
	}
	ok, rehash, e := VerifyPassword("a reasonably long secret", hash)
	if e != nil || !ok || !rehash {
		t.Fatal(ok, rehash, e)
	}
	if ok, _, _ := VerifyPassword("bad", hash); ok {
		t.Fatal("wrong password")
	}
	if _, _, e = VerifyPassword("bad", strings.Replace(hash, "m=8192", "m=4294967295", 1)); e == nil {
		t.Fatal("unbounded hash accepted")
	}
}
func TestPolicyAndContextCopies(t *testing.T) {
	p := Principal{ID: "1", Authenticated: true, Active: true, Superuser: true, Permissions: []string{"catalog.view_product"}}
	ctx := WithPrincipal(context.Background(), p)
	p.Permissions[0] = "bad"
	got := FromContext(ctx)
	policy := ModelPolicy{}
	if policy.Authorize(ctx, got, "view", Resource{App: "catalog", Model: "product"}) != nil {
		t.Fatal("grant lost")
	}
	if policy.Authorize(ctx, got, "delete", Resource{App: "catalog", Model: "product"}) == nil {
		t.Fatal("superuser implicitly bypassed")
	}
	got.Superuser = false
	if err := policy.Authorize(ctx, got, "view", Resource{App: "catalog", Model: "Product"}); err != nil {
		t.Fatal("Go model name did not resolve lowercase permission codename", err)
	}
}
