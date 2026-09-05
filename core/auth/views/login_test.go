package views

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/sessions"
)

type credentialsFunc func(context.Context, string, string) (auth.Principal, error)

func (f credentialsFunc) Authenticate(ctx context.Context, id, password string) (auth.Principal, error) {
	return f(ctx, id, password)
}

type limiterFunc func(context.Context, string, ratelimit.Limit, int) (ratelimit.Decision, error)

func (f limiterFunc) Allow(ctx context.Context, key string, limit ratelimit.Limit, cost int) (ratelimit.Decision, error) {
	return f(ctx, key, limit, cost)
}

func loginConfig() LoginConfig {
	return LoginConfig{Authenticator: credentialsFunc(func(context.Context, string, string) (auth.Principal, error) {
		return auth.Principal{}, auth.ErrCredentials
	}), NormalizeIdentifier: auth.NormalizeIdentifier, RateSecret: []byte(strings.Repeat("fixture-secret", 3)), Limiter: limiterFunc(func(context.Context, string, ratelimit.Limit, int) (ratelimit.Decision, error) {
		return ratelimit.Decision{Allowed: true}, nil
	})}
}

var csrfInput = regexp.MustCompile(`name="csrfmiddlewaretoken" value="([^"]+)"`)

type loginFixture struct {
	t       *testing.T
	handler http.Handler
	session *sessions.Session
	cookies map[string]*http.Cookie
	token   string
}

func newLoginFixture(t *testing.T, config LoginConfig) *loginFixture {
	t.Helper()
	handler, err := Login(config)
	if err != nil {
		t.Fatal(err)
	}
	f := &loginFixture{t: t, handler: handler, session: sessions.New(sessions.Record{}), cookies: map[string]*http.Cookie{}}
	response := f.call("GET", "/login", nil, "")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	return f
}

func (f *loginFixture) call(method, path string, values url.Values, headerToken string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(values.Encode()))
	r.RemoteAddr = "192.0.2.1:4242"
	r = r.WithContext(sessions.WithSession(r.Context(), f.session))
	for _, c := range f.cookies {
		r.AddCookie(c)
	}
	if values != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if headerToken != "" {
		r.Header.Set("X-CSRFToken", headerToken)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		f.cookies[c.Name] = c
	}
	if match := csrfInput.FindStringSubmatch(w.Body.String()); len(match) == 2 {
		f.token = match[1]
	}
	return w
}

func TestLoginFormCSRFAndGenericCredentialFailure(t *testing.T) {
	config := loginConfig()
	var calls int
	config.Authenticator = credentialsFunc(func(_ context.Context, id, password string) (auth.Principal, error) {
		calls++
		if password != "  secret value  " {
			t.Fatal("password whitespace changed")
		}
		return auth.Principal{}, auth.ErrCredentials
	})
	config.OnFailure = func(context.Context, string) { panic("observer cannot change response") }
	f := newLoginFixture(t, config)
	values := url.Values{"identifier": {`<script>alert(1)</script>`}, "password": {"  secret value  "}}
	w := f.call("POST", "/login", values, "")
	if w.Code != 403 || calls != 0 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("CSRF did not protect work", w.Code, calls)
	}
	w = f.call("POST", "/login", values, f.token)
	if w.Code != 401 || calls != 1 || strings.Contains(w.Body.String(), values.Get("identifier")) || strings.Contains(w.Body.String(), "secret value") || !strings.Contains(w.Body.String(), "Unable to sign in") {
		t.Fatal("unsafe failed login response", w.Code, w.Body.String())
	}
	if f.session.Modified() {
		t.Fatal("invalid credentials modified session")
	}
	values.Add("password", "duplicate")
	w = f.call("POST", "/login", values, f.token)
	if w.Code != 400 || calls != 1 {
		t.Fatal("duplicate password reached authenticator", w.Code, calls)
	}
	w = f.call("HEAD", "/login", nil, "")
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("HEAD returned body")
	}
}

func TestLoginNormalizesRateBucketOnlyAndRotatesSession(t *testing.T) {
	config := loginConfig()
	config.NormalizeIdentifier = func(v string) (string, error) { return "prefix:" + strings.TrimSpace(v), nil }
	config.Authenticator = credentialsFunc(func(_ context.Context, id, password string) (auth.Principal, error) {
		if id != " user " || password != "    " {
			t.Fatal("credentials transformed before backend", id)
		}
		return auth.Principal{ID: "verified-account", Active: true, Authenticated: true, AuthVersion: 1}, nil
	})
	var keys []string
	config.Limiter = limiterFunc(func(_ context.Context, key string, _ ratelimit.Limit, cost int) (ratelimit.Decision, error) {
		keys = append(keys, key)
		if cost != 1 {
			t.Fatal(cost)
		}
		return ratelimit.Decision{Allowed: true}, nil
	})
	f := newLoginFixture(t, config)
	oldCSRF := f.cookies["gogo_csrf"].Value
	w := f.call("POST", "/login", url.Values{"identifier": {" user "}, "password": {"    "}, "next": {"//attacker.test/"}}, f.token)
	if w.Code != 303 || w.Header().Get("Location") != "/" || oldCSRF == f.cookies["gogo_csrf"].Value || !f.session.Modified() {
		t.Fatal("login did not rotate or validate next", w.Code, w.Header())
	}
	if len(keys) != 2 || strings.Contains(strings.Join(keys, ""), "user") || strings.Contains(strings.Join(keys, ""), "192.0.2.1") {
		t.Fatal("rate keys disclosed identifiers", keys)
	}
	// Captured construction secrets cannot change through caller-owned memory.
	for i := range config.RateSecret {
		config.RateSecret[i] = 'x'
	}
	w = f.call("GET", "/login", nil, "")
	w = f.call("POST", "/login", url.Values{"identifier": {" user "}, "password": {"    "}}, f.token)
	if w.Code != 303 || !reflect.DeepEqual(keys[:2], keys[2:]) {
		t.Fatal("mutable secret changed rate key", w.Code)
	}
}

func TestLoginRateFailureAndBodyBounds(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		delay  time.Duration
		err    error
		status int
		retry  string
	}{
		{"throttle", time.Millisecond, nil, 429, "1"}, {"rounded", 1500 * time.Millisecond, nil, 429, "2"}, {"provider", 0, errors.New("secret provider error"), 503, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			config := loginConfig()
			config.Authenticator = credentialsFunc(func(context.Context, string, string) (auth.Principal, error) {
				t.Fatal("throttled authentication ran")
				return auth.Principal{}, nil
			})
			config.Limiter = limiterFunc(func(context.Context, string, ratelimit.Limit, int) (ratelimit.Decision, error) {
				return ratelimit.Decision{RetryAfter: scenario.delay}, scenario.err
			})
			f := newLoginFixture(t, config)
			w := f.call("POST", "/login", url.Values{"identifier": {"user"}, "password": {"secret"}}, f.token)
			if w.Code != scenario.status || w.Header().Get("Retry-After") != scenario.retry || strings.Contains(w.Body.String(), "secret") {
				t.Fatal(w.Code, w.Header(), w.Body.String())
			}
		})
	}
	f := newLoginFixture(t, loginConfig())
	w := f.call("POST", "/login", url.Values{"identifier": {"user"}, "password": {strings.Repeat("x", 64<<10)}}, f.token)
	if w.Code != 400 {
		t.Fatal("body bound not enforced with header CSRF", w.Code)
	}
	config := loginConfig()
	config.CSRF.Exempt = func(*http.Request) bool { return true }
	if _, err := Login(config); err == nil {
		t.Fatal("CSRF exempt account handler accepted")
	}
	config = loginConfig()
	config.SuccessURL = "https://attacker.test/"
	if _, err := Login(config); err == nil {
		t.Fatal("external fallback accepted")
	}
}

func TestLogoutRequiresPOSTAndFlushesAccountData(t *testing.T) {
	f := newLoginFixture(t, loginConfig())
	if err := f.session.Set("private", "old-account-data"); err != nil {
		t.Fatal(err)
	}
	handler, err := Logout(LogoutConfig{SuccessURL: "/signed-out"})
	if err != nil {
		t.Fatal(err)
	}
	f.handler = handler
	w := f.call("GET", "/logout", nil, "")
	var value string
	if found, _ := f.session.Get("private", &value); w.Code != 405 || !found {
		t.Fatal("GET logged out", w.Code)
	}
	w = f.call("POST", "/logout", nil, "")
	if w.Code != 403 {
		t.Fatal("logout missing CSRF accepted", w.Code)
	}
	w = f.call("POST", "/logout", nil, f.token)
	if found, _ := f.session.Get("private", &value); w.Code != 303 || found || w.Header().Get("Location") != "/signed-out" {
		t.Fatal("logout failed", w.Code)
	}
}

func TestLoginRendererFailureIsNotPartialResponse(t *testing.T) {
	config := loginConfig()
	config.Render = func(context.Context, LoginPage) ([]byte, error) {
		return []byte("partial secret"), errors.New("private rendering error")
	}
	handler, err := Login(config)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://example.test/login", nil)
	r = r.WithContext(sessions.WithSession(r.Context(), sessions.New(sessions.Record{})))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "private") {
		t.Fatal(w.Code, w.Body.String())
	}
}
