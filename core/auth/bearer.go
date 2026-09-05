package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

var ErrTokenUnavailable = errors.New("token authentication service unavailable")

// BearerMiddleware requires exactly one Authorization bearer credential. It
// never falls back to an existing cookie principal, query token or anonymous
// identity. It does not disable CSRF globally; mount it only on routes declared
// to use non-cookie authentication and wrap custom policies with ConstrainPolicy.
// Production transport must enforce HTTPS at the trusted server/proxy boundary.
// Custom backends must honor context and return a constrained current identity.
func BearerMiddleware(backend TokenBackend) (func(http.Handler) http.Handler, error) {
	if backend == nil {
		return nil, ErrTokenUnavailable
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Add("Vary", "Authorization")
			challenge := func() {
				w.Header().Set("WWW-Authenticate", `Bearer realm="gogo"`)
				http.Error(w, "invalid authentication credentials", http.StatusUnauthorized)
			}
			values := r.Header.Values("Authorization")
			if len(values) != 1 || len(values[0]) > 8192 {
				challenge()
				return
			}
			scheme, bearer, ok := strings.Cut(values[0], " ")
			// RFC 6750 permits one or more SP between scheme and credential.
			bearer = strings.TrimLeft(bearer, " ")
			if !ok || !strings.EqualFold(scheme, "Bearer") || bearer == "" {
				challenge()
				return
			}
			for _, b := range []byte(bearer) {
				if b <= ' ' || b >= 127 || b == ',' {
					challenge()
					return
				}
			}
			principal, err := authenticateToken(r.Context(), backend, bearer)
			if errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrCredentials) {
				challenge()
				return
			}
			if err != nil {
				http.Error(w, ErrTokenUnavailable.Error(), http.StatusServiceUnavailable)
				return
			}
			if !principal.Authenticated || !principal.Active || principal.ID == "" || principal.AuthVersion == 0 {
				challenge()
				return
			}
			if principal.tokenScopes == nil {
				http.Error(w, ErrTokenUnavailable.Error(), http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}, nil
}

func authenticateToken(ctx context.Context, backend TokenBackend, bearer string) (principal Principal, err error) {
	defer func() {
		if recover() != nil {
			principal, err = Principal{}, ErrTokenUnavailable
		}
	}()
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	principal, err = backend.AuthenticateToken(ctx, bearer)
	if ctx.Err() != nil {
		return Principal{}, ctx.Err()
	}
	return principal, err
}
