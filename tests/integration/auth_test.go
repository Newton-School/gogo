package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/messages"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

// This exercises the custom principal-loader port with real Redis sessions and
// HTTP middleware. It does not stand in for password/account persistence tests.
func TestRedisAuthenticatedSessionRotationAndInvalidation(t *testing.T) {
	ctx := context.Background()
	config := fixture.Start(t)
	config.Role = connector.SessionRole
	connection, err := connector.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	store := &connector.Sessions{Connection: connection}
	key, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "integration-sessions")
	if err != nil {
		t.Fatal(err)
	}
	sessionMiddleware, err := sessions.Middleware(sessions.MiddlewareConfig{Store: store, Signer: signer, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	var version atomic.Uint64
	version.Store(1)
	principal := func() auth.Principal {
		return auth.Principal{ID: "fixture-account", Active: true, Authenticated: true, AuthVersion: version.Load(), Permissions: []string{"catalog.view_product"}}
	}
	authMiddleware, err := auth.SessionMiddleware(auth.PrincipalLoaderFunc(func(_ context.Context, id string) (auth.Principal, error) {
		if id != "fixture-account" {
			return auth.Principal{}, auth.ErrUnauthenticated
		}
		return principal(), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	csrfMiddleware, err := security.CSRF(security.CSRFConfig{})
	if err != nil {
		t.Fatal(err)
	}
	messageMiddleware, err := messages.Middleware(messages.Config{})
	if err != nil {
		t.Fatal(err)
	}
	handler := sessionMiddleware(authMiddleware(csrfMiddleware(messageMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, _ := sessions.FromContext(r.Context())
		switch r.URL.Path {
		case "/start":
			if err := session.Set("cart", "fixture-cart"); err != nil {
				t.Fatal(err)
			}
		case "/login":
			if err := auth.Login(w, r, principal(), security.CSRFConfig{}); err != nil {
				t.Fatal(err)
			}
		case "/logout":
			if err := auth.Logout(w, r, security.CSRFConfig{}); err != nil {
				t.Fatal(err)
			}
			if err := messages.Add(r, messages.Success, "Signed out"); err != nil {
				t.Fatal(err)
			}
		case "/protected":
			if err := (auth.ModelPolicy{}).Authorize(r.Context(), auth.FromContext(r.Context()), "view", auth.Resource{App: "catalog", Model: "Product"}); err != nil {
				w.WriteHeader(401)
			}
		}
		var cart string
		_, _ = session.Get("cart", &cart)
		_ = json.NewEncoder(w).Encode(map[string]any{"csrf": security.CSRFToken(r), "authenticated": auth.FromContext(r.Context()).Authenticated, "cart": cart})
	})))))
	jar := map[string]*http.Cookie{}
	call := func(method, path, token string) (*httptest.ResponseRecorder, map[string]any) {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(""))
		for _, cookie := range jar {
			r.AddCookie(cookie)
		}
		r.Header.Set("X-CSRFToken", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		for _, cookie := range w.Result().Cookies() {
			if cookie.MaxAge < 0 {
				delete(jar, cookie.Name)
			} else {
				jar[cookie.Name] = cookie
			}
		}
		var output map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &output); err != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return w, output
	}
	_, start := call("GET", "/start", "")
	oldCookie := *jar["gogo_session"]
	oldID, err := signer.Verify(oldCookie.Value, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	w, logged := call("POST", "/login", start["csrf"].(string))
	if w.Code != 200 || logged["authenticated"] != true || logged["cart"] != "fixture-cart" {
		t.Fatal(w.Code, logged)
	}
	if _, err := store.Load(ctx, string(oldID)); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("pre-login fixation identity survived", err)
	}
	loginCookie := *jar["gogo_session"]
	loginID, err := signer.Verify(loginCookie.Value, time.Hour)
	if err != nil || string(loginID) == string(oldID) {
		t.Fatal("identity did not rotate", err)
	}
	beforeLogout, err := store.Load(ctx, string(loginID))
	if err != nil {
		t.Fatal(err)
	}
	w, _ = call("POST", "/protected", logged["csrf"].(string))
	if w.Code != 200 {
		t.Fatal("post-login token or lowercase grant denied", w.Code)
	}
	w, out := call("POST", "/logout", logged["csrf"].(string))
	if w.Code != 200 || out["authenticated"] != false || out["cart"] != "" {
		t.Fatal(w.Code, out)
	}
	if _, err := store.Load(ctx, string(loginID)); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("logout retained authenticated session", err)
	}
	beforeLogout.Version++
	if err := store.Save(ctx, beforeLogout, beforeLogout.Version-1); !errors.Is(err, sessions.ErrConflict) {
		t.Fatal("stale request resurrected logged-out state", err)
	}
	if jar["gogo_session"] == nil || jar["gogo_session"].Value == loginCookie.Value {
		t.Fatal("logout notice failed to get new anonymous identity")
	}
	w, out = call("POST", "/login", out["csrf"].(string))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	version.Store(2)
	w, out = call("GET", "/protected", "")
	if w.Code != 401 || out["authenticated"] != false {
		t.Fatal("auth_version change did not invalidate session", w.Code, out)
	}
}
