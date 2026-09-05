// Package views supplies opt-in, CSRF-protected account HTTP workflows. Mount
// these handlers inside sessions.Middleware and auth.SessionMiddleware, with
// security.Headers outside both. It never registers URLs or opens providers.
package views

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html/template"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

type CredentialAuthenticator interface {
	Authenticate(context.Context, string, string) (auth.Principal, error)
}

type LoginConfig struct {
	Authenticator CredentialAuthenticator
	// NormalizeIdentifier must match the backend's identity equivalence rules.
	// Use Accounts.NormalizeLoginIdentifier for the built-in account service.
	NormalizeIdentifier func(string) (string, error)
	Limiter             ratelimit.Limiter
	// RateSecret is a purpose-specific, at least 32-byte deployment secret. Rate
	// bucket keys contain HMAC digests, never submitted identifiers or IPs.
	RateSecret []byte
	// Zero values default to 10 identity attempts and 60 IP attempts per minute.
	IdentityLimit, IPLimit ratelimit.Limit
	CSRF                   security.CSRFConfig
	SuccessURL, Title      string
	// Render replaces the escaped built-in page. It returns HTML bytes so the
	// handler can finish rendering before writing response headers. Passwords
	// and raw backend errors are never supplied to renderers.
	Render func(context.Context, LoginPage) ([]byte, error)
	// OnFailure receives only a safe outcome code, never credentials, submitted
	// identity, IP, or backend errors. It must be fast and non-blocking. Panics
	// are isolated so observability cannot change the authentication outcome.
	OnFailure func(context.Context, string)
}

type LoginPage struct {
	Title, Identifier, Next, CSRFToken, Error string
}

// Login returns a GET/HEAD form and POST credential handler. Failed credentials
// return 401 without distinguishing unknown, inactive or unusable accounts.
func Login(config LoginConfig) (http.Handler, error) {
	if config.Authenticator == nil || config.NormalizeIdentifier == nil || config.Limiter == nil || len(config.RateSecret) < 32 {
		return nil, errors.New("auth views: authenticator, identity normalizer, limiter and rate secret required")
	}
	if config.IdentityLimit == (ratelimit.Limit{}) {
		config.IdentityLimit = ratelimit.Limit{Rate: 10, Burst: 10, Period: time.Minute}
	}
	if config.IPLimit == (ratelimit.Limit{}) {
		config.IPLimit = ratelimit.Limit{Rate: 60, Burst: 60, Period: time.Minute}
	}
	if config.IdentityLimit.Validate(1) != nil || config.IPLimit.Validate(1) != nil {
		return nil, errors.New("auth views: invalid login rate limit")
	}
	config.RateSecret = slices.Clone(config.RateSecret)
	if config.SuccessURL == "" {
		config.SuccessURL = "/"
	}
	if security.SafeNext(config.SuccessURL, "") == "" {
		return nil, errors.New("auth views: local success URL required")
	}
	if config.Title == "" {
		config.Title = "Sign in"
	}
	if config.Render == nil {
		config.Render = renderLogin
	}
	csrf, err := accountCSRF(config.CSRF)
	if err != nil {
		return nil, err
	}
	config.CSRF = csrf
	protect, err := security.CSRF(csrf)
	if err != nil {
		return nil, err
	}
	return private(protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, HEAD, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, ok := sessions.FromContext(r.Context()); !ok {
			unavailable(w)
			return
		}
		page := LoginPage{Title: config.Title, Next: security.SafeNext(r.URL.Query().Get("next"), config.SuccessURL), CSRFToken: security.CSRFToken(r)}
		if r.Method != http.MethodPost {
			serveLogin(w, r, config.Render, page, http.StatusOK)
			return
		}
		values, err := parseForm(w, r, csrf.MaxBodyBytes)
		if err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		for _, name := range []string{"identifier", "password", "next"} {
			if len(values[name]) > 1 {
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}
		}
		page.Next = security.SafeNext(values.Get("next"), config.SuccessURL)
		identifier, password := values.Get("identifier"), values.Get("password")
		ip, err := netip.ParseAddr(security.ClientIP(r))
		if err != nil {
			unavailable(w)
			return
		}
		if !allowLogin(w, r, config, "ip", ip.Unmap().String(), config.IPLimit) {
			return
		}
		// Apply byte bounds before custom normalization and credential work. Do
		// not trim passwords: whitespace is a valid part of a credential.
		if len(identifier) > 512 || len(password) > 4096 || identifier == "" || password == "" {
			page.Error = "Enter a valid identifier and password."
			serveLogin(w, r, config.Render, page, http.StatusBadRequest)
			return
		}
		normalized, normalizeErr := config.NormalizeIdentifier(identifier)
		if normalizeErr != nil || normalized == "" || len(normalized) > 512 {
			normalized = ""
		}
		if !allowLogin(w, r, config, "identity", normalized, config.IdentityLimit) {
			return
		}
		// The backend applies its normalizer once. Passing the normalized value
		// here would apply a custom normalizer twice and change its identity.
		principal, err := config.Authenticator.Authenticate(r.Context(), identifier, password)
		if err != nil && !errors.Is(err, auth.ErrCredentials) && !errors.Is(err, auth.ErrUnauthenticated) && !errors.Is(err, auth.ErrPermissionDenied) && !errors.Is(err, auth.ErrAccountChanged) {
			failure(r.Context(), config.OnFailure, "unavailable")
			unavailable(w)
			return
		}
		if err != nil || normalized == "" || !principal.Authenticated || !principal.Active || principal.ID == "" || principal.AuthVersion == 0 {
			failure(r.Context(), config.OnFailure, "invalid_credentials")
			page.Identifier = identifier
			page.Error = "Unable to sign in with these credentials."
			serveLogin(w, r, config.Render, page, http.StatusUnauthorized)
			return
		}
		if err := auth.Login(w, r, principal, csrf); err != nil {
			failure(r.Context(), config.OnFailure, "unavailable")
			unavailable(w)
			return
		}
		http.Redirect(w, r, page.Next, http.StatusSeeOther)
	}))), nil
}

func allowLogin(w http.ResponseWriter, r *http.Request, config LoginConfig, kind, value string, limit ratelimit.Limit) bool {
	mac := hmac.New(sha256.New, config.RateSecret)
	_, _ = mac.Write([]byte("gogo.auth.login." + kind + "\x00" + value))
	decision, err := config.Limiter.Allow(r.Context(), "auth:login:"+kind+":"+hex.EncodeToString(mac.Sum(nil)), limit, 1)
	if err != nil {
		failure(r.Context(), config.OnFailure, "unavailable")
		unavailable(w)
		return false
	}
	if !decision.Allowed {
		seconds := int64(decision.RetryAfter / time.Second)
		if decision.RetryAfter%time.Second > 0 {
			seconds++
		}
		w.Header().Set("Retry-After", strconv.FormatInt(max(int64(1), seconds), 10))
		failure(r.Context(), config.OnFailure, "rate_limited")
		http.Error(w, "Too many sign-in attempts. Try again later.", http.StatusTooManyRequests)
		return false
	}
	return true
}

func accountCSRF(config security.CSRFConfig) (security.CSRFConfig, error) {
	if config.Exempt != nil {
		return config, errors.New("auth views: account actions cannot exempt CSRF")
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = 32 << 10
	}
	if config.MaxBodyBytes < 1 || config.MaxBodyBytes > 64<<10 {
		return config, errors.New("auth views: account form limit must be between 1 and 65536 bytes")
	}
	config.TrustedOrigins = slices.Clone(config.TrustedOrigins)
	return config, nil
}

func parseForm(w http.ResponseWriter, r *http.Request, bound int64) (url.Values, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	r.Body = http.MaxBytesReader(w, r.Body, bound)
	switch mediaType {
	case "application/x-www-form-urlencoded":
		err = r.ParseForm()
	case "multipart/form-data":
		err = r.ParseMultipartForm(bound)
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
			if len(r.MultipartForm.File) > 0 {
				return nil, errors.New("auth views: credential form cannot contain files")
			}
		}
	default:
		err = errors.New("auth views: encoded form required")
	}
	return r.PostForm, err
}

func private(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func unavailable(w http.ResponseWriter) {
	http.Error(w, "authentication service unavailable", http.StatusServiceUnavailable)
}

func failure(ctx context.Context, callback func(context.Context, string), code string) {
	if callback != nil {
		defer func() { _ = recover() }()
		callback(ctx, code)
	}
}

func serveLogin(w http.ResponseWriter, r *http.Request, render func(context.Context, LoginPage) ([]byte, error), page LoginPage, status int) {
	body, err := render(r.Context(), page)
	if err != nil || len(body) > 1<<20 {
		unavailable(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

var loginTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title></head>
<body><main><h1>{{.Title}}</h1>{{if .Error}}<p role="alert" id="login_error">{{.Error}}</p>{{end}}
<form method="post"><input type="hidden" name="csrfmiddlewaretoken" value="{{.CSRFToken}}"><input type="hidden" name="next" value="{{.Next}}">
<p><label for="id_identifier">Identifier</label><input id="id_identifier" name="identifier" autocomplete="username" maxlength="255" required value="{{.Identifier}}"{{if .Error}} aria-describedby="login_error"{{end}}></p>
<p><label for="id_password">Password</label><input id="id_password" name="password" type="password" autocomplete="current-password" required{{if .Error}} aria-describedby="login_error"{{end}}></p>
<button type="submit">Sign in</button></form></main></body></html>`))

func renderLogin(_ context.Context, page LoginPage) ([]byte, error) {
	var out bytes.Buffer
	err := loginTemplate.Execute(&out, page)
	return out.Bytes(), err
}
