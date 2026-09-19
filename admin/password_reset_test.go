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
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/security"
)

type resetRequester func(context.Context, string) error

func (f resetRequester) Request(ctx context.Context, identifier string) error {
	return f(ctx, identifier)
}

type resetConfirmer func(context.Context, string, string) (auth.PasswordChangeResult, error)

func (f resetConfirmer) Confirm(ctx context.Context, token, password string) (auth.PasswordChangeResult, error) {
	return f(ctx, token, password)
}

type recoveryNoStore struct{ calls int }

func (s *recoveryNoStore) Scope(context.Context, auth.Principal, string, models.Schema) (ScopedStore, error) {
	s.calls++
	return nil, errors.New("model store must not be consulted")
}

func TestAdminPasswordResetPresentationPreservesAnonymousCoreWorkflow(t *testing.T) {
	site, _ := newTestSite(t)
	site.config.LoginURL, site.config.PasswordResetURL = "/admin/login/", "/admin/password-reset/"
	site.config.Header = `Review <img src=x onerror=bad()>`
	store := &recoveryNoStore{}
	site.config.Store = store
	secret, _ := security.RandomToken(32)
	requestCalls, confirmCalls := 0, 0
	requester, err := site.PasswordResetRequestHandler(authviews.PasswordResetRequestConfig{Service: resetRequester(func(_ context.Context, identifier string) error {
		requestCalls++
		if identifier != "private-identifier" {
			t.Error("identifier changed")
		}
		return errors.New("private provider failure")
	}), NormalizeIdentifier: func(value string) (string, error) { return value, nil }, Limiter: loginTestLimiter{}, RateSecret: []byte(secret)})
	if err != nil {
		t.Fatal(err)
	}
	token := "00000000-0000-4000-8000-000000000001." + strings.Repeat("a", 43)
	confirm, err := site.PasswordResetConfirmHandler(authviews.PasswordResetConfirmConfig{Service: resetConfirmer(func(_ context.Context, supplied, password string) (auth.PasswordChangeResult, error) {
		confirmCalls++
		if supplied != token || password != "  new private credential  " {
			t.Error("token/password changed")
		}
		return auth.PasswordChangeResult{State: auth.PasswordUnchanged}, auth.ErrPasswordValidation
	}), Limiter: loginTestLimiter{}, RateSecret: []byte(secret)})
	if err != nil {
		t.Fatal(err)
	}
	call := func(handler http.Handler, path, method string, values url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(values.Encode()))
		r.RemoteAddr = "192.0.2.10:4100"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, test := range []struct {
		handler     http.Handler
		path, field string
	}{{requester, "/admin/password-reset/", "identifier"}, {confirm, "/admin/reset-confirm/", "new_password1"}} {
		get := call(test.handler, test.path, "GET", nil, nil)
		if get.Code != 200 || strings.Contains(get.Body.String(), "<img") || strings.Contains(get.Body.String(), "Model navigation") || !strings.Contains(get.Body.String(), site.cssVersion) || !strings.Contains(get.Body.String(), `name="`+test.field+`"`) || get.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(get.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("unsafe anonymous recovery page", get.Code)
		}
		values := url.Values{"identifier": {"private-identifier"}, "token": {token}, "new_password1": {"  new private credential  "}, "new_password2": {"  new private credential  "}}
		denied := call(test.handler, test.path, "POST", values, get.Result().Cookies())
		if denied.Code != 403 {
			t.Fatal("anonymous recovery bypassed CSRF", denied.Code)
		}
		values.Set("csrfmiddlewaretoken", hidden(t, get.Body.String(), "csrfmiddlewaretoken"))
		post := call(test.handler, test.path, "POST", values, get.Result().Cookies())
		if strings.Contains(post.Body.String(), "private-identifier") || strings.Contains(post.Body.String(), "private provider") || strings.Contains(post.Body.String(), "new private credential") {
			t.Fatal("recovery presentation disclosed private input/provider outcome")
		}
		if test.field == "identifier" && (post.Code != 200 || !strings.Contains(post.Body.String(), "If an eligible account exists")) {
			t.Fatal("reset request changed generic acknowledgement", post.Code)
		}
		if test.field == "new_password1" && (post.Code != 400 || !strings.Contains(post.Body.String(), `value="`+token+`"`) || !strings.Contains(post.Body.String(), authviews.PasswordResetConfirmScriptPath())) {
			t.Fatal("confirm retry lost explicit secret form boundary or script", post.Code)
		}
	}
	if requestCalls != 1 || confirmCalls != 1 || store.calls != 0 {
		t.Fatal("unexpected recovery effects", requestCalls, confirmCalls, store.calls)
	}
	login, err := site.renderLogin(context.Background(), authviews.LoginPage{Title: "Sign in"})
	if err != nil || !strings.Contains(string(login), `href="/admin/password-reset/"`) {
		t.Fatal("missing recovery link", err)
	}
}

func TestAdminRecoveryRejectsUnsafeConfiguration(t *testing.T) {
	site, _ := newTestSite(t)
	config := site.config
	config.PasswordResetURL = "//attacker.example"
	if _, err := NewSite(config); err == nil {
		t.Fatal("unsafe reset URL")
	}
	exempt := security.CSRFConfig{Exempt: func(*http.Request) bool { return true }}
	if _, err := site.PasswordResetRequestHandler(authviews.PasswordResetRequestConfig{CSRF: exempt}); err == nil {
		t.Fatal("reset request CSRF exemption")
	}
	if _, err := site.PasswordResetConfirmHandler(authviews.PasswordResetConfirmConfig{CSRF: exempt}); err == nil {
		t.Fatal("reset confirm CSRF exemption")
	}
	if _, err := site.PasswordResetConfirmHandler(authviews.PasswordResetConfirmConfig{}); err == nil {
		t.Fatal("missing completion URL")
	}
}

func TestAdminResetRequestUsesSharedResponsiveInsets(t *testing.T) {
	site, _ := newTestSite(t)
	site.config.LoginURL = "/admin/login/"
	for _, submitted := range []bool{false, true} {
		body, err := site.renderPasswordResetRequest(context.Background(), authviews.PasswordResetRequestPage{Title: "Reset password", Submitted: submitted})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `<p><a href="/admin/login/">Back to sign in</a></p>`) {
			t.Fatal("recovery navigation lost the shared inset")
		}
		if submitted {
			if !strings.Contains(string(body), `<p role="status">`) || strings.Contains(string(body), `name="identifier"`) {
				t.Fatal("recovery acknowledgement layout changed form behavior")
			}
		} else if !strings.Contains(string(body), `<p>Enter your account identifier`) {
			t.Fatal("recovery description lost the shared inset")
		}
	}
}
