package auth

import (
	"context"
	"errors"
	"testing"
)

func TestIdentityMutationTokenScopesRequireExactUserOrGroupCeiling(t *testing.T) {
	for _, test := range []struct{ action, allowed, denied string }{
		{"change_identifier", "gogo_auth.change_user", "gogo_auth.change_group"},
		{"rename_group", "gogo_auth.change_group", "gogo_auth.change_user"},
		{"create_user", "gogo_auth.add_user", "gogo_auth.change_user"},
	} {
		for _, scope := range []string{test.allowed, test.denied} {
			principal, _ := ConstrainPrincipal(Principal{Authenticated: true, Active: true, Superuser: true}, []string{scope})
			calls := 0
			unusable := test.action == "create_user"
			accounts := &Accounts{authorize: func(_ context.Context, change AccountChange) error {
				calls++
				if change.Unusable != unusable {
					t.Fatal("credential mode lost")
				}
				return nil
			}}
			err := accounts.permit(WithPrincipal(context.Background(), principal), AccountChange{Action: test.action, Unusable: unusable})
			if scope == test.allowed && (err != nil || calls != 1) {
				t.Fatal("mapped account scope rejected", test.action, err)
			}
			if scope == test.denied && (!errors.Is(err, ErrPermissionDenied) || calls != 0) {
				t.Fatal("custom policy widened account ceiling", test.action, err)
			}
		}
	}
}
