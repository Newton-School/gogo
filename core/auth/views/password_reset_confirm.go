package views

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

type PasswordResetConfirmer interface {
	Confirm(context.Context, string, string) (auth.PasswordChangeResult, error)
}

type PasswordResetConfirmConfig struct {
	Service             PasswordResetConfirmer
	Limiter             ratelimit.Limiter
	RateSecret          []byte
	TokenLimit, IPLimit ratelimit.Limit
	CSRF                security.CSRFConfig
	SuccessURL, Title   string
	// ScriptURL must mount PasswordResetConfirmScript on this origin. Empty
	// selects PasswordResetConfirmScriptPath. Imports never register a route.
	ScriptURL string
	Render    func(context.Context, PasswordResetConfirmPage) ([]byte, error)
	OnFailure func(context.Context, string)
}

// ResetFormToken intentionally denies routine serialization/formatting. Only
// FormValue explicitly reveals it for an escaped form field. Never log it,
// serialize it into normal task JSON, or use it in a URL query/path.
type ResetFormToken struct{ value string }

func (ResetFormToken) String() string                   { return "auth.ResetFormToken{redacted}" }
func (t ResetFormToken) Format(state fmt.State, _ rune) { _, _ = fmt.Fprint(state, t.String()) }
func (ResetFormToken) MarshalJSON() ([]byte, error) {
	return nil, errors.New("auth views: reset token cannot be serialized")
}
func (t ResetFormToken) FormValue() string { return t.value }

type PasswordResetConfirmPage struct {
	Title, CSRFToken, Error, ScriptURL string
	Token                              ResetFormToken
}

var resetFormTokenPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}\.[A-Za-z0-9_-]{43}$`)

// The browser moves the secret from a fragment (never sent in an HTTP request)
// into the POST form and removes it from history. It uses neither eval nor
// innerHTML. Without JavaScript the visible token field supports manual paste.
const resetConfirmScript = `"use strict";(function(){var input=document.getElementById("id_reset_token");if(!input)return;var row=document.getElementById("reset-token-row");var value=input.value;if(window.location.hash){try{value=decodeURIComponent(window.location.hash.slice(1));}catch(e){value="";}window.history.replaceState(null,document.title,window.location.pathname+window.location.search);}var valid=/^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}\.[A-Za-z0-9_-]{43}$/.test(value);input.value=valid?value:"";if(row)row.hidden=valid;})();`

func PasswordResetConfirmScriptPath() string {
	digest := sha256.Sum256([]byte(resetConfirmScript))
	return "/_gogo/auth/reset-confirm." + hex.EncodeToString(digest[:8]) + ".js"
}

// PasswordResetConfirmScript serves only public immutable code; no request or
// secret is reflected in the body. Mount at the matching content-hashed path.
func PasswordResetConfirmScript() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(resetConfirmScript))
		}
	})
}

// PasswordResetConfirm consumes only POSTed tokens, never query/path tokens.
// PasswordReset.Request links use URL fragments and the explicitly mounted
// script above. Confirmation never logs in: it clears any current session and
// redirects to a configured local login/completion page after confirmed success.
// As with password change, the outcome header remains truthful if later session
// persistence fails. Unknown/committed failure must not be retried automatically.
func PasswordResetConfirm(config PasswordResetConfirmConfig) (http.Handler, error) {
	if config.Service == nil || config.Limiter == nil || len(config.RateSecret) < 32 {
		return nil, errors.New("auth views: reset confirmer, limiter and rate secret required")
	}
	if config.TokenLimit == (ratelimit.Limit{}) {
		config.TokenLimit = ratelimit.Limit{Rate: 10, Burst: 10, Period: time.Minute}
	}
	if config.IPLimit == (ratelimit.Limit{}) {
		config.IPLimit = ratelimit.Limit{Rate: 60, Burst: 60, Period: time.Minute}
	}
	if config.TokenLimit.Validate(1) != nil || config.IPLimit.Validate(1) != nil {
		return nil, errors.New("auth views: invalid reset confirmation rate limit")
	}
	config.RateSecret = slices.Clone(config.RateSecret)
	if config.SuccessURL == "" {
		config.SuccessURL = "/login"
	}
	if config.Title == "" {
		config.Title = "Choose a new password"
	}
	if config.ScriptURL == "" {
		config.ScriptURL = PasswordResetConfirmScriptPath()
	}
	if security.SafeNext(config.SuccessURL, "") == "" || security.SafeNext(config.ScriptURL, "") == "" || strings.ContainsAny(config.ScriptURL, "?#") {
		return nil, errors.New("auth views: local reset destinations and script path required")
	}
	if config.Render == nil {
		config.Render = renderPasswordResetConfirm
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
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, HEAD, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		page := PasswordResetConfirmPage{Title: config.Title, CSRFToken: security.CSRFToken(r), ScriptURL: config.ScriptURL}
		if r.Method != http.MethodPost {
			servePasswordResetConfirm(w, r, config.Render, page, http.StatusOK)
			return
		}
		w.Header().Set("X-Gogo-Password-Change", string(auth.PasswordUnchanged))
		values, err := parseForm(w, r, csrf.MaxBodyBytes)
		if err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		for _, field := range []string{"token", "new_password1", "new_password2"} {
			if len(values[field]) != 1 {
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}
		}
		ip, err := netip.ParseAddr(security.ClientIP(r))
		if err != nil {
			unavailable(w)
			return
		}
		if !allowAccountAction(w, r, config.Limiter, config.RateSecret, config.OnFailure, "password_reset_confirm", "ip", ip.Unmap().String(), config.IPLimit) {
			return
		}
		token := values.Get("token")
		if !resetFormTokenPattern.MatchString(token) {
			page.Error = "This password reset link is invalid or expired."
			servePasswordResetConfirm(w, r, config.Render, page, http.StatusBadRequest)
			return
		}
		if !allowAccountAction(w, r, config.Limiter, config.RateSecret, config.OnFailure, "password_reset_confirm", "token", token[:36], config.TokenLimit) {
			return
		}
		page.Token = ResetFormToken{value: token}
		password, confirmation := values.Get("new_password1"), values.Get("new_password2")
		if password == "" || len(password) > 4096 || len(confirmation) > 4096 || password != confirmation {
			page.Error = "Enter matching new passwords."
			servePasswordResetConfirm(w, r, config.Render, page, http.StatusBadRequest)
			return
		}
		result, confirmErr := invokeCredentialMutation(func() (auth.PasswordChangeResult, error) {
			return config.Service.Confirm(r.Context(), token, password)
		})
		if result.State != auth.PasswordChanged && result.State != auth.PasswordUnchanged && result.State != auth.PasswordChangeUnknown || result.State == auth.PasswordUnchanged && confirmErr == nil {
			result.State = auth.PasswordChangeUnknown
			confirmErr = errors.New("auth views: invalid reset confirmation result")
		}
		w.Header().Set("X-Gogo-Password-Change", string(result.State))
		if result.State != auth.PasswordUnchanged {
			clearErr := clearResetSession(w, r, csrf)
			if result.State == auth.PasswordChanged && confirmErr == nil && clearErr == nil {
				http.Redirect(w, r, config.SuccessURL, http.StatusSeeOther)
				return
			}
			failure(r.Context(), config.OnFailure, "reset_outcome_requires_login")
			http.Error(w, "Password reset outcome requires a fresh login. Do not repeat automatically.", http.StatusServiceUnavailable)
			return
		}
		switch {
		case errors.Is(confirmErr, auth.ErrResetToken), errors.Is(confirmErr, auth.ErrAccountChanged), errors.Is(confirmErr, auth.ErrUnauthenticated):
			page.Token = ResetFormToken{}
			page.Error = "This password reset link is invalid or expired."
		case errors.Is(confirmErr, auth.ErrPasswordValidation):
			page.Error = "The new password does not satisfy the account password policy."
		case errors.Is(confirmErr, auth.ErrPermissionDenied):
			page.Token = ResetFormToken{}
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		default:
			failure(r.Context(), config.OnFailure, "unavailable")
			unavailable(w)
			return
		}
		servePasswordResetConfirm(w, r, config.Render, page, http.StatusBadRequest)
	}))), nil
}

func clearResetSession(w http.ResponseWriter, r *http.Request, csrf security.CSRFConfig) error {
	var err error
	if _, ok := sessions.FromContext(r.Context()); ok {
		err = auth.Logout(w, r, csrf)
	} else {
		err = security.RotateCSRFRequest(w, r, csrf)
	}
	*r = *r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{}))
	return err
}

func servePasswordResetConfirm(w http.ResponseWriter, r *http.Request, render func(context.Context, PasswordResetConfirmPage) ([]byte, error), page PasswordResetConfirmPage, status int) {
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

var passwordResetConfirmTemplate = template.Must(template.New("password-reset-confirm").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title><script src="{{.ScriptURL}}" defer></script></head><body><main><h1>{{.Title}}</h1>
{{if .Error}}<p role="alert" id="reset_error">{{.Error}}</p>{{end}}<form method="post"><input type="hidden" name="csrfmiddlewaretoken" value="{{.CSRFToken}}">
<p id="reset-token-row"><label for="id_reset_token">Reset token</label><input id="id_reset_token" name="token" type="password" autocomplete="off" maxlength="80" required value="{{.Token.FormValue}}"><small>Open the complete email link or paste its token (the part after #).</small></p>
<p><label for="id_new_password1">New password</label><input id="id_new_password1" name="new_password1" type="password" autocomplete="new-password" required{{if .Error}} aria-describedby="reset_error"{{end}}></p>
<p><label for="id_new_password2">Confirm new password</label><input id="id_new_password2" name="new_password2" type="password" autocomplete="new-password" required{{if .Error}} aria-describedby="reset_error"{{end}}></p>
<button type="submit">Reset password</button></form></main></body></html>`))

func renderPasswordResetConfirm(_ context.Context, page PasswordResetConfirmPage) ([]byte, error) {
	var out bytes.Buffer
	err := passwordResetConfirmTemplate.Execute(&out, page)
	return out.Bytes(), err
}
