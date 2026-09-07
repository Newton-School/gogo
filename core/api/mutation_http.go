package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/auth"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/security"
)

// All explicit resource mutations share authentication/CSRF and the outer
// outcome boundary. Business callbacks seal their JSON before committing.
func mutationHandler(methods []string, maxBytes int64, config *security.CSRFConfig, run func(*http.Request) (IdempotencyResult, error)) (http.Handler, error) {
	methods = slices.Clone(methods)
	csrf := security.CSRFConfig{Secure: true, MaxBodyBytes: maxBytes}
	if config != nil {
		csrf = *config
		csrf.TrustedOrigins = slices.Clone(csrf.TrustedOrigins)
		if csrf.Exempt != nil {
			return nil, errors.New("api: mutation CSRF exemptions are authentication-owned")
		}
		csrf.MaxBodyBytes = maxBytes
	}
	csrf.Exempt = func(r *http.Request) bool {
		p := auth.FromContext(r.Context())
		_, scoped := p.TokenScopes()
		return scoped && p.Authenticated && p.Active
	}
	protect, err := security.CSRF(csrf)
	if err != nil {
		return nil, err
	}
	handler := protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := run(r)
		w.Header().Set("X-Gogo-Mutation", string(result.Outcome))
		if err != nil {
			public := publicMutationError(err)
			if result.Outcome == MutationUnknown {
				public = mediaError(503, "MUTATION_UNKNOWN", "Mutation outcome is unknown; reconcile before retry")
			}
			ghttp.WriteError(w, r, public)
			return
		}
		if result.Replayed {
			w.Header().Set("Idempotency-Replayed", "true")
		}
		response, err := ghttp.JSON(result.Response.Status, result.Response.Body)
		if err != nil {
			ghttp.WriteError(w, r, ghttp.ErrUnavailable)
			return
		}
		for name, value := range result.Response.Headers {
			response.Headers.Set(name, value)
		}
		// Network failure may have emitted a partial response; never append an
		// error document or automatically retry the committed mutation.
		_ = response.Write(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Add("Vary", "Accept, Authorization, Cookie")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !slices.Contains(methods, r.Method) {
			w.Header().Set("Allow", strings.Join(methods, ", "))
			ghttp.WriteError(w, r, mediaError(405, "METHOD_NOT_ALLOWED", "Method not allowed"))
			return
		}
		principal := auth.FromContext(r.Context())
		if !principal.Authenticated {
			ghttp.WriteError(w, r, auth.ErrUnauthenticated)
			return
		}
		if !principal.Active {
			ghttp.WriteError(w, r, auth.ErrPermissionDenied)
			return
		}
		handler.ServeHTTP(w, r)
	}), nil
}
