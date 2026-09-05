package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/Newton-School/gogo/core/auth"
	authviews "github.com/Newton-School/gogo/core/auth/views"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/templates"
)

type staffAuthenticator struct {
	backend authviews.CredentialAuthenticator
}

func (a staffAuthenticator) Authenticate(ctx context.Context, identifier, password string) (auth.Principal, error) {
	principal, err := a.backend.Authenticate(ctx, identifier, password)
	if err != nil {
		return auth.Principal{}, err
	}
	if !principal.Authenticated || !principal.Active || !principal.Staff {
		return auth.Principal{}, auth.ErrCredentials
	}
	return principal, nil
}

// LoginHandler reuses the Core credential workflow and accepts only active staff
// before a session is created. Staff status never grants model permissions.
// The application mounts the handler explicitly inside sessions.Middleware and
// auth.SessionMiddleware and supplies the backend and atomic rate-limit ports.
// CSRF security settings come from this Site; config.CSRF.MaxBodyBytes may set
// the smaller credential-body bound. A custom Render callback remains supported.
func (s *Site) LoginHandler(config authviews.LoginConfig) (http.Handler, error) {
	if config.Authenticator == nil {
		return nil, errors.New("admin: credential authenticator required")
	}
	csrf, err := s.accountCSRF(config.CSRF)
	if err != nil {
		return nil, err
	}
	config.CSRF = csrf
	config.Authenticator = staffAuthenticator{backend: config.Authenticator}
	if config.Title == "" {
		config.Title = "Sign in"
	}
	if config.SuccessURL == "" {
		config.SuccessURL = s.config.Prefix
	}
	if config.Render == nil {
		config.Render = s.renderLogin
	}
	return authviews.Login(config)
}

// LogoutHandler delegates to Core's POST-only, CSRF-protected session logout.
// It does not register the Config.LogoutURL route on the application's router.
func (s *Site) LogoutHandler(config authviews.LogoutConfig) (http.Handler, error) {
	csrf, err := s.accountCSRF(config.CSRF)
	if err != nil {
		return nil, err
	}
	config.CSRF = csrf
	if config.SuccessURL == "" {
		config.SuccessURL = s.config.LoginURL
		if config.SuccessURL == "" {
			config.SuccessURL = s.config.Prefix
		}
	}
	return authviews.Logout(config)
}

func (s *Site) accountCSRF(requested security.CSRFConfig) (security.CSRFConfig, error) {
	if requested.Exempt != nil {
		return security.CSRFConfig{}, errors.New("admin: account actions cannot exempt CSRF")
	}
	config := s.config.CSRF
	config.MaxBodyBytes = requested.MaxBodyBytes
	return config, nil
}

func (s *Site) renderLogin(ctx context.Context, page authviews.LoginPage) ([]byte, error) {
	// Never call navigation or query a model store on the anonymous page. Core
	// supplies no password or private provider error to this renderer.
	body, err := s.engine.Render(ctx, "login.html", templates.Context{
		"title": page.Title, "header": s.config.Header, "site_title": s.config.Title,
		"prefix": s.config.Prefix, "css_url": s.config.Prefix + "assets/admin." + s.cssVersion + ".css",
		"identifier": page.Identifier, "next": page.Next, "csrf_token": page.CSRFToken, "error": page.Error,
	})
	return []byte(body), err
}

// PasswordChangeHandler provides the Admin presentation and staff boundary for
// Core's self-service credential flow. It grants no authority over other users.
// The application mounts Config.PasswordChangeURL explicitly inside sessions
// and auth.SessionMiddleware. PreserveSession remains an explicit caller policy.
func (s *Site) PasswordChangeHandler(config authviews.PasswordChangeConfig) (http.Handler, error) {
	csrf, err := s.accountCSRF(config.CSRF)
	if err != nil {
		return nil, err
	}
	config.CSRF = csrf
	if config.SuccessURL == "" {
		config.SuccessURL = s.config.Prefix
	}
	if config.LoginURL == "" {
		config.LoginURL = s.config.LoginURL
	}
	if config.LoginURL == "" {
		return nil, errors.New("admin: password change requires a login URL")
	}
	if config.Render == nil {
		config.Render = s.renderPasswordChange
	}
	handler, err := authviews.PasswordChange(config)
	if err != nil {
		return nil, err
	}
	loginURL := config.LoginURL
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Add("Vary", "Cookie")
		p := auth.FromContext(r.Context())
		if !p.Authenticated {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				login, _ := url.Parse(loginURL)
				query := login.Query()
				query.Set("next", r.URL.RequestURI())
				login.RawQuery = query.Encode()
				http.Redirect(w, r, login.String(), http.StatusSeeOther)
			} else {
				http.Error(w, "Authentication required", http.StatusUnauthorized)
			}
			return
		}
		if !p.Active || !p.Staff {
			http.Error(w, "Permission denied", http.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	}), nil
}

func (s *Site) renderPasswordChange(ctx context.Context, page authviews.PasswordChangePage) ([]byte, error) {
	// This self-service page does not query model stores or expose navigation
	// metadata. Core's page contract contains no old/new password values.
	body, err := s.engine.Render(ctx, "password_change.html", templates.Context{
		"title": page.Title, "header": s.config.Header, "site_title": s.config.Title, "prefix": s.config.Prefix,
		"css_url": s.config.Prefix + "assets/admin." + s.cssVersion + ".css", "actor": auth.FromContext(ctx).ID,
		"csrf_token": page.CSRFToken, "error": page.Error, "logout_url": s.config.LogoutURL,
	})
	return []byte(body), err
}
