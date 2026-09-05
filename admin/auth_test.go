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

type passwordChangerFunc func(context.Context, string, string) (auth.PasswordChangeResult, error)

func (f passwordChangerFunc) ChangeOwnPassword(ctx context.Context, old, password string) (auth.PasswordChangeResult, error) {
	return f(ctx, old, password)
}

func TestAdminPasswordChangeReusesCoreWithoutPasswordEchoOrStaffBypass(t *testing.T) {
	site, _ := newTestSite(t)
	site.config.LoginURL = "/admin/login/?site=review"
	site.config.PasswordChangeURL = "/admin/password-change/"
	var calls int
	changer := passwordChangerFunc(func(ctx context.Context, old, password string) (auth.PasswordChangeResult, error) {
		calls++
		if old != "  current credential  " || password != "  new credential  " {
			t.Error("significant whitespace changed")
		}
		return auth.PasswordChangeResult{State: auth.PasswordUnchanged}, auth.ErrCredentials
	})
	handler, err := site.PasswordChangeHandler(authviews.PasswordChangeConfig{Changer: changer, Limiter: loginTestLimiter{}, RateSecret: []byte(strings.Repeat("r", 32))})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []auth.Principal{{}, {ID: "not-staff", Authenticated: true, Active: true, AuthVersion: 1}, {ID: "inactive", Authenticated: true, Staff: true, AuthVersion: 1}} {
		request := httptest.NewRequest("GET", "http://example.test/admin/password-change/", nil)
		request = request.WithContext(auth.WithPrincipal(request.Context(), p))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := 403
		if !p.Authenticated {
			want = 303
			location, _ := url.Parse(response.Header().Get("Location"))
			if location.Query().Get("site") != "review" || location.Query().Get("next") != "/admin/password-change/" {
				t.Fatal("unsafe account redirect")
			}
		}
		if response.Code != want || strings.Contains(response.Body.String(), "old_password") || calls != 0 {
			t.Fatal("staff gate bypassed")
		}
	}
	p := auth.Principal{ID: "staff-reviewer", Authenticated: true, Active: true, Staff: true, AuthVersion: 1}
	session := sessions.New(sessions.Record{})
	get := httptest.NewRequest("GET", "http://example.test/admin/password-change/", nil)
	get = get.WithContext(auth.WithPrincipal(sessions.WithSession(get.Context(), session), p))
	form := httptest.NewRecorder()
	handler.ServeHTTP(form, get)
	if form.Code != 200 || strings.Contains(form.Body.String(), "Model navigation") {
		t.Fatal("password form queried model presentation", form.Code)
	}
	for _, required := range []string{`name="old_password"`, `name="new_password1"`, `name="new_password2"`, `autocomplete="current-password"`, `autocomplete="new-password"`, site.cssVersion} {
		if !strings.Contains(form.Body.String(), required) {
			t.Fatal("missing account form element", required)
		}
	}
	values := url.Values{"old_password": {"  current credential  "}, "new_password1": {"  new credential  "}, "new_password2": {"  new credential  "}, "csrfmiddlewaretoken": {hidden(t, form.Body.String(), "csrfmiddlewaretoken")}, "user_id": {"another-account"}, "superuser": {"true"}}
	post := httptest.NewRequest("POST", "http://example.test/admin/password-change/", strings.NewReader(values.Encode()))
	post.RemoteAddr = "192.0.2.10:3200"
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range form.Result().Cookies() {
		post.AddCookie(cookie)
	}
	post = post.WithContext(auth.WithPrincipal(sessions.WithSession(post.Context(), session), p))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, post)
	if response.Code != 400 || calls != 1 || response.Header().Get("X-Gogo-Password-Change") != "unchanged" || strings.Contains(response.Body.String(), "current credential") || strings.Contains(response.Body.String(), "new credential") {
		t.Fatal("credential rejection leaked input or lost outcome", response.Code)
	}
	body, err := site.renderPasswordChange(auth.WithPrincipal(context.Background(), p), authviews.PasswordChangePage{Title: `<script>bad()</script>`, Error: `<img src=x onerror=bad()>`, CSRFToken: "masked"})
	if err != nil || strings.Contains(string(body), "<script>") || strings.Contains(string(body), "<img") {
		t.Fatal("password renderer escape")
	}
}

func TestAdminPasswordChangeLinkAndConfiguration(t *testing.T) {
	site, _ := newTestSite(t)
	site.config.PasswordChangeURL = "/admin/password-change/"
	response := perform(site, "GET", "/admin/", principal(), nil, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `href="/admin/password-change/"`) {
		t.Fatal("missing password change header link")
	}
	config := site.config
	config.PasswordChangeURL = "//attacker.test"
	if _, err := NewSite(config); err == nil {
		t.Fatal("unsafe password change URL")
	}
	if _, err := site.PasswordChangeHandler(authviews.PasswordChangeConfig{CSRF: security.CSRFConfig{Exempt: func(*http.Request) bool { return true }}}); err == nil {
		t.Fatal("account CSRF exemption")
	}
}
