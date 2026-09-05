package integration

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

func TestPostgresPasswordChangeHTTPBothSessionProviders(t *testing.T) {
	for _, provider := range []string{"redis", "postgres"} {
		t.Run(provider, func(t *testing.T) { runPasswordChangeHTTP(t, provider) })
	}
}

func runPasswordChangeHTTP(t *testing.T, provider string) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	registry := &models.Registry{}
	for _, schema := range append(auth.Schemas(), (&contenttypes.ContentType{}).Schema()) {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, registry)
	runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: append(contenttypes.Migrations(), auth.Migrations()...)}
	if provider == "postgres" {
		runner.Migrations = append(runner.Migrations, sessions.Migrations()...)
	}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	deny := false
	accounts, err := auth.NewAccounts(auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, change auth.AccountChange) error {
		p := auth.FromContext(ctx)
		if p.ID == "fixture-operator" || !deny && p.Authenticated && p.Active && change.Action == "change_own_password" && change.UserID == p.ID {
			return nil
		}
		return auth.ErrPermissionDenied
	}})
	if err != nil {
		t.Fatal(err)
	}
	operator := auth.WithPrincipal(ctx, auth.Principal{ID: "fixture-operator", Active: true, Authenticated: true})
	const first = "  fixture first password  "
	const second = "  fixture second password  "
	const third = "  fixture third password  "
	const fourth = "  fixture fourth password  "
	const fifth = "  fixture fifth password  "
	user, err := accounts.CreateUser(operator, "password-http", first, auth.CreateUserOptions{})
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
	if provider == "postgres" {
		persistence, err = postgres.NewSessions(postgres.SessionConfig{Backend: backend})
		if err != nil {
			t.Fatal(err)
		}
	}
	secret, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(secret)}, nil, "password-change-http")
	if err != nil {
		t.Fatal(err)
	}
	limit := ratelimit.Limit{Rate: 100, Burst: 100, Period: time.Minute}
	limiter := &connector.Limiter{Connection: connection}
	login, err := views.Login(views.LoginConfig{Authenticator: authenticator, NormalizeIdentifier: accounts.NormalizeLoginIdentifier, Limiter: limiter, RateSecret: []byte(secret), IdentityLimit: limit, IPLimit: limit, SuccessURL: "/protected"})
	if err != nil {
		t.Fatal(err)
	}
	changeConfig := views.PasswordChangeConfig{Changer: accounts, Limiter: limiter, RateSecret: []byte(secret), AccountLimit: limit, IPLimit: limit, PreserveSession: true, SuccessURL: "/protected", LoginURL: "/login"}
	change, err := views.PasswordChange(changeConfig)
	if err != nil {
		t.Fatal(err)
	}
	changeConfig.PreserveSession = false
	logoutChange, err := views.PasswordChange(changeConfig)
	if err != nil {
		t.Fatal(err)
	}
	authMiddleware, err := auth.SessionMiddleware(accounts)
	if err != nil {
		t.Fatal(err)
	}
	headers, err := security.Headers(security.HeadersConfig{AllowedHosts: []string{"example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/login", login)
	mux.Handle("/change", change)
	mux.Handle("/change-default", logoutChange)
	mux.HandleFunc("/protected", func(w http.ResponseWriter, r *http.Request) {
		p := auth.FromContext(r.Context())
		if !p.Authenticated || !p.Active || p.ID != user.ID {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	wrap := func(persistence sessions.Store) http.Handler {
		middleware, err := sessions.Middleware(sessions.MiddlewareConfig{Store: persistence, Signer: signer, TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return headers(middleware(authMiddleware(mux)))
	}
	handler := wrap(persistence)
	call := func(cookies map[string]*http.Cookie, method, path string, data url.Values, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(data.Encode()))
		r.RemoteAddr = "192.0.2.30:3210"
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		if data != nil {
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
	pattern := regexp.MustCompile(`name="csrfmiddlewaretoken" value="([^"]+)"`)
	token := func(cookies map[string]*http.Cookie, path string) string {
		w := call(cookies, http.MethodGet, path, nil, "")
		matches := pattern.FindStringSubmatch(w.Body.String())
		if w.Code != http.StatusOK || len(matches) != 2 {
			t.Fatal("account form unavailable", w.Code)
		}
		return matches[1]
	}
	loginAs := func(cookies map[string]*http.Cookie, password string) {
		csrf := token(cookies, "/login")
		w := call(cookies, http.MethodPost, "/login", url.Values{"identifier": {user.Identifier}, "password": {password}}, csrf)
		if w.Code != http.StatusSeeOther || cookies["gogo_session"] == nil {
			t.Fatal("fixture login failed", w.Code)
		}
	}
	sessionID := func(cookies map[string]*http.Cookie) string {
		cookie := cookies["gogo_session"]
		if cookie == nil {
			t.Fatal("expected session cookie")
		}
		value, err := signer.Verify(cookie.Value, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return string(value)
	}
	passwordForm := func(old, replacement string) url.Values {
		return url.Values{"old_password": {old}, "new_password1": {replacement}, "new_password2": {replacement}}
	}
	currentAccount := func() *auth.User {
		current, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", user.ID)).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	firstClient, otherClient := map[string]*http.Cookie{}, map[string]*http.Cookie{}
	loginAs(firstClient, first)
	loginAs(otherClient, first)
	oldID, otherID := sessionID(firstClient), sessionID(otherClient)
	oldCookie := *firstClient["gogo_session"]
	record, err := persistence.Load(ctx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	opaque := json.RawMessage(`{"integer":9007199254740993,"cart":["retained",null]}`)
	record.Data["private_data"] = opaque
	previousVersion := record.Version
	record.Version++
	if err := persistence.Save(ctx, record, previousVersion); err != nil {
		t.Fatal(err)
	}
	initial := currentAccount()
	csrf := token(firstClient, "/change")
	if w := call(firstClient, http.MethodHead, "/change", nil, ""); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatal("password form HEAD returned a body", w.Code)
	}
	if w := call(firstClient, http.MethodPut, "/change", passwordForm(first, second), csrf); w.Code != http.StatusMethodNotAllowed {
		t.Fatal("unsupported password change method accepted", w.Code)
	}
	anonymous := map[string]*http.Cookie{}
	anonymousCSRF := token(anonymous, "/login")
	if w := call(anonymous, http.MethodGet, "/change", nil, ""); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatal("anonymous password form did not require login", w.Code)
	}
	if w := call(anonymous, http.MethodPost, "/change", passwordForm(first, second), anonymousCSRF); w.Code != http.StatusUnauthorized || w.Header().Get("X-Gogo-Password-Change") != "unchanged" {
		t.Fatal("anonymous password mutation accepted", w.Code)
	}
	for _, test := range []struct {
		name   string
		form   url.Values
		csrf   string
		deny   bool
		status int
	}{
		{"missing_csrf", passwordForm(first, second), "", false, http.StatusForbidden},
		{"wrong_password", passwordForm("wrong password", second), csrf, false, http.StatusBadRequest},
		{"whitespace_significant", passwordForm(strings.TrimSpace(first), second), csrf, false, http.StatusBadRequest},
		{"password_policy", passwordForm(first, "short"), csrf, false, http.StatusBadRequest},
		{"permission_denied", passwordForm(first, second), csrf, true, http.StatusForbidden},
		{"duplicate_input", url.Values{"old_password": {first, first}, "new_password1": {second}, "new_password2": {second}}, csrf, false, http.StatusBadRequest},
		{"mismatch", url.Values{"old_password": {first}, "new_password1": {second}, "new_password2": {third}}, csrf, false, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			deny = test.deny
			w := call(firstClient, http.MethodPost, "/change", test.form, test.csrf)
			deny = false
			current := currentAccount()
			if w.Code != test.status || current.AuthVersion != initial.AuthVersion || *current.PasswordHash != *initial.PasswordHash || sessionID(firstClient) != oldID {
				t.Fatal("rejected password change mutated credentials or session", w.Code)
			}
			if test.csrf != "" && w.Header().Get("X-Gogo-Password-Change") != "unchanged" {
				t.Fatal("rejection outcome absent", w.Header())
			}
			if strings.Contains(w.Body.String(), first) || strings.Contains(w.Body.String(), second) {
				t.Fatal("response exposed submitted password")
			}
		})
	}
	oldCSRF := firstClient["gogo_csrf"].Value
	w := call(firstClient, http.MethodPost, "/change", passwordForm(first, second), csrf)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/protected" || w.Header().Get("X-Gogo-Password-Change") != "changed" || sessionID(firstClient) == oldID || firstClient["gogo_csrf"].Value == oldCSRF {
		t.Fatal("preserving password transition failed", w.Code, w.Header())
	}
	if _, err := persistence.Load(ctx, oldID); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("old session key survived rotation", err)
	}
	record, err = persistence.Load(ctx, sessionID(firstClient))
	if err != nil || !bytes.Equal(record.Data["private_data"], opaque) {
		t.Fatal("session data changed on refresh", err)
	}
	var identity struct {
		ID          string `json:"id"`
		AuthVersion uint64 `json:"auth_version"`
	}
	if json.Unmarshal(record.Data["_gogo_auth"], &identity) != nil || identity.ID != user.ID || identity.AuthVersion != 2 {
		t.Fatal("session did not record the committed transition")
	}
	if w := call(firstClient, http.MethodPost, "/change", passwordForm(second, third), csrf); w.Code != http.StatusForbidden || currentAccount().AuthVersion != 2 {
		t.Fatal("pre-change CSRF token survived rotation", w.Code)
	}
	if w := call(firstClient, http.MethodGet, "/protected", nil, ""); w.Code != http.StatusNoContent {
		t.Fatal("preserved session did not authenticate", w.Code)
	}
	if w := call(otherClient, http.MethodGet, "/protected", nil, ""); w.Code != http.StatusUnauthorized || otherClient["gogo_session"] != nil {
		t.Fatal("another session survived credential change", w.Code)
	}
	if _, err := persistence.Load(ctx, otherID); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("invalidated second session not flushed", err)
	}
	replay := map[string]*http.Cookie{"gogo_session": &oldCookie}
	if w := call(replay, http.MethodGet, "/protected", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatal("revoked cookie replay authenticated", w.Code)
	}
	// Without explicit preservation, even a successful self change logs out.
	csrf = token(firstClient, "/change-default")
	previousID := sessionID(firstClient)
	w = call(firstClient, http.MethodPost, "/change-default", passwordForm(second, third), csrf)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" || w.Header().Get("X-Gogo-Password-Change") != "changed" || firstClient["gogo_session"] != nil {
		t.Fatal("default policy preserved authentication", w.Code)
	}
	if _, err := persistence.Load(ctx, previousID); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("default logout retained session", err)
	}
	// The password owns a DB commit, then session rotation may fail. Never
	// report rollback, publish a replacement cookie, or accept the old key.
	loginAs(firstClient, third)
	csrf = token(firstClient, "/change")
	previousID = sessionID(firstClient)
	handler = wrap(failingSessionWrites{persistence})
	w = call(firstClient, http.MethodPost, "/change", passwordForm(third, fourth), csrf)
	handler = wrap(persistence)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("X-Gogo-Password-Change") != "changed" || len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), "private persistence") {
		t.Fatal("session failure hid committed password outcome or published cookies", w.Code, w.Header())
	}
	if currentAccount().AuthVersion != 4 {
		t.Fatal("session failure rolled back credential change")
	}
	if _, err := persistence.Load(ctx, previousID); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("failed replacement preserved old session", err)
	}
	if w := call(firstClient, http.MethodGet, "/protected", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatal("session-write failure left an authenticated cookie", w.Code)
	}
	loginAs(firstClient, fourth)
	// Simulate an actual committed DB transaction whose acknowledgement was
	// lost. The HTTP outcome must stay unknown and require a fresh login.
	csrf = token(firstClient, "/change")
	previousID = sessionID(firstClient)
	store.Backend = unknownAccountCommitBackend{backend}
	w = call(firstClient, http.MethodPost, "/change", passwordForm(fourth, fifth), csrf)
	store.Backend = backend
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("X-Gogo-Password-Change") != "unknown" || firstClient["gogo_session"] != nil || currentAccount().AuthVersion != 5 {
		t.Fatal("unknown commit outcome promoted a session or claimed rollback", w.Code, w.Header())
	}
	if _, err := persistence.Load(ctx, previousID); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("unknown transition did not revoke current session", err)
	}
	loginAs(firstClient, fifth)
}
