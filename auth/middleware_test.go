package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Newton-School/gogo/sessions"
)

func TestAuthMiddlewareAttachesAuthenticatedUserFromSession(t *testing.T) {
	password, err := MakePassword("secret")
	if err != nil {
		t.Fatalf("MakePassword() error = %v", err)
	}
	users, err := NewMemoryUserStore(User{AbstractUser: AbstractUser{
		AbstractBaseUser: AbstractBaseUser{ID: 42, Password: password, IsActive: true},
		Username:         "saksham",
	}})
	if err != nil {
		t.Fatalf("NewMemoryUserStore() error = %v", err)
	}
	sessionStore := sessions.NewDatabaseStore("secret")
	session := sessions.NewSession(time.Hour)
	session.Set("user_id", "42")
	if err := sessionStore.Save(context.Background(), session); err != nil {
		t.Fatalf("Save(session) error = %v", err)
	}

	handler := sessions.SessionMiddleware(sessionStore, sessions.CookieOptions{Name: "sid"})(
		AuthenticationMiddleware(users)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := UserFromContext(r.Context())
			if !ok || user.ID != 42 || !user.IsAuthenticated() || user.IsAnonymous() {
				t.Fatalf("context user = %#v, %v", user, ok)
			}
			if _, ok := sessions.SessionFromContext(r.Context()); !ok {
				t.Fatalf("session should be available before auth middleware")
			}
		})),
	)
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: "sid", Value: session.Key})
	handler.ServeHTTP(httptest.NewRecorder(), request)
}

func TestAuthMiddlewareAttachesAnonymousForMissingInactiveOrExpiredSession(t *testing.T) {
	password, err := MakePassword("secret")
	if err != nil {
		t.Fatalf("MakePassword() error = %v", err)
	}
	users, err := NewMemoryUserStore(User{AbstractUser: AbstractUser{
		AbstractBaseUser: AbstractBaseUser{ID: 2, Password: password, IsActive: false},
		Username:         "inactive",
	}})
	if err != nil {
		t.Fatalf("NewMemoryUserStore() error = %v", err)
	}
	sessionStore := sessions.NewDatabaseStore("secret")
	expired := sessions.NewSession(time.Hour)
	expired.Set("user_id", "2")
	expired.ExpireDate = time.Now().Add(-time.Second)
	if err := sessionStore.Save(context.Background(), expired); err != nil {
		t.Fatalf("Save(expired) error = %v", err)
	}

	for _, request := range []*http.Request{
		httptest.NewRequest("GET", "/", nil),
		func() *http.Request {
			r := httptest.NewRequest("GET", "/", nil)
			r.AddCookie(&http.Cookie{Name: "sid", Value: expired.Key})
			return r
		}(),
	} {
		handler := sessions.SessionMiddleware(sessionStore, sessions.CookieOptions{Name: "sid"})(
			AuthenticationMiddleware(users)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, ok := UserFromContext(r.Context())
				if !ok || !user.IsAnonymous() || user.IsAuthenticated() {
					t.Fatalf("anonymous user = %#v, %v", user, ok)
				}
			})),
		)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
}

func TestBackendAuthenticationMiddlewareUsesCustomBackend(t *testing.T) {
	backend := headerBackend{}
	handler := BackendAuthenticationMiddleware(backend)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := UserFromContext(r.Context())
		if !ok || user.ID != 99 || user.Username != "external" || !user.IsAuthenticated() {
			t.Fatalf("context user = %#v, %v", user, ok)
		}
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-External-User", "external")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	anonymous := BackendAuthenticationMiddleware(backend)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := UserFromContext(r.Context())
		if !ok || !user.IsAnonymous() {
			t.Fatalf("anonymous user = %#v, %v", user, ok)
		}
	}))
	anonymous.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

type headerBackend struct{}

func (headerBackend) Authenticate(_ context.Context, r *http.Request) (User, bool, error) {
	if r.Header.Get("X-External-User") != "external" {
		return User{}, false, nil
	}
	return User{AbstractUser: AbstractUser{
		AbstractBaseUser: AbstractBaseUser{ID: 99, IsActive: true, Authenticated: true},
		Username:         "external",
	}}, true, nil
}

func (headerBackend) GetUser(_ context.Context, id string) (User, bool, error) {
	if id != "99" {
		return User{}, false, nil
	}
	return User{AbstractUser: AbstractUser{
		AbstractBaseUser: AbstractBaseUser{ID: 99, IsActive: true, Authenticated: true},
		Username:         "external",
	}}, true, nil
}
