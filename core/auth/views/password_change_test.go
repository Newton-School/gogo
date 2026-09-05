package views

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

type passwordChangerFunc func(context.Context, string, string) (auth.PasswordChangeResult, error)

func (f passwordChangerFunc) ChangeOwnPassword(ctx context.Context, old, password string) (auth.PasswordChangeResult, error) {
	return f(ctx, old, password)
}

func passwordConfig() PasswordChangeConfig {
	l := loginConfig()
	return PasswordChangeConfig{Limiter: l.Limiter, RateSecret: l.RateSecret, PreserveSession: true, SuccessURL: "/changed", LoginURL: "/login", Changer: passwordChangerFunc(func(ctx context.Context, old, password string) (auth.PasswordChangeResult, error) {
		p := auth.FromContext(ctx)
		p.AuthVersion++
		return auth.PasswordChangeResult{State: auth.PasswordChanged, Principal: p}, nil
	})}
}

func newPasswordFixture(t *testing.T, config PasswordChangeConfig) *loginFixture {
	t.Helper()
	handler, err := PasswordChange(config)
	if err != nil {
		t.Fatal(err)
	}
	p := auth.Principal{ID: "verified-account", AuthVersion: 1, Active: true, Authenticated: true}
	f := &loginFixture{t: t, handler: handler, principal: p, session: sessions.New(sessions.Record{ID: "existing-identity", Version: 1}), cookies: map[string]*http.Cookie{}}
	r := httptest.NewRequest("GET", "/", nil).WithContext(sessions.WithSession(context.Background(), f.session))
	if err := auth.Login(httptest.NewRecorder(), r, p, security.CSRFConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := f.session.Set("private", "same-account-data"); err != nil {
		t.Fatal(err)
	}
	if w := f.call("GET", "/password-change", nil, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	return f
}

func passwordValues() url.Values {
	return url.Values{"old_password": {"  current credential  "}, "new_password1": {"  new credential  "}, "new_password2": {"  new credential  "}}
}

func TestPasswordChangeCSRFFormBoundsAndExactCredentials(t *testing.T) {
	config := passwordConfig()
	calls := 0
	config.Changer = passwordChangerFunc(func(ctx context.Context, old, password string) (auth.PasswordChangeResult, error) {
		calls++
		if old != "  current credential  " || password != "  new credential  " {
			t.Fatal("credential transformed")
		}
		return auth.PasswordChangeResult{State: auth.PasswordUnchanged}, auth.ErrCredentials
	})
	f := newPasswordFixture(t, config)
	if w := f.call("POST", "/password-change", passwordValues(), ""); w.Code != 403 || calls != 0 {
		t.Fatal(w.Code, calls)
	}
	for _, variant := range []string{"duplicate", "mismatch", "empty", "oversize"} {
		values := passwordValues()
		switch variant {
		case "duplicate":
			values.Add("old_password", "extra")
		case "mismatch":
			values.Set("new_password2", "different")
		case "empty":
			values.Set("new_password1", "")
		case "oversize":
			values.Set("old_password", strings.Repeat("x", 4097))
		}
		w := f.call("POST", "/password-change", values, f.token)
		if w.Code != 400 || calls != 0 || w.Header().Get("X-Gogo-Password-Change") != "unchanged" {
			t.Fatal(variant, w.Code, calls)
		}
	}
	values := passwordValues()
	values.Set("user_id", "attacker-chosen-account")
	values.Set("next", "https://attacker.invalid")
	w := f.call("POST", "/password-change", values, f.token)
	if w.Code != 400 || calls != 1 || strings.Contains(w.Body.String(), "credential") || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal(w.Code, calls, w.Body.String())
	}
	if w := f.call("HEAD", "/password-change", nil, ""); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestPasswordChangeRefreshAndExplicitLogout(t *testing.T) {
	for _, preserve := range []bool{true, false} {
		config := passwordConfig()
		config.PreserveSession = preserve
		f := newPasswordFixture(t, config)
		before := f.cookies["gogo_csrf"].Value
		w := f.call("POST", "/password-change", passwordValues(), f.token)
		if w.Code != 303 || w.Header().Get("X-Gogo-Password-Change") != "changed" || before == f.cookies["gogo_csrf"].Value {
			t.Fatal(w.Code, w.Header())
		}
		var retained string
		found, _ := f.session.Get("private", &retained)
		if found != preserve {
			t.Fatal("incorrect private-state policy", preserve, found)
		}
		location := "/login"
		if preserve {
			location = "/changed"
		}
		if w.Header().Get("Location") != location {
			t.Fatal(w.Header())
		}
	}
}

func TestPasswordChangeOutcomesNeverClaimRollbackOrPromoteFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		state    auth.PasswordChangeState
		err      error
		status   int
		retained bool
		header   string
	}{
		{"policy", auth.PasswordUnchanged, auth.ErrPasswordValidation, 400, true, "unchanged"},
		{"denied", auth.PasswordUnchanged, auth.ErrPermissionDenied, 403, true, "unchanged"},
		{"stale", auth.PasswordUnchanged, auth.ErrAccountChanged, 401, false, "unchanged"},
		{"database", auth.PasswordUnchanged, errors.New("private provider details"), 503, true, "unchanged"},
		{"committed", auth.PasswordChanged, errors.New("private after-commit failure"), 503, false, "changed"},
		{"unknown", auth.PasswordChangeUnknown, errors.New("private commit uncertainty"), 503, false, "unknown"},
		{"invalid", "", nil, 503, false, "unknown"},
		{"unchanged-success", auth.PasswordUnchanged, nil, 503, false, "unknown"},
		{"invalid-principal", auth.PasswordChanged, nil, 503, false, "changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := passwordConfig()
			config.OnFailure = func(context.Context, string) { panic("isolated observer") }
			config.Changer = passwordChangerFunc(func(context.Context, string, string) (auth.PasswordChangeResult, error) {
				return auth.PasswordChangeResult{State: test.state}, test.err
			})
			f := newPasswordFixture(t, config)
			w := f.call("POST", "/password-change", passwordValues(), f.token)
			var private string
			found, _ := f.session.Get("private", &private)
			if w.Code != test.status || found != test.retained || w.Header().Get("X-Gogo-Password-Change") != test.header || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "credential") {
				t.Fatal(w.Code, found, w.Header(), w.Body.String())
			}
		})
	}
}

func TestPasswordChangeAuthenticationRatesAndRendererFailures(t *testing.T) {
	config := passwordConfig()
	calls := 0
	config.Changer = passwordChangerFunc(func(context.Context, string, string) (auth.PasswordChangeResult, error) {
		calls++
		return auth.PasswordChangeResult{}, nil
	})
	var keys []string
	config.Limiter = limiterFunc(func(_ context.Context, key string, _ ratelimit.Limit, _ int) (ratelimit.Decision, error) {
		keys = append(keys, key)
		return ratelimit.Decision{Allowed: len(keys) == 1}, nil
	})
	f := newPasswordFixture(t, config)
	w := f.call("POST", "/password-change", passwordValues(), f.token)
	if w.Code != 429 || calls != 0 || len(keys) != 2 || strings.Contains(strings.Join(keys, ""), "verified-account") || strings.Contains(strings.Join(keys, ""), "192.0.2.1") || !strings.HasPrefix(keys[1], "auth:password_change:account:") {
		t.Fatal(w.Code, calls, keys)
	}
	f.principal = auth.Principal{}
	if w := f.call("POST", "/password-change", passwordValues(), f.token); w.Code != 401 || calls != 0 {
		t.Fatal(w.Code, calls)
	}
	if w := f.call("GET", "/password-change", nil, ""); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Fatal(w.Code, w.Header())
	}
	config = passwordConfig()
	config.Render = func(context.Context, PasswordChangePage) ([]byte, error) {
		return []byte("partial private renderer output"), errors.New("private")
	}
	handler, err := PasswordChange(config)
	if err != nil {
		t.Fatal(err)
	}
	f.handler = handler
	f.principal = auth.Principal{ID: "verified-account", AuthVersion: 1, Active: true, Authenticated: true}
	w = f.call("GET", "/password-change", nil, "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "private") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, mutate := range []func(*PasswordChangeConfig){
		func(c *PasswordChangeConfig) { c.Changer = nil }, func(c *PasswordChangeConfig) { c.RateSecret = nil }, func(c *PasswordChangeConfig) { c.LoginURL = "//evil.invalid" }, func(c *PasswordChangeConfig) { c.CSRF.Exempt = func(*http.Request) bool { return true } },
	} {
		c := passwordConfig()
		mutate(&c)
		if _, err := PasswordChange(c); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
