package admin

import (
	"net/http"
	"strconv"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/sessions"
)

// SessionPermissionPolicy authorizes admin requests from auth context or admin session cookies.
type SessionPermissionPolicy struct {
	UserStore    auth.UserIDLoader
	SessionStore sessions.Store
	Cookie       sessions.CookieOptions
}

// HasAccess reports whether a request belongs to an active staff user.
func (p SessionPermissionPolicy) HasAccess(r *http.Request) bool {
	user, ok := p.UserForRequest(r)
	return ok && user.IsActive && user.IsStaff && user.IsAuthenticated() && !user.IsAnonymous()
}

// UserForRequest returns the active user from context or session storage.
func (p SessionPermissionPolicy) UserForRequest(r *http.Request) (auth.User, bool) {
	if user, ok := auth.UserFromContext(r.Context()); ok && user.IsAuthenticated() && !user.IsAnonymous() {
		return user, true
	}
	if p.UserStore == nil || p.SessionStore == nil {
		return auth.User{}, false
	}
	options := normalizeAdminCookie(p.Cookie)
	cookie, err := r.Cookie(options.Name)
	if err != nil || cookie.Value == "" {
		return auth.User{}, false
	}
	session, ok, err := p.SessionStore.Load(r.Context(), cookie.Value)
	if err != nil || !ok {
		return auth.User{}, false
	}
	id, err := strconv.ParseInt(session.GetString("user_id"), 10, 64)
	if err != nil {
		return auth.User{}, false
	}
	user, ok, err := p.UserStore.FindByID(r.Context(), id)
	if err != nil || !ok || !user.IsActive {
		return auth.User{}, false
	}
	user.Authenticated = true
	user.Anonymous = false
	return user, true
}
