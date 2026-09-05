package auth

import (
	"context"
	"errors"
	"reflect"
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

func TestRehashNeverLowersExistingWorkFactors(t *testing.T) {
	for _, p := range []PasswordParams{{64 * 1024, 4, 1}, {8 * 1024, 4, 1}, {64 * 1024, 3, 4}, DefaultPasswordParams()} {
		if passwordUpgradeNeeded(p) {
			t.Fatalf("would lower or needlessly replace existing profile: %+v", p)
		}
	}
	if !passwordUpgradeNeeded(PasswordParams{8192, 1, 1}) {
		t.Fatal("weaker profile was not upgraded")
	}
}

func TestMalformedCredentialPerformsDummyVerification(t *testing.T) {
	var calls []string
	valid, rehash, err := verifyCredential("supplied", "malformed", "dummy", func(password, encoded string) (bool, bool, error) {
		calls = append(calls, encoded)
		if password != "supplied" {
			t.Fatal("dummy work did not use supplied password")
		}
		if encoded == "malformed" {
			return false, false, ErrPasswordHash
		}
		return false, false, nil
	})
	if valid || rehash || !errors.Is(err, ErrPasswordHash) || !reflect.DeepEqual(calls, []string{"malformed", "dummy"}) {
		t.Fatalf("dummy verification: %v %v %v %v", valid, rehash, err, calls)
	}
}

func TestUnusablePasswordsAreDistinctAndNeverAuthenticate(t *testing.T) {
	one, err := UnusablePassword()
	if err != nil {
		t.Fatal(err)
	}
	two, err := UnusablePassword()
	if err != nil || one == two || HasUsablePassword(one) || HasUsablePassword("") {
		t.Fatal("invalid unusable password markers")
	}
	if valid, _, _ := VerifyPassword("", one); valid {
		t.Fatal("unusable credential accepted an empty password")
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
