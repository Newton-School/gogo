package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/sessions"
)

func TestSessionPermissionPolicyAllowsActiveStaffFromAdminCookie(t *testing.T) {
	users := adminUserStore(t, auth.User{AbstractUser: auth.AbstractUser{
		AbstractBaseUser: auth.AbstractBaseUser{ID: 7, IsActive: true},
		Username:         "staff",
		IsStaff:          true,
	}})
	sessionStore := sessions.NewDatabaseStore("secret")
	session := sessions.NewSession(time.Hour)
	session.Set("user_id", "7")
	if err := sessionStore.Save(context.Background(), session); err != nil {
		t.Fatalf("Save(session) error = %v", err)
	}

	policy := SessionPermissionPolicy{
		UserStore:    users,
		SessionStore: sessionStore,
		Cookie:       sessions.CookieOptions{Name: "sid", Path: "/"},
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	request.AddCookie(&http.Cookie{Name: "sid", Value: session.Key})

	if !policy.HasAccess(request) {
		t.Fatalf("HasAccess() = false, want true")
	}
	user, ok := policy.UserForRequest(request)
	if !ok || user.ID != 7 || !user.IsAuthenticated() || user.IsAnonymous() {
		t.Fatalf("UserForRequest() = %#v, %v", user, ok)
	}
}
