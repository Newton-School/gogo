package views

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/sessions"
)

type resetConfirmFunc func(context.Context, string, string) (auth.PasswordChangeResult, error)

func (f resetConfirmFunc) Confirm(ctx context.Context, token, password string) (auth.PasswordChangeResult, error) {
	return f(ctx, token, password)
}

const fixtureResetToken = "12345678-1234-1234-1234-123456789abc.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func resetConfirmConfig() PasswordResetConfirmConfig {
	l := loginConfig()
	return PasswordResetConfirmConfig{Limiter: l.Limiter, RateSecret: l.RateSecret, SuccessURL: "/login", Service: resetConfirmFunc(func(context.Context, string, string) (auth.PasswordChangeResult, error) {
		return auth.PasswordChangeResult{State: auth.PasswordChanged}, nil
	})}
}
func newResetConfirmFixture(t *testing.T, config PasswordResetConfirmConfig) *loginFixture {
	t.Helper()
	handler, err := PasswordResetConfirm(config)
	if err != nil {
		t.Fatal(err)
	}
	f := &loginFixture{t: t, handler: handler, session: sessions.New(sessions.Record{}), cookies: map[string]*http.Cookie{}}
	if w := f.call("GET", "/reset/confirm", nil, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	return f
}
func resetConfirmValues() url.Values {
	return url.Values{"token": {fixtureResetToken}, "new_password1": {"  new credential  "}, "new_password2": {"  new credential  "}}
}

func TestResetConfirmationCSRFParsingAndPasswordValidation(t *testing.T) {
	config := resetConfirmConfig()
	calls := 0
	config.Service = resetConfirmFunc(func(_ context.Context, token, password string) (auth.PasswordChangeResult, error) {
		calls++
		if token != fixtureResetToken || password != "  new credential  " {
			t.Fatal("reset inputs changed")
		}
		return auth.PasswordChangeResult{State: auth.PasswordUnchanged}, auth.ErrPasswordValidation
	})
	f := newResetConfirmFixture(t, config)
	if w := f.call("POST", "/reset/confirm", resetConfirmValues(), ""); w.Code != 403 || calls != 0 {
		t.Fatal(w.Code, calls)
	}
	for _, mode := range []string{"duplicate", "mismatch", "long", "invalid-token"} {
		values := resetConfirmValues()
		switch mode {
		case "duplicate":
			values.Add("token", fixtureResetToken)
		case "mismatch":
			values.Set("new_password2", "different")
		case "long":
			values.Set("new_password1", strings.Repeat("x", 4097))
		case "invalid-token":
			values.Set("token", "<script>private malformed token</script>")
		}
		w := f.call("POST", "/reset/confirm", values, f.token)
		if w.Code != 400 || calls != 0 || strings.Contains(w.Body.String(), "<script>private") {
			t.Fatal(mode, w.Code, calls)
		}
	}
	w := f.call("POST", "/reset/confirm", resetConfirmValues(), f.token)
	if w.Code != 400 || calls != 1 || strings.Contains(w.Body.String(), "new credential") || !strings.Contains(w.Body.String(), fixtureResetToken) || w.Header().Get("X-Gogo-Password-Change") != "unchanged" {
		t.Fatal(w.Code, calls, w.Body.String())
	}
	// Token is preserved only in the explicit escaped form field for retry;
	// GET query values are never consumed/reflected and cannot trigger a reset.
	w = f.call("GET", "/reset/confirm?token="+fixtureResetToken, nil, "")
	if w.Code != 200 || calls != 1 || strings.Contains(w.Body.String(), fixtureResetToken) {
		t.Fatal("query token consumed or reflected")
	}
	if w := f.call("HEAD", "/reset/confirm", nil, ""); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal(w.Code)
	}
}

func TestResetConfirmationOutcomeNeverAuthenticates(t *testing.T) {
	for _, test := range []struct {
		name   string
		state  auth.PasswordChangeState
		err    error
		status int
		header string
		retain bool
	}{
		{"success", auth.PasswordChanged, nil, 303, "changed", false},
		{"invalid", auth.PasswordUnchanged, auth.ErrResetToken, 400, "unchanged", true},
		{"denied", auth.PasswordUnchanged, auth.ErrPermissionDenied, 403, "unchanged", true},
		{"provider", auth.PasswordUnchanged, errors.New("private provider failure"), 503, "unchanged", true},
		{"committed-error", auth.PasswordChanged, errors.New("private after-commit failure"), 503, "changed", false},
		{"uncertain", auth.PasswordChangeUnknown, errors.New("private unknown commit"), 503, "unknown", false},
		{"contradictory", auth.PasswordUnchanged, nil, 503, "unknown", false},
		{"panic", auth.PasswordChangeUnknown, nil, 503, "unknown", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := resetConfirmConfig()
			config.Service = resetConfirmFunc(func(context.Context, string, string) (auth.PasswordChangeResult, error) {
				if test.name == "panic" {
					panic("private token and password")
				}
				return auth.PasswordChangeResult{State: test.state, Principal: auth.Principal{ID: "must-not-authenticate", Authenticated: true, Active: true, AuthVersion: 5}}, test.err
			})
			f := newResetConfirmFixture(t, config)
			_ = f.session.Set("private", "old-account-state")
			w := f.call("POST", "/reset/confirm", resetConfirmValues(), f.token)
			var retained string
			found, _ := f.session.Get("private", &retained)
			if w.Code != test.status || found != test.retain || w.Header().Get("X-Gogo-Password-Change") != test.header || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), fixtureResetToken) {
				t.Fatal(w.Code, found, w.Header(), w.Body.String())
			}
			if test.status == 303 && w.Header().Get("Location") != "/login" {
				t.Fatal(w.Header())
			}
		})
	}
}

func TestResetConfirmationTokenFormattingAndScriptAreSafe(t *testing.T) {
	page := PasswordResetConfirmPage{Token: ResetFormToken{value: fixtureResetToken}}
	for _, verb := range []string{"%v", "%+v", "%#v", "%d", "%s", "%q", "%x", "%f"} {
		for _, value := range []any{page, &page, page.Token, &page.Token} {
			if strings.Contains(fmt.Sprintf(verb, value), fixtureResetToken) {
				t.Fatal("routine token rendering", verb)
			}
		}
	}
	if _, err := json.Marshal(page); err == nil {
		t.Fatal("token page serialized into ordinary JSON")
	}
	if page.Token.FormValue() != fixtureResetToken {
		t.Fatal("explicit form access changed token")
	}
	for _, method := range []string{"GET", "HEAD", "POST"} {
		w := httptest.NewRecorder()
		PasswordResetConfirmScript().ServeHTTP(w, httptest.NewRequest(method, PasswordResetConfirmScriptPath(), nil))
		if method == "POST" {
			if w.Code != 405 {
				t.Fatal(w.Code)
			}
			continue
		}
		if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || w.Header().Get("X-Content-Type-Options") != "nosniff" || len(w.Result().Cookies()) != 0 {
			t.Fatal(w.Code, w.Header())
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("script HEAD has body")
		}
	}
}

func TestResetConfirmationRateGateAndInvalidConfiguration(t *testing.T) {
	config := resetConfirmConfig()
	var keys []string
	config.Limiter = limiterFunc(func(_ context.Context, key string, _ ratelimit.Limit, _ int) (ratelimit.Decision, error) {
		keys = append(keys, key)
		return ratelimit.Decision{Allowed: len(keys) == 1}, nil
	})
	config.Service = resetConfirmFunc(func(context.Context, string, string) (auth.PasswordChangeResult, error) {
		t.Fatal("rate denial reached mutation")
		return auth.PasswordChangeResult{}, nil
	})
	f := newResetConfirmFixture(t, config)
	w := f.call("POST", "/reset/confirm", resetConfirmValues(), f.token)
	if w.Code != 429 || len(keys) != 2 || strings.Contains(strings.Join(keys, ""), fixtureResetToken[:36]) || !strings.HasPrefix(keys[1], "auth:password_reset_confirm:token:") {
		t.Fatal(w.Code, keys)
	}
	for _, change := range []func(*PasswordResetConfirmConfig){func(c *PasswordResetConfirmConfig) { c.Service = nil }, func(c *PasswordResetConfirmConfig) { c.SuccessURL = "https://evil.invalid" }, func(c *PasswordResetConfirmConfig) { c.ScriptURL = "//evil.invalid/reset.js" }, func(c *PasswordResetConfirmConfig) { c.CSRF.Exempt = func(*http.Request) bool { return true } }} {
		config := resetConfirmConfig()
		change(&config)
		if _, err := PasswordResetConfirm(config); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
