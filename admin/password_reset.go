package admin

import (
	"context"
	"errors"
	"net/http"

	authviews "github.com/Newton-School/gogo/core/auth/views"
	"github.com/Newton-School/gogo/core/templates"
)

// PasswordResetRequestHandler is an explicitly mounted anonymous recovery
// route. It adds only Admin presentation: Core owns CSRF, bounded throttling,
// indistinguishable acknowledgement and the verified-recipient service port.
// Unlike Admin model views it must remain reachable without a staff session.
func (s *Site) PasswordResetRequestHandler(config authviews.PasswordResetRequestConfig) (http.Handler, error) {
	csrf, err := s.accountCSRF(config.CSRF)
	if err != nil {
		return nil, err
	}
	config.CSRF = csrf
	if config.Render == nil {
		config.Render = s.renderPasswordResetRequest
	}
	return authviews.PasswordResetRequest(config)
}

// PasswordResetConfirmHandler adds escaped Admin presentation without changing
// Core's token proof, credential/session outcomes or no-automatic-login policy.
// The application separately mounts authviews.PasswordResetConfirmScript at
// config.ScriptURL (or the default content-hashed path). Reset bearer values
// enter only the POST form, never a query/path, notices, logs or ordinary JSON.
func (s *Site) PasswordResetConfirmHandler(config authviews.PasswordResetConfirmConfig) (http.Handler, error) {
	csrf, err := s.accountCSRF(config.CSRF)
	if err != nil {
		return nil, err
	}
	config.CSRF = csrf
	if config.SuccessURL == "" {
		config.SuccessURL = s.config.LoginURL
	}
	if config.SuccessURL == "" {
		return nil, errors.New("admin: password reset confirmation requires a login/completion URL")
	}
	if config.Render == nil {
		config.Render = s.renderPasswordResetConfirm
	}
	return authviews.PasswordResetConfirm(config)
}

func (s *Site) recoveryContext(title, csrfToken, problem string) templates.Context {
	return templates.Context{
		"title": title, "header": s.config.Header, "site_title": s.config.Title, "prefix": s.config.Prefix,
		"css_url": s.config.Prefix + "assets/admin." + s.cssVersion + ".css", "login_url": s.config.LoginURL,
		"csrf_token": csrfToken, "error": problem,
	}
}

func (s *Site) renderPasswordResetRequest(ctx context.Context, page authviews.PasswordResetRequestPage) ([]byte, error) {
	values := s.recoveryContext(page.Title, page.CSRFToken, page.Error)
	values["submitted"] = page.Submitted
	body, err := s.engine.Render(ctx, "password_reset_request.html", values)
	return []byte(body), err
}

func (s *Site) renderPasswordResetConfirm(ctx context.Context, page authviews.PasswordResetConfirmPage) ([]byte, error) {
	values := s.recoveryContext(page.Title, page.CSRFToken, page.Error)
	// FormValue is the explicit secret-bearing boundary. Engine output escapes
	// ordinary strings; do not convert this token to template.HTML or serialize
	// the context for logging/telemetry. No submitted passwords reach this DTO.
	values["reset_token"] = page.Token.FormValue()
	values["script_url"] = page.ScriptURL
	body, err := s.engine.Render(ctx, "password_reset_confirm.html", values)
	return []byte(body), err
}
