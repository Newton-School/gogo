package admin

import (
	"context"
	"errors"
	"net/http"

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
