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

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

type PasswordChanger interface {
	ChangeOwnPassword(context.Context, string, string) (auth.PasswordChangeResult, error)
}

type PasswordChangeConfig struct {
	Changer    PasswordChanger
	Limiter    ratelimit.Limiter
	RateSecret []byte
	// Zero values default to 10 account attempts and 60 IP attempts per minute.
	AccountLimit, IPLimit ratelimit.Limit
	CSRF                  security.CSRFConfig
	// PreserveSession is an explicit opt-in. Otherwise a confirmed change
	// clears the current login and redirects to LoginURL.
	PreserveSession             bool
	SuccessURL, LoginURL, Title string
	Render                      func(context.Context, PasswordChangePage) ([]byte, error)
	// OnFailure has the same redacted, panic-isolated contract as LoginConfig.
	OnFailure func(context.Context, string)
}

// PasswordChangePage deliberately cannot contain submitted passwords or raw
// validator/provider errors. Custom renderers are trusted HTML producers.
type PasswordChangePage struct{ Title, CSRFToken, Error string }

// PasswordChange serves an opt-in GET/HEAD form and CSRF-protected POST action.
// X-Gogo-Password-Change reports unchanged, changed or unknown on POST responses
// that reach the handler. It remains present if session persistence changes the
// HTTP response to 503. A changed/unknown error requires fresh authentication;
// clients must never assume rollback or automatically repeat the mutation.
func PasswordChange(config PasswordChangeConfig) (http.Handler, error) {
	if config.Changer == nil || config.Limiter == nil || len(config.RateSecret) < 32 {
		return nil, errors.New("auth views: password changer, limiter and rate secret required")
	}
	if config.AccountLimit == (ratelimit.Limit{}) {
		config.AccountLimit = ratelimit.Limit{Rate: 10, Burst: 10, Period: time.Minute}
	}
	if config.IPLimit == (ratelimit.Limit{}) {
		config.IPLimit = ratelimit.Limit{Rate: 60, Burst: 60, Period: time.Minute}
	}
	if config.AccountLimit.Validate(1) != nil || config.IPLimit.Validate(1) != nil {
		return nil, errors.New("auth views: invalid password change rate limit")
	}
	config.RateSecret = slices.Clone(config.RateSecret)
	if config.SuccessURL == "" {
		config.SuccessURL = "/"
	}
	if config.LoginURL == "" {
		config.LoginURL = "/login"
	}
	if security.SafeNext(config.SuccessURL, "") == "" || security.SafeNext(config.LoginURL, "") == "" {
		return nil, errors.New("auth views: local password change destinations required")
	}
	if config.Title == "" {
		config.Title = "Change password"
	}
	if config.Render == nil {
		config.Render = renderPasswordChange
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
		if r.Method == http.MethodPost {
			w.Header().Set("X-Gogo-Password-Change", string(auth.PasswordUnchanged))
		}
		if _, ok := sessions.FromContext(r.Context()); !ok {
			unavailable(w)
			return
		}
		current := auth.FromContext(r.Context())
		if !current.Authenticated || !current.Active || current.ID == "" || current.AuthVersion == 0 {
			if r.Method == http.MethodPost {
				http.Error(w, "authentication required", http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, config.LoginURL, http.StatusSeeOther)
			}
			return
		}
		page := PasswordChangePage{Title: config.Title, CSRFToken: security.CSRFToken(r)}
		if r.Method != http.MethodPost {
			servePasswordChange(w, r, config.Render, page, http.StatusOK)
			return
		}
		values, err := parseForm(w, r, csrf.MaxBodyBytes)
		if err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		for _, name := range []string{"old_password", "new_password1", "new_password2"} {
			if len(values[name]) != 1 {
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}
		}
		ip, err := netip.ParseAddr(security.ClientIP(r))
		if err != nil {
			unavailable(w)
			return
		}
		if !allowAccountAction(w, r, config.Limiter, config.RateSecret, config.OnFailure, "password_change", "ip", ip.Unmap().String(), config.IPLimit) ||
			!allowAccountAction(w, r, config.Limiter, config.RateSecret, config.OnFailure, "password_change", "account", current.ID, config.AccountLimit) {
			return
		}
		old, password, confirmation := values.Get("old_password"), values.Get("new_password1"), values.Get("new_password2")
		if old == "" || password == "" || len(old) > 4096 || len(password) > 4096 || len(confirmation) > 4096 || password != confirmation {
			page.Error = "Enter your current password and matching new passwords."
			servePasswordChange(w, r, config.Render, page, http.StatusBadRequest)
			return
		}
		result, changeErr := invokeCredentialMutation(func() (auth.PasswordChangeResult, error) {
			return config.Changer.ChangeOwnPassword(r.Context(), old, password)
		})
		// Unknown or contradictory provider responses never promote a session.
		if result.State != auth.PasswordChanged && result.State != auth.PasswordUnchanged && result.State != auth.PasswordChangeUnknown || result.State == auth.PasswordUnchanged && changeErr == nil {
			result.State = auth.PasswordChangeUnknown
			changeErr = errors.New("auth views: invalid password change result")
		}
		w.Header().Set("X-Gogo-Password-Change", string(result.State))
		if result.State == auth.PasswordChanged && changeErr == nil {
			if config.PreserveSession {
				if err := auth.RefreshLogin(w, r, result.Principal, csrf); err != nil {
					passwordChangeNeedsLogin(w, r, config, "session_refresh_failed")
					return
				}
				http.Redirect(w, r, config.SuccessURL, http.StatusSeeOther)
			} else {
				if err := auth.Logout(w, r, csrf); err != nil {
					passwordChangeNeedsLogin(w, r, config, "session_clear_failed")
					return
				}
				http.Redirect(w, r, config.LoginURL, http.StatusSeeOther)
			}
			return
		}
		if result.State != auth.PasswordUnchanged {
			passwordChangeNeedsLogin(w, r, config, "credential_outcome_requires_login")
			return
		}
		switch {
		case errors.Is(changeErr, auth.ErrCredentials):
			page.Error = "Unable to verify your current password."
			servePasswordChange(w, r, config.Render, page, http.StatusBadRequest)
		case errors.Is(changeErr, auth.ErrPasswordValidation):
			page.Error = "The new password does not satisfy the account password policy."
			servePasswordChange(w, r, config.Render, page, http.StatusBadRequest)
		case errors.Is(changeErr, auth.ErrUnauthenticated), errors.Is(changeErr, auth.ErrAccountChanged):
			if err := auth.Logout(w, r, csrf); err != nil {
				unavailable(w)
				return
			}
			http.Error(w, "authentication required", http.StatusUnauthorized)
		case errors.Is(changeErr, auth.ErrPermissionDenied):
			http.Error(w, "permission denied", http.StatusForbidden)
		default:
			failure(r.Context(), config.OnFailure, "unavailable")
			unavailable(w)
		}
	}))), nil
}

// A custom mutation backend may panic after committing. Never assume rollback,
// expose its panic value, or promote an unverified session in that situation.
func invokeCredentialMutation(action func() (auth.PasswordChangeResult, error)) (result auth.PasswordChangeResult, err error) {
	result.State = auth.PasswordChangeUnknown
	err = errors.New("auth views: credential mutation outcome unavailable")
	defer func() { _ = recover() }()
	return action()
}

func passwordChangeNeedsLogin(w http.ResponseWriter, r *http.Request, config PasswordChangeConfig, code string) {
	// A database change may already be committed. Even when clearing storage
	// fails, do not return an authenticated principal to later request code.
	_ = auth.Logout(w, r, config.CSRF)
	*r = *r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{}))
	failure(r.Context(), config.OnFailure, code)
	http.Error(w, "Password change outcome requires a fresh login. Do not repeat automatically.", http.StatusServiceUnavailable)
}

func servePasswordChange(w http.ResponseWriter, r *http.Request, render func(context.Context, PasswordChangePage) ([]byte, error), page PasswordChangePage, status int) {
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

var passwordChangeTemplate = template.Must(template.New("password-change").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title></head>
<body><main><h1>{{.Title}}</h1>{{if .Error}}<p role="alert" id="password_error">{{.Error}}</p>{{end}}
<form method="post"><input type="hidden" name="csrfmiddlewaretoken" value="{{.CSRFToken}}">
<p><label for="id_old_password">Current password</label><input id="id_old_password" name="old_password" type="password" autocomplete="current-password" required{{if .Error}} aria-describedby="password_error"{{end}}></p>
<p><label for="id_new_password1">New password</label><input id="id_new_password1" name="new_password1" type="password" autocomplete="new-password" required{{if .Error}} aria-describedby="password_error"{{end}}></p>
<p><label for="id_new_password2">Confirm new password</label><input id="id_new_password2" name="new_password2" type="password" autocomplete="new-password" required{{if .Error}} aria-describedby="password_error"{{end}}></p>
<button type="submit">Change password</button></form></main></body></html>`))

func renderPasswordChange(_ context.Context, page PasswordChangePage) ([]byte, error) {
	var out bytes.Buffer
	err := passwordChangeTemplate.Execute(&out, page)
	return out.Bytes(), err
}
