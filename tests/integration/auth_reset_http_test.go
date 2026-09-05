package integration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/connectors/postgres"
	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/auth/views"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

type failingResetSessionDelete struct{ sessions.Store }

func (f failingResetSessionDelete) Delete(context.Context, string) error {
	return errors.New("private session deletion error")
}

func TestPostgresPasswordResetHTTPWorkflow(t *testing.T) {
	for _, provider := range []string{"redis", "postgres"} {
		t.Run(provider, func(t *testing.T) { runPasswordResetHTTPWorkflow(t, provider == "postgres") })
	}
}

func runPasswordResetHTTPWorkflow(t *testing.T, postgresSessions bool) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	registry := &models.Registry{}
	for _, schema := range append(append(auth.Schemas(), auth.PasswordResetSchemas()...), (&contenttypes.ContentType{}).Schema()) {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, registry)
	migrationList := append(append(contenttypes.Migrations(), auth.Migrations()...), auth.PasswordResetMigrations()...)
	if postgresSessions {
		migrationList = append(migrationList, sessions.Migrations()...)
	}
	runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: migrationList}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	accounts, err := auth.NewAccounts(auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, change auth.AccountChange) error {
		if auth.FromContext(ctx).ID == "fixture-operator" || change.Action == "reset_password" {
			return nil
		}
		return auth.ErrPermissionDenied
	}})
	if err != nil {
		t.Fatal(err)
	}
	operator := auth.WithPrincipal(ctx, auth.Principal{ID: "fixture-operator", Active: true, Authenticated: true})
	user, err := accounts.CreateUser(operator, "reset-http", "fixture original credential", auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewAuthenticator(accounts, 2)
	if err != nil {
		t.Fatal(err)
	}
	redisConfig := fixture.Start(t)
	redisConfig.Role = connector.SessionRole
	connection, err := connector.Open(ctx, redisConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var persistence sessions.Store = &connector.Sessions{Connection: connection}
	if postgresSessions {
		persistence, err = postgres.NewSessions(postgres.SessionConfig{Backend: backend})
		if err != nil {
			t.Fatal(err)
		}
	}
	secret, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(secret)}, nil, "reset-http-session")
	if err != nil {
		t.Fatal(err)
	}
	limiter := &connector.Limiter{Connection: connection}
	box := &resetMailbox{}
	service, err := auth.NewPasswordReset(auth.PasswordResetConfig{Accounts: accounts, Mail: box, From: "support@example.test", ResetURL: "https://example.test/reset", RequestTimeout: 100 * time.Millisecond, VerifiedRecipient: func(_ context.Context, p auth.Principal) (string, error) {
		if p.ID != user.ID {
			return "", auth.ErrPermissionDenied
		}
		return "verified@example.test", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	requestReset, err := views.PasswordResetRequest(views.PasswordResetRequestConfig{Service: service, NormalizeIdentifier: accounts.NormalizeLoginIdentifier, Limiter: limiter, RateSecret: []byte(secret)})
	if err != nil {
		t.Fatal(err)
	}
	confirm, err := views.PasswordResetConfirm(views.PasswordResetConfirmConfig{Service: service, Limiter: limiter, RateSecret: []byte(secret), SuccessURL: "/login"})
	if err != nil {
		t.Fatal(err)
	}
	login, err := views.Login(views.LoginConfig{Authenticator: authenticator, NormalizeIdentifier: accounts.NormalizeLoginIdentifier, Limiter: limiter, RateSecret: []byte(secret), SuccessURL: "/protected"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/reset/request", requestReset)
	mux.Handle("/reset", confirm)
	mux.Handle("/login", login)
	mux.Handle(views.PasswordResetConfirmScriptPath(), views.PasswordResetConfirmScript())
	mux.HandleFunc("/protected", func(w http.ResponseWriter, r *http.Request) {
		if !auth.FromContext(r.Context()).Authenticated {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/private", func(w http.ResponseWriter, r *http.Request) {
		s, _ := sessions.FromContext(r.Context())
		if err := s.Set("private", "old-account-data"); err != nil {
			t.Error(err)
		}
		w.WriteHeader(204)
	})
	principalMiddleware, err := auth.SessionMiddleware(accounts)
	if err != nil {
		t.Fatal(err)
	}
	headers, err := security.Headers(security.HeadersConfig{AllowedHosts: []string{"example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	wrap := func(provider sessions.Store) http.Handler {
		sessionMiddleware, err := sessions.Middleware(sessions.MiddlewareConfig{Store: provider, Signer: signer, TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return headers(sessionMiddleware(principalMiddleware(mux)))
	}
	handler := wrap(persistence)
	cookies := map[string]*http.Cookie{}
	call := func(method, path string, values url.Values, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(values.Encode()))
		r.RemoteAddr = "192.0.2.30:3333"
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		if values != nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if token != "" {
			r.Header.Set("X-CSRFToken", token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		for _, cookie := range w.Result().Cookies() {
			if cookie.MaxAge < 0 {
				delete(cookies, cookie.Name)
			} else {
				cookies[cookie.Name] = cookie
			}
		}
		return w
	}
	csrf := func(path string) string {
		t.Helper()
		w := call("GET", path, nil, "")
		match := regexp.MustCompile(`name="csrfmiddlewaretoken" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
		if w.Code != 200 || len(match) != 2 {
			t.Fatal("missing form CSRF", w.Code)
		}
		return match[1]
	}
	loginWith := func(password string) {
		t.Helper()
		token := csrf("/login")
		if w := call("POST", "/login", url.Values{"identifier": {user.Identifier}, "password": {password}}, token); w.Code != 303 {
			t.Fatal("fixture login failed", w.Code)
		}
	}
	loginWith("fixture original credential")
	if w := call("GET", "/private", nil, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	oldCookie := *cookies["gogo_session"]
	requestToken := csrf("/reset/request")
	if w := call("POST", "/reset/request", url.Values{"identifier": {user.Identifier}}, ""); w.Code != 403 || len(box.messages) != 0 {
		t.Fatal("reset request without CSRF", w.Code)
	}
	absent := call("POST", "/reset/request", url.Values{"identifier": {"absent-account"}}, requestToken)
	known := call("POST", "/reset/request", url.Values{"identifier": {user.Identifier}}, requestToken)
	if absent.Code != 200 || known.Code != 200 || absent.Body.String() != known.Body.String() || len(box.messages) != 1 {
		t.Fatal("reset existence acknowledgment differs", absent.Code, known.Code)
	}
	bearer := box.bearer(t)
	formToken := csrf("/reset")
	values := url.Values{"token": {bearer}, "new_password1": {"short"}, "new_password2": {"short"}, "user_id": {"ignored-other-account"}, "superuser": {"true"}}
	w := call("POST", "/reset", values, formToken)
	if w.Code != 400 || w.Header().Get("X-Gogo-Password-Change") != "unchanged" || !strings.Contains(w.Body.String(), bearer) {
		t.Fatal("policy rejection did not preserve retry", w.Code)
	}
	values.Set("new_password1", "  fixture changed credential  ")
	values.Set("new_password2", "  fixture changed credential  ")
	w = call("POST", "/reset", values, formToken)
	if w.Code != 303 || w.Header().Get("X-Gogo-Password-Change") != "changed" || w.Header().Get("Location") != "/login" || cookies["gogo_session"] != nil {
		t.Fatal("reset changed/login outcome", w.Code, w.Header())
	}
	if w := call("GET", "/protected", nil, ""); w.Code != 401 {
		t.Fatal("reset automatically logged in", w.Code)
	}
	cookies["gogo_session"] = &oldCookie
	if w := call("GET", "/protected", nil, ""); w.Code != 401 {
		t.Fatal("old session survived reset", w.Code)
	}
	formToken = csrf("/reset")
	if w := call("POST", "/reset", values, formToken); w.Code != 400 || w.Header().Get("X-Gogo-Password-Change") != "unchanged" {
		t.Fatal("consumed token replayed", w.Code)
	}
	loginWith("  fixture changed credential  ")
	p, err := accounts.LoadPrincipal(ctx, user.ID)
	if err != nil || p.Staff || p.Superuser || p.AuthVersion != 2 {
		t.Fatal("POST metadata changed grants", err)
	}
	// Failure to clear browser-session storage cannot undo the new password.
	requestToken = csrf("/reset/request")
	if w := call("POST", "/reset/request", url.Values{"identifier": {user.Identifier}}, requestToken); w.Code != 200 {
		t.Fatal(w.Code)
	}
	bearer = box.bearer(t)
	formToken = csrf("/reset")
	handler = wrap(failingResetSessionDelete{persistence})
	values = url.Values{"token": {bearer}, "new_password1": {"fixture final credential"}, "new_password2": {"fixture final credential"}}
	w = call("POST", "/reset", values, formToken)
	if w.Code != 503 || w.Header().Get("X-Gogo-Password-Change") != "changed" || len(w.Result().Cookies()) != 0 {
		t.Fatal("session cleanup failure claimed credential rollback", w.Code, w.Header())
	}
	handler = wrap(persistence)
	if principal, err := authenticator.Authenticate(ctx, user.Identifier, "fixture final credential"); err != nil || principal.AuthVersion != 3 {
		t.Fatal("confirmed reset was not committed", err)
	}
	if w := call("GET", "/protected", nil, ""); w.Code != 401 {
		t.Fatal("failed session cleanup retained current authority", w.Code)
	}
	// The public script never contains token/request data or issues cookies.
	w = call("GET", views.PasswordResetConfirmScriptPath(), nil, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), bearer) || len(w.Result().Cookies()) != 0 {
		t.Fatal("script response exposed request data", w.Code)
	}
}
