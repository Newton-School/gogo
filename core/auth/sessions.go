package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

// PrincipalLoader always returns current account state and current grants, not
// claims from a session cookie. Custom user models implement this public port.
type PrincipalLoader interface {
	LoadPrincipal(context.Context, string) (Principal, error)
}
type PrincipalLoaderFunc func(context.Context, string) (Principal, error)

func (f PrincipalLoaderFunc) LoadPrincipal(ctx context.Context, id string) (Principal, error) {
	return f(ctx, id)
}

const authSessionKey = "_gogo_auth"

type sessionIdentity struct {
	ID          string `json:"id"`
	AuthVersion uint64 `json:"auth_version"`
}

// SessionMiddleware runs inside sessions.Middleware. Invalidated accounts are
// anonymous and their session is flushed; an identity-store outage fails closed.
func SessionMiddleware(loader PrincipalLoader) (func(http.Handler) http.Handler, error) {
	if loader == nil {
		return nil, errors.New("auth: principal loader required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			session, ok := sessions.FromContext(r.Context())
			if !ok {
				http.Error(w, "authentication service unavailable", 503)
				return
			}
			var identity sessionIdentity
			found, err := session.Get(authSessionKey, &identity)
			if err != nil || found && (identity.ID == "" || identity.AuthVersion == 0) {
				if err := session.Flush(); err != nil {
					http.Error(w, "authentication service unavailable", 503)
					return
				}
				next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), Principal{})))
				return
			}
			if !found {
				next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), Principal{})))
				return
			}
			principal, err := loader.LoadPrincipal(r.Context(), identity.ID)
			if err != nil && !errors.Is(err, ErrUnauthenticated) {
				http.Error(w, "authentication service unavailable", 503)
				return
			}
			if err != nil || !principal.Active || principal.ID != identity.ID || principal.AuthVersion != identity.AuthVersion {
				if err := session.Flush(); err != nil {
					http.Error(w, "authentication service unavailable", 503)
					return
				}
				principal = Principal{}
			} else {
				principal.Authenticated = true
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}, nil
}

// Login accepts only a principal already verified by an authentication backend.
// The route remains responsible for CSRF and per-identity/IP throttling. It
// rotates CSRF and session identity before response headers; session middleware
// must successfully persist before an authenticated cookie can be emitted.
// Like other request-local operations, Login must not run concurrently with
// handlers using the same request or session.
func Login(w http.ResponseWriter, r *http.Request, p Principal, csrf security.CSRFConfig) error {
	if !p.Authenticated || !p.Active || p.ID == "" || p.AuthVersion == 0 {
		return ErrCredentials
	}
	session, ok := sessions.FromContext(r.Context())
	if !ok {
		return errors.New("auth: session middleware required")
	}
	var old sessionIdentity
	found, err := session.Get(authSessionKey, &old)
	if err != nil {
		return err
	}
	if err := security.RotateCSRFRequest(w, r, csrf); err != nil {
		return err
	}
	if found && (old.ID != p.ID || old.AuthVersion != p.AuthVersion) {
		if err := session.Flush(); err != nil {
			return err
		}
	} else {
		if err := session.CycleKey(); err != nil {
			return err
		}
	}
	if err := session.Set(authSessionKey, sessionIdentity{ID: p.ID, AuthVersion: p.AuthVersion}); err != nil {
		return err
	}
	*r = *r.WithContext(WithPrincipal(r.Context(), p))
	return nil
}

// Logout removes all state belonging to the old account. A subsequent session
// write (such as a generic logout message) receives a fresh anonymous identity.
func Logout(w http.ResponseWriter, r *http.Request, csrf security.CSRFConfig) error {
	session, ok := sessions.FromContext(r.Context())
	if !ok {
		return errors.New("auth: session middleware required")
	}
	if err := session.Flush(); err != nil {
		return err
	}
	*r = *r.WithContext(WithPrincipal(r.Context(), Principal{}))
	return security.RotateCSRFRequest(w, r, csrf)
}
