package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/messages"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

func TestLoginRotatesIdentityAndSwitchingAccountsDropsData(t *testing.T) {
	for _, oldID := range []string{"", "first"} {
		t.Run(oldID, func(t *testing.T) {
			session := sessions.New(sessions.Record{ID: "old-cookie-id", Version: 1})
			_ = session.Set("cart", "fixture")
			if oldID != "" {
				_ = session.Set(authSessionKey, sessionIdentity{ID: oldID, AuthVersion: 1})
			}
			r := httptest.NewRequest("POST", "/auth/login", nil).WithContext(sessions.WithSession(context.Background(), session))
			w := httptest.NewRecorder()
			p := Principal{ID: "second", AuthVersion: 2, Active: true, Authenticated: true}
			if err := Login(w, r, p, security.CSRFConfig{Secure: true}); err != nil {
				t.Fatal(err)
			}
			if FromContext(r.Context()).ID != "second" {
				t.Fatal("current request not authenticated")
			}
			var identity sessionIdentity
			if _, err := session.Get(authSessionKey, &identity); err != nil || identity.ID != "second" || identity.AuthVersion != 2 {
				t.Fatal(identity, err)
			}
			var cart string
			found, _ := session.Get("cart", &cart)
			if found != (oldID == "") {
				t.Fatal("account-switch state leak or anonymous data loss")
			}
			if len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].Secure {
				t.Fatal("CSRF not rotated")
			}
		})
	}
}

func TestCurrentPrincipalRefreshAndInvalidation(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal Principal
		err       error
		status    int
		anonymous bool
	}{
		{"current", Principal{ID: "account", Active: true, AuthVersion: 3, Permissions: []string{"catalog.view_product"}}, nil, 200, false},
		{"version", Principal{ID: "account", Active: true, AuthVersion: 4}, nil, 200, true},
		{"inactive", Principal{ID: "account", AuthVersion: 3}, nil, 200, true},
		{"missing", Principal{}, ErrUnauthenticated, 200, true},
		{"outage", Principal{}, errors.New("private connection details"), 503, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := sessions.New(sessions.Record{ID: "old-id", Version: 1})
			_ = session.Set(authSessionKey, sessionIdentity{ID: "account", AuthVersion: 3})
			_ = session.Set("private", "account-state")
			middleware, err := SessionMiddleware(PrincipalLoaderFunc(func(context.Context, string) (Principal, error) { return test.principal, test.err }))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/", nil).WithContext(sessions.WithSession(context.Background(), session))
			w := httptest.NewRecorder()
			called := false
			middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				p := FromContext(r.Context())
				if p.Authenticated == test.anonymous {
					t.Fatal(p)
				}
				if !test.anonymous && len(p.Permissions) != 1 {
					t.Fatal("stale grants")
				}
				w.WriteHeader(200)
			})).ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatal(w.Code)
			}
			if test.status == 503 && called {
				t.Fatal("store outage reached protected handler")
			}
			if test.anonymous && test.status != 503 {
				var old string
				if found, _ := session.Get("private", &old); found {
					t.Fatal("invalid account retained session data")
				}
			}
		})
	}
}

func TestLogoutImmediatelyClearsRequestAndSessionIdentity(t *testing.T) {
	session := sessions.New(sessions.Record{ID: "old", Version: 1})
	_ = session.Set(authSessionKey, sessionIdentity{ID: "account", AuthVersion: 1})
	ctx := WithPrincipal(sessions.WithSession(context.Background(), session), Principal{ID: "account", Authenticated: true})
	r := httptest.NewRequest("POST", "/auth/logout", nil).WithContext(ctx)
	if err := Logout(httptest.NewRecorder(), r, security.CSRFConfig{}); err != nil {
		t.Fatal(err)
	}
	if FromContext(r.Context()).Authenticated || session.Snapshot().ID != "" || len(session.Snapshot().Data) != 0 {
		t.Fatal("logout retained account state")
	}
}

func TestLoginRenderedCSRFTokenMatchesRotatedCookie(t *testing.T) {
	csrf, err := security.CSRF(security.CSRFConfig{})
	if err != nil {
		t.Fatal(err)
	}
	session := sessions.New(sessions.Record{})
	handler := csrf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			if err := Login(w, r, Principal{ID: "account", AuthVersion: 1, Authenticated: true, Active: true}, security.CSRFConfig{}); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = w.Write([]byte(security.CSRFToken(r)))
	}))
	call := func(path, method, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(url.Values{"csrfmiddlewaretoken": {token}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		r = r.WithContext(sessions.WithSession(r.Context(), session))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	first := call("/form", "GET", "", nil)
	oldCookie := first.Result().Cookies()[0]
	login := call("/login", "POST", first.Body.String(), oldCookie)
	if login.Code != 200 {
		t.Fatal(login.Code, login.Body)
	}
	cookies := login.Result().Cookies()
	newCookie := cookies[len(cookies)-1]
	if oldCookie.Value == newCookie.Value {
		t.Fatal("CSRF secret not rotated")
	}
	verified := call("/verify", "POST", login.Body.String(), newCookie)
	if verified.Code != 200 {
		t.Fatal("rendered post-login token does not match cookie", verified.Code, verified.Body)
	}
}

func TestSwitchingAccountDiscardsLoadedPrivateFlashMessages(t *testing.T) {
	for _, consume := range []bool{false, true} {
		t.Run(map[bool]string{false: "persist", true: "consume"}[consume], func(t *testing.T) {
			session := sessions.New(sessions.Record{ID: "old", Version: 1})
			_ = session.Set(authSessionKey, sessionIdentity{ID: "first", AuthVersion: 1})
			_ = session.Set("_gogo_messages", []messages.Message{{Level: messages.Info, Text: "first account private notice"}})
			middleware, err := messages.Middleware(messages.Config{})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/login", nil).WithContext(sessions.WithSession(context.Background(), session))
			middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := Login(w, r, Principal{ID: "second", AuthVersion: 1, Active: true, Authenticated: true}, security.CSRFConfig{}); err != nil {
					t.Fatal(err)
				}
				if err := messages.AddPublic(r, messages.Success, "Signed in"); err != nil {
					t.Fatal(err)
				}
				if consume {
					values, err := messages.Consume(r)
					if err != nil || len(values) != 1 || values[0].Text != "Signed in" {
						t.Fatal("cross-account flash leak", values, err)
					}
				}
				w.WriteHeader(200)
			})).ServeHTTP(httptest.NewRecorder(), r)
			var values []messages.Message
			_, _ = session.Get("_gogo_messages", &values)
			for _, value := range values {
				if value.Text != "Signed in" {
					t.Fatal("old message persisted into new identity")
				}
			}
		})
	}
}
