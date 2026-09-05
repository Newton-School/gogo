package admin

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
)

func TestUserCredentialFormsPreservePasswordsWithoutEcho(t *testing.T) {
	password := "  significant credential whitespace  "
	for _, create := range []bool{true, false} {
		data := url.Values{"identifier": {"alice"}, "password_mode": {"set"}, "password1": {password}, "password2": {password}}
		form, err := credentialForm(context.Background(), data, create, false)
		if err != nil || !form.IsValid() || form.Value("password1") != password {
			t.Fatal("password form changed credential", err)
		}
		html, err := form.Render("div")
		if err != nil || strings.Contains(string(html), password) {
			t.Fatal("credential echoed", err)
		}
		data.Set("password2", "different")
		form, _ = credentialForm(context.Background(), data, create, false)
		if form.IsValid() {
			t.Fatal("mismatched passwords accepted")
		}
	}
	form, err := credentialForm(context.Background(), url.Values{"password_mode": {"unusable"}, "password1": {""}, "password2": {""}}, false, true)
	if err != nil || !form.IsValid() {
		t.Fatal("explicit unusable credential needs a password", err)
	}
	form, _ = credentialForm(context.Background(), url.Values{"password_mode": {"set"}, "password1": {""}, "password2": {""}}, false, true)
	if form.IsValid() {
		t.Fatal("empty new credential accepted")
	}
	form, err = credentialForm(context.Background(), url.Values{"identifier": {"alice"}, "password_mode": {"unusable"}, "password1": {""}, "password2": {""}}, true, true)
	if err != nil || !form.IsValid() {
		t.Fatal("password-disabled creation requires an invented credential", err)
	}
}

func TestUserMutationPanicsAreRedactedAndNeverClaimRollback(t *testing.T) {
	err := invokeAccountMutation(func() error { panic("private credential material") })
	if !errors.Is(err, errAccountMutationUnknown) || strings.Contains(err.Error(), "private") || accountOutcome(err) != auth.PasswordChangeUnknown {
		t.Fatal("panic boundary leaked or asserted rollback", err)
	}
}
