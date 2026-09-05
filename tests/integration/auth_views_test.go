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

type failingSessionWrites struct{ sessions.Store }

func (f failingSessionWrites) Create(context.Context, sessions.Record) error {
	return errors.New("private persistence failure")
}
func (f failingSessionWrites) Save(context.Context, sessions.Record, uint64) error {
	return errors.New("private persistence failure")
}

func TestPostgresRedisCredentialHTTPWorkflow(t *testing.T) {
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
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	accounts, err := auth.NewAccounts(auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, _ auth.AccountChange) error {
		if auth.FromContext(ctx).ID != "fixture-operator" {
			return auth.ErrPermissionDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	operatorCtx := auth.WithPrincipal(ctx, auth.Principal{ID: "fixture-operator", Authenticated: true, Active: true})
	user, err := accounts.CreateUser(operatorCtx, "fixture-login", "  fixture login password  ", auth.CreateUserOptions{Staff: true})
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
	sessionStore := &connector.Sessions{Connection: connection}
	secret, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(secret)}, nil, "auth-view-integration")
	if err != nil {
		t.Fatal(err)
	}
	loginConfig := views.LoginConfig{Authenticator: authenticator, NormalizeIdentifier: accounts.NormalizeLoginIdentifier, Limiter: &connector.Limiter{Connection: connection}, RateSecret: []byte(secret), IdentityLimit: ratelimit.Limit{Rate: 100, Burst: 100, Period: time.Minute}, IPLimit: ratelimit.Limit{Rate: 1000, Burst: 1000, Period: time.Minute}, SuccessURL: "/protected"}
	login, err := views.Login(loginConfig)
	if err != nil {
		t.Fatal(err)
	}
	logout, err := views.Logout(views.LogoutConfig{SuccessURL: "/login"})
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
	mux.Handle("/logout", logout)
	mux.HandleFunc("/protected", func(w http.ResponseWriter, r *http.Request) {
		p := auth.FromContext(r.Context())
		if !p.Authenticated || p.ID != user.ID || !p.Staff {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/cart", func(w http.ResponseWriter, r *http.Request) {
		s, _ := sessions.FromContext(r.Context())
		if err := s.Set("cart", "anonymous-cart"); err != nil {
			t.Error(err)
		}
		w.WriteHeader(204)
	})
	wrap := func(persistence sessions.Store) http.Handler {
		middleware, err := sessions.Middleware(sessions.MiddlewareConfig{Store: persistence, Signer: signer, TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return headers(middleware(authMiddleware(mux)))
	}
	handler := wrap(sessionStore)
	cookies := map[string]*http.Cookie{}
	call := func(method, path string, data url.Values, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(data.Encode()))
		r.RemoteAddr = "192.0.2.20:3210"
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		if data != nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if csrf != "" {
			r.Header.Set("X-CSRFToken", csrf)
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
	token := func() string {
		w := call("GET", "/login", nil, "")
		matches := pattern.FindStringSubmatch(w.Body.String())
		if w.Code != 200 || len(matches) != 2 {
			t.Fatal("login form", w.Code, w.Body.String())
		}
		return matches[1]
	}
	if w := call("GET", "/cart", nil, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	oldCookie := *cookies["gogo_session"]
	oldID, err := signer.Verify(oldCookie.Value, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	csrf := token()
	valid := url.Values{"identifier": {" fixture-login "}, "password": {"  fixture login password  "}, "next": {"https://attacker.test"}}
	if w := call("POST", "/login", valid, ""); w.Code != 403 {
		t.Fatal("CSRF missing", w.Code)
	}
	wrong := url.Values{"identifier": {"fixture-login"}, "password": {"wrong password"}}
	w := call("POST", "/login", wrong, csrf)
	if w.Code != 401 || cookies["gogo_session"].Value != oldCookie.Value {
		t.Fatal("invalid credentials changed session", w.Code)
	}
	w = call("POST", "/login", valid, csrf)
	if w.Code != 303 || w.Header().Get("Location") != "/protected" || cookies["gogo_session"].Value == oldCookie.Value {
		t.Fatal("valid login failed", w.Code, w.Body.String())
	}
	if _, err := sessionStore.Load(ctx, string(oldID)); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("fixed anonymous identity survived login", err)
	}
	currentCookie := *cookies["gogo_session"]
	currentID, err := signer.Verify(currentCookie.Value, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	record, err := sessionStore.Load(ctx, string(currentID))
	if err != nil || string(record.Data["cart"]) != `"anonymous-cart"` {
		t.Fatal("anonymous cart was not retained", err)
	}
	if w := call("GET", "/protected", nil, ""); w.Code != 204 {
		t.Fatal("persisted account session not authorized", w.Code)
	}
	if w := call("GET", "/logout", nil, ""); w.Code != 405 {
		t.Fatal("GET logout allowed", w.Code)
	}
	if w := call("GET", "/protected", nil, ""); w.Code != 204 {
		t.Fatal("GET logout changed session")
	}
	if err := accounts.SetAccountFlags(operatorCtx, user.ID, true, false, false); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/protected", nil, ""); w.Code != 401 {
		t.Fatal("account privilege revocation did not invalidate session", w.Code)
	}
	if _, err := sessionStore.Load(ctx, string(currentID)); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal("version-invalid session not flushed", err)
	}
	if err := accounts.SetAccountFlags(operatorCtx, user.ID, true, true, false); err != nil {
		t.Fatal(err)
	}
	csrf = token()
	w = call("POST", "/login", valid, csrf)
	if w.Code != 303 {
		t.Fatal(w.Code)
	}
	csrf = token()
	w = call("POST", "/logout", nil, csrf)
	if w.Code != 303 || cookies["gogo_session"] != nil {
		t.Fatal("logout cookie not expired", w.Code)
	}
	if w := call("GET", "/protected", nil, ""); w.Code != 401 {
		t.Fatal("logout did not clear identity")
	}
	// The session writer owns final response commit. A successful password
	// check cannot publish an authenticated cookie after provider write failure.
	csrf = token()
	handler = wrap(failingSessionWrites{sessionStore})
	w = call("POST", "/login", valid, csrf)
	if w.Code != 503 || len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), "private persistence") {
		t.Fatal("failed session write published identity", w.Code, w.Header())
	}
	handler = wrap(sessionStore)
	// A one-attempt identity budget also binds equivalent trimmed spellings.
	loginConfig.RateSecret = []byte(secret + "-strict")
	loginConfig.IdentityLimit = ratelimit.Limit{Rate: 1, Burst: 1, Period: time.Hour}
	strict, err := views.Login(loginConfig)
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/strict-login", strict)
	csrf = token()
	if w := call("POST", "/strict-login", wrong, csrf); w.Code != 401 {
		t.Fatal(w.Code)
	}
	wrong.Set("identifier", "  fixture-login  ")
	w = call("POST", "/strict-login", wrong, csrf)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("equivalent identity bypassed rate limit", w.Code)
	}
}
