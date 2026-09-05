package views

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/netip"
	"slices"
	"time"

	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/security"
)

type PasswordResetRequester interface {
	Request(context.Context, string) error
}

type PasswordResetRequestConfig struct {
	Service             PasswordResetRequester
	NormalizeIdentifier func(string) (string, error)
	Limiter             ratelimit.Limiter
	RateSecret          []byte
	// Zero values default to 10 identity and 60 IP attempts per minute. Choose
	// deployment-specific quotas to bound mail delivery and account probing.
	IdentityLimit, IPLimit ratelimit.Limit
	CSRF                   security.CSRFConfig
	Title                  string
	Render                 func(context.Context, PasswordResetRequestPage) ([]byte, error)
	OnFailure              func(context.Context, string)
}

// A request page never exposes existence, delivery status, identity, recipient
// or tokens. The same Submitted acknowledgement covers all service outcomes.
type PasswordResetRequestPage struct {
	Title, CSRFToken, Error string
	Submitted               bool
}

// PasswordResetRequest is an opt-in anonymous GET/HEAD form and protected POST
// route. It never creates a public signup route or assumes an identifier is an
// email. Mount behind trusted host/proxy headers. CSRF applies even without an
// authenticated session; the cookie carries no authentication authority.
func PasswordResetRequest(config PasswordResetRequestConfig) (http.Handler, error) {
	if config.Service == nil || config.NormalizeIdentifier == nil || config.Limiter == nil || len(config.RateSecret) < 32 {
		return nil, errors.New("auth views: reset service, normalizer, limiter and rate secret required")
	}
	if config.IdentityLimit == (ratelimit.Limit{}) {
		config.IdentityLimit = ratelimit.Limit{Rate: 10, Burst: 10, Period: time.Minute}
	}
	if config.IPLimit == (ratelimit.Limit{}) {
		config.IPLimit = ratelimit.Limit{Rate: 60, Burst: 60, Period: time.Minute}
	}
	if config.IdentityLimit.Validate(1) != nil || config.IPLimit.Validate(1) != nil {
		return nil, errors.New("auth views: invalid reset rate limit")
	}
	config.RateSecret = slices.Clone(config.RateSecret)
	if config.Title == "" {
		config.Title = "Reset password"
	}
	if config.Render == nil {
		config.Render = renderPasswordResetRequest
	}
	csrf, err := accountCSRF(config.CSRF)
	if err != nil {
		return nil, err
	}
	protect, err := security.CSRF(csrf)
	if err != nil {
		return nil, err
	}
	return private(protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, HEAD, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		page := PasswordResetRequestPage{Title: config.Title, CSRFToken: security.CSRFToken(r)}
		if r.Method != http.MethodPost {
			servePasswordResetRequest(w, r, config.Render, page, http.StatusOK)
			return
		}
		values, err := parseForm(w, r, csrf.MaxBodyBytes)
		if err != nil || len(values["identifier"]) != 1 {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		identifier := values.Get("identifier")
		if len(identifier) > 512 {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		ip, err := netip.ParseAddr(security.ClientIP(r))
		if err != nil {
			unavailable(w)
			return
		}
		if !allowAccountAction(w, r, config.Limiter, config.RateSecret, config.OnFailure, "password_reset", "ip", ip.Unmap().String(), config.IPLimit) {
			return
		}
		normalized, normalizeErr := normalizeAccountIdentifier(config.NormalizeIdentifier, identifier)
		if normalizeErr != nil || normalized == "" || len(normalized) > 512 {
			normalized = ""
			failure(r.Context(), config.OnFailure, "identity_normalization_failed")
		}
		if !allowAccountAction(w, r, config.Limiter, config.RateSecret, config.OnFailure, "password_reset", "identity", normalized, config.IdentityLimit) {
			return
		}
		// Pass raw identity to avoid applying custom normalization twice. The
		// service owns comparable ineligible timing and commit/delivery policy.
		if !invokeResetRequest(r.Context(), config.Service, identifier) {
			failure(r.Context(), config.OnFailure, "reset_request_unavailable")
		}
		page.Submitted = true
		servePasswordResetRequest(w, r, config.Render, page, http.StatusOK)
	}))), nil
}

func invokeResetRequest(ctx context.Context, service PasswordResetRequester, identifier string) (ok bool) {
	defer func() { _ = recover() }()
	return service.Request(ctx, identifier) == nil
}

func servePasswordResetRequest(w http.ResponseWriter, r *http.Request, render func(context.Context, PasswordResetRequestPage) ([]byte, error), page PasswordResetRequestPage, status int) {
	body, err := renderAccountPage(r.Context(), render, page)
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

var passwordResetRequestTemplate = template.Must(template.New("password-reset-request").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title></head><body><main><h1>{{.Title}}</h1>
{{if .Submitted}}<p role="status">If an eligible account exists, password reset instructions will be sent to its verified address.</p>{{else}}
<p>Enter your account identifier to request password reset instructions.</p>{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}
<form method="post"><input type="hidden" name="csrfmiddlewaretoken" value="{{.CSRFToken}}"><p><label for="id_identifier">Identifier</label><input id="id_identifier" name="identifier" autocomplete="username" required maxlength="255"></p><button type="submit">Request password reset</button></form>{{end}}</main></body></html>`))

func renderPasswordResetRequest(_ context.Context, page PasswordResetRequestPage) ([]byte, error) {
	var out bytes.Buffer
	err := passwordResetRequestTemplate.Execute(&out, page)
	return out.Bytes(), err
}
