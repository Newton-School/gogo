package auth

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Newton-School/gogo/sessions"
)

type userContextKey struct{}

// Backend authenticates requests or restores users from external identity sources.
type Backend interface {
	Authenticate(context.Context, *http.Request) (User, bool, error)
	GetUser(context.Context, string) (User, bool, error)
}

// UserIDLoader loads users by primary key for authentication middleware.
type UserIDLoader interface {
	FindByID(ctx context.Context, id int64) (User, bool, error)
}

// ContextWithUser attaches a user to a context.
func ContextWithUser(ctx context.Context, user User) context.Context {
	return context.WithValue(ctx, userContextKey{}, user)
}

// UserFromContext returns the user attached by authentication middleware.
func UserFromContext(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userContextKey{}).(User)
	return user, ok
}

// AnonymousUser returns the built-in anonymous principal.
func AnonymousUser() User {
	return User{AbstractUser: AbstractUser{AbstractBaseUser: AbstractBaseUser{Anonymous: true, Authenticated: false}}}
}

// AuthenticationMiddleware attaches the authenticated or anonymous user.
func AuthenticationMiddleware(loader UserIDLoader) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := AnonymousUser()
			if loader != nil {
				if session, ok := sessions.SessionFromContext(r.Context()); ok {
					user = userFromSession(r.Context(), loader, session)
				}
			}
			next.ServeHTTP(w, r.WithContext(ContextWithUser(r.Context(), user)))
		})
	}
}

// BackendAuthenticationMiddleware attaches the first user authenticated by a backend.
func BackendAuthenticationMiddleware(backends ...Backend) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok, err := AuthenticateRequest(r.Context(), r, backends...)
			if err != nil {
				http.Error(w, "authentication failed", http.StatusInternalServerError)
				return
			}
			if !ok {
				user = AnonymousUser()
			}
			next.ServeHTTP(w, r.WithContext(ContextWithUser(r.Context(), user)))
		})
	}
}

// AuthenticateRequest returns the first user authenticated by the configured backends.
func AuthenticateRequest(ctx context.Context, r *http.Request, backends ...Backend) (User, bool, error) {
	for _, backend := range backends {
		if backend == nil {
			continue
		}
		user, ok, err := backend.Authenticate(ctx, r)
		if err != nil || ok {
			return user, ok, err
		}
	}
	return User{}, false, nil
}

// GetUserFromBackends returns the first user found by the configured backends.
func GetUserFromBackends(ctx context.Context, id string, backends ...Backend) (User, bool, error) {
	for _, backend := range backends {
		if backend == nil {
			continue
		}
		user, ok, err := backend.GetUser(ctx, id)
		if err != nil || ok {
			return user, ok, err
		}
	}
	return User{}, false, nil
}

func userFromSession(ctx context.Context, loader UserIDLoader, session *sessions.Session) User {
	value, ok := session.Get("user_id")
	if !ok || value == "" {
		return AnonymousUser()
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return AnonymousUser()
	}
	user, ok, err := loader.FindByID(ctx, id)
	if err != nil || !ok || !user.IsActive {
		return AnonymousUser()
	}
	user.Authenticated = true
	user.Anonymous = false
	return user
}
