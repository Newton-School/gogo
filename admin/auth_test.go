package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	authviews "github.com/Newton-School/gogo/core/auth/views"
	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

type credentialFunc func(context.Context, string, string) (auth.Principal, error)

func (f credentialFunc) Authenticate(ctx context.Context, identifier, password string) (auth.Principal, error) {
	return f(ctx, identifier, password)
}

type loginTestLimiter struct{}

func (loginTestLimiter) Allow(context.Context, string, ratelimit.Limit, int) (ratelimit.Decision, error) {
	return ratelimit.Decision{Allowed: true}, nil
}

func TestAdminLoginRendererIsBrandedEscapedAndModelIndependent(t *testing.T) {
	site, _ := newTestSite(t)
	site.config.Header = `Review <script>bad()</script>`
	body, err := site.renderLogin(context.Background(), authviews.LoginPage{Title: "Sign in", Identifier: `"><img src=x onerror=bad()>`, Next: "/admin/", CSRFToken: "masked", Error: "Unable to sign in."})
	if err != nil || strings.Contains(string(body), "<script>") || strings.Contains(string(body), "<img") || strings.Contains(string(body), "Product") || strings.Contains(string(body), "Model navigation") {
		t.Fatal("anonymous page disclosed navigation or failed escaping", string(body), err)
	}
	for _, required := range []string{`autocomplete="username"`, `autocomplete="current-password"`, `name="csrfmiddlewaretoken"`, `role="alert"`, `href="#main"`, `aria-describedby="login_error"`, site.cssVersion} {
		if !strings.Contains(string(body), required) {
			t.Fatal("missing accessible login element", required)
		}
	}
}

func TestAdminLoginRequiresStaffBeforeCreatingIdentity(t *testing.T) {
	site, _ := newTestSite(t)
	for _, staff := range []bool{false, true} {
		backend := credentialFunc(func(context.Context, string, string) (auth.Principal, error) {
			return auth.Principal{ID: "verified", Authenticated: true, Active: true, Staff: staff, AuthVersion: 1}, nil
		})
		handler, err := site.LoginHandler(authviews.LoginConfig{Authenticator: backend, NormalizeIdentifier: func(s string) (string, error) { return s, nil }, Limiter: loginTestLimiter{}, RateSecret: []byte(strings.Repeat("a", 32))})
		if err != nil {
			t.Fatal(err)
		}
		session := sessions.New(sessions.Record{})
		get := httptest.NewRequest("GET", "http://example.test/admin/login/", nil)
		get = get.WithContext(sessions.WithSession(get.Context(), session))
		form := httptest.NewRecorder()
		handler.ServeHTTP(form, get)
		if form.Code != 200 {
			t.Fatal(form.Code)
		}
		token := hidden(t, form.Body.String(), "csrfmiddlewaretoken")
		values := url.Values{"identifier": {"verified"}, "password": {"  significant credential  "}, "csrfmiddlewaretoken": {token}}
		post := httptest.NewRequest("POST", "http://example.test/admin/login/", strings.NewReader(values.Encode()))
		post.RemoteAddr = "192.0.2.10:3200"
		post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, cookie := range form.Result().Cookies() {
			post.AddCookie(cookie)
		}
		post = post.WithContext(sessions.WithSession(post.Context(), session))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, post)
		var identity any
		found, err := session.Get("_gogo_auth", &identity)
		if err != nil || found != staff || staff && response.Code != 303 || !staff && response.Code != 401 {
			t.Fatal("staff boundary failed", staff, response.Code, found, err)
		}
	}
	if _, err := (staffAuthenticator{backend: credentialFunc(func(context.Context, string, string) (auth.Principal, error) {
		return auth.Principal{}, errors.New("provider unavailable")
	})}).Authenticate(context.Background(), "", ""); err == nil || errors.Is(err, auth.ErrCredentials) {
		t.Fatal("provider outage was collapsed into credential rejection", err)
	}
}

func TestAdminCredentialRoutesPreserveNextAndRejectUnsafeConfiguration(t *testing.T) {
	site, _ := newTestSite(t)
	site.config.LoginURL = "/login/?site=review"
	response := perform(site, "GET", "/admin/?q=hello", auth.Principal{}, nil, nil)
	redirect, err := url.Parse(response.Header().Get("Location"))
	if err != nil || response.Code != 303 || redirect.Query().Get("site") != "review" || redirect.Query().Get("next") != "/admin/?q=hello" {
		t.Fatal(response.Code, response.Header(), err)
	}
	for _, unsafe := range []string{"//attacker.test", "/%2f/attacker.test", "/bad%escape", "/\\attacker.test"} {
		config := site.config
		config.LoginURL = unsafe
		if _, err := NewSite(config); err == nil {
			t.Fatal("unsafe login URL accepted", unsafe)
		}
		config.LoginURL, config.LogoutURL = "/login/", unsafe
		if _, err := NewSite(config); err == nil {
			t.Fatal("unsafe logout URL accepted", unsafe)
		}
	}
	if _, err := site.LogoutHandler(authviews.LogoutConfig{CSRF: security.CSRFConfig{Exempt: func(*http.Request) bool { return true }}}); err == nil {
		t.Fatal("CSRF exemption accepted for logout")
	}
}

func TestAdminSnapshotsSharedCSRFOriginConfiguration(t *testing.T) {
	base, _ := newTestSite(t)
	origins := []string{"https://trusted.example"}
	config := base.config
	config.CSRF.TrustedOrigins = origins
	site, err := NewSite(config)
	if err != nil {
		t.Fatal(err)
	}
	origins[0] = "https://attacker.example"
	if site.config.CSRF.TrustedOrigins[0] != "https://trusted.example" {
		t.Fatal("site CSRF trust changed through caller-owned slice")
	}
	credentialCSRF, err := site.accountCSRF(security.CSRFConfig{})
	if err != nil || credentialCSRF.TrustedOrigins[0] != "https://trusted.example" {
		t.Fatal("subsequent login inherited caller-mutated origins", credentialCSRF, err)
	}
}
