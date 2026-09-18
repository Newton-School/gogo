package integration_test

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
	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/admin/admindocs"
	"github.com/Newton-School/gogo/connectors/postgres"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redisfixture "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/auth"
	authviews "github.com/Newton-School/gogo/core/auth/views"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

// Each fixture owns its PostgreSQL schema and Redis instance. Product tables
// deliberately do not exist: reference pages must inspect metadata, not rows.
type documentationNativeFixture struct {
	t                            *testing.T
	ctx, operator                context.Context
	site                         *admin.Site
	accounts                     *auth.Accounts
	user                         *auth.User
	permission, modelPermission  int64
	wrap                         func(http.Handler) http.Handler
	beforeRender                 func() error
	policyFailure, loaderFailure bool
	scopeCalls                   int
}

func newDocumentationNativeFixture(t *testing.T, provider string) *documentationNativeFixture {
	t.Helper()
	f := &documentationNativeFixture{t: t, ctx: context.Background()}
	f.operator = auth.WithPrincipal(f.ctx, auth.Principal{ID: "fixture-documentation-operator", Authenticated: true, Active: true})
	backend := testservice.Postgres(t)
	registry := &models.Registry{}
	for _, schema := range append(auth.Schemas(), (&contenttypes.ContentType{}).Schema(), (&adminProduct{}).Schema()) {
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
	if err := runner.Apply(f.ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := contenttypes.Sync(f.ctx, store, registry, nil); err != nil {
		t.Fatal(err)
	}
	if err := auth.SyncPermissions(f.ctx, store, registry, nil); err != nil {
		t.Fatal(err)
	}
	// This non-model grant is explicit privileged fixture setup. Installing the
	// optional documentation handler must never create permission records.
	identity := &contenttypes.ContentType{AppLabel: "admindocs", ModelName: "documentation", SchemaVersion: 1, Active: true}
	if err := store.Save(f.ctx, identity, orm.SaveOptions{ForceInsert: true}); err != nil {
		t.Fatal(err)
	}
	permission := &auth.Permission{ContentTypeID: identity.ID, Codename: "view_documentation", Name: "Can view developer reference"}
	if err := store.Save(f.ctx, permission, orm.SaveOptions{ForceInsert: true}); err != nil {
		t.Fatal(err)
	}
	f.permission = permission.ID
	modelPermission, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).Filter(orm.Q("codename", "view_product")).Get(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.modelPermission = modelPermission.ID
	f.accounts, err = auth.NewAccounts(auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, _ auth.AccountChange) error {
		if auth.FromContext(ctx).ID != "fixture-documentation-operator" {
			return auth.ErrPermissionDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.user, err = f.accounts.CreateUser(f.operator, "documentation-staff", "  fixture documentation password  ", auth.CreateUserOptions{Staff: true})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{"shop.Product": func() models.Model { return &adminProduct{} }}, QueryScope: func(_ context.Context, p auth.Principal, _ models.Schema) (admin.QueryScope, error) {
		f.scopeCalls++
		return admin.QueryScope{Predicate: orm.Q("tenant", p.ID), Identity: p.ID}, nil
	}, ValidateWrite: func(context.Context, auth.Principal, models.Record) error {
		return auth.ErrPermissionDenied
	}})
	if err != nil {
		t.Fatal(err)
	}
	redisConfig := redisfixture.Start(t)
	redisConfig.Role = connector.SessionRole
	connection, err := connector.Open(f.ctx, redisConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
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
	signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(secret)}, nil, "documentation-native")
	if err != nil {
		t.Fatal(err)
	}
	// Reloading inside this application policy supplies current authority at
	// each render fence. Plain ModelPolicy uses the request's identity snapshot;
	// no claim of an automatic mid-request account reload is made for that type.
	policy := auth.PolicyFunc(func(ctx context.Context, p auth.Principal, action string, resource auth.Resource) error {
		if f.policyFailure {
			return errors.New("private documentation policy details")
		}
		current, err := f.accounts.LoadPrincipal(ctx, p.ID)
		if err != nil {
			return err
		}
		if current.AuthVersion != p.AuthVersion || !current.Active || !current.Staff {
			return auth.ErrPermissionDenied
		}
		return (auth.ModelPolicy{}).Authorize(ctx, current, action, resource)
	})
	f.site, err = admin.NewSite(admin.Config{Store: adapter, Policy: policy, Signer: signer, LoginURL: "/admin/login/", ActorLabel: func(_ context.Context, p auth.Principal) (string, error) {
		if hook := f.beforeRender; hook != nil {
			f.beforeRender = nil
			if err := hook(); err != nil {
				return "", err
			}
		}
		return p.ID, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	product := (&adminProduct{}).Schema()
	product.Comment = "private schema comment"
	product.Fields[3].Default = "private field default"
	if err := f.site.Register(admin.ModelAdmin{Schema: product, Fields: []string{"name"}, SensitiveFields: []string{"secret"}}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewAuthenticator(f.accounts, 2)
	if err != nil {
		t.Fatal(err)
	}
	limit := ratelimit.Limit{Rate: 100, Burst: 100, Period: time.Minute}
	login, err := f.site.LoginHandler(authviews.LoginConfig{Authenticator: authenticator, NormalizeIdentifier: f.accounts.NormalizeLoginIdentifier, Limiter: &connector.Limiter{Connection: connection}, RateSecret: []byte(secret), IdentityLimit: limit, IPLimit: limit})
	if err != nil {
		t.Fatal(err)
	}
	identityMiddleware, err := auth.SessionMiddleware(auth.PrincipalLoaderFunc(func(ctx context.Context, id string) (auth.Principal, error) {
		if f.loaderFailure {
			return auth.Principal{}, errors.New("private documentation identity details")
		}
		return f.accounts.LoadPrincipal(ctx, id)
	}))
	if err != nil {
		t.Fatal(err)
	}
	sessionMiddleware, err := sessions.Middleware(sessions.MiddlewareConfig{Store: persistence, Signer: signer, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	f.wrap = func(documentation http.Handler) http.Handler {
		mux := http.NewServeMux()
		mux.Handle("/admin/login/", login)
		if documentation != nil {
			mux.Handle("/admin/doc/", documentation)
		}
		mux.Handle("/admin/", f.site)
		return sessionMiddleware(identityMiddleware(mux))
	}
	return f
}

type documentationNativeClient struct {
	t       *testing.T
	handler http.Handler
	cookies map[string]*http.Cookie
}

func (c *documentationNativeClient) call(method, path string, values url.Values) *httptest.ResponseRecorder {
	c.t.Helper()
	request := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(values.Encode()))
	request.RemoteAddr = "192.0.2.41:3000"
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	// A conditional request must not bypass current authorization.
	if strings.HasSuffix(path, "/doc/") {
		request.Header.Set("If-None-Match", "*")
		request.Header.Set("If-Modified-Since", "Wed, 01 Jan 2100 00:00:00 GMT")
	}
	for _, cookie := range c.cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	c.handler.ServeHTTP(response, request)
	for _, cookie := range response.Result().Cookies() {
		if cookie.MaxAge < 0 {
			delete(c.cookies, cookie.Name)
		} else {
			c.cookies[cookie.Name] = cookie
		}
	}
	return response
}

func (c *documentationNativeClient) login() {
	c.t.Helper()
	form := c.call(http.MethodGet, "/admin/login/", nil)
	match := regexp.MustCompile(`name="csrfmiddlewaretoken" value="([^"]*)"`).FindStringSubmatch(form.Body.String())
	if form.Code != http.StatusOK || len(match) != 2 {
		c.t.Fatal("credential form unavailable", form.Code)
	}
	response := c.call(http.MethodPost, "/admin/login/", url.Values{"identifier": {"documentation-staff"}, "password": {"  fixture documentation password  "}, "csrfmiddlewaretoken": {match[1]}})
	if response.Code != http.StatusSeeOther || c.cookies["gogo_session"] == nil {
		c.t.Fatal("real staff authentication failed", response.Code)
	}
}

func TestAdminDocumentationNativeCurrentIdentityAndExplicitMetadata(t *testing.T) {
	for _, provider := range []string{"redis", "postgres"} {
		t.Run(provider, func(t *testing.T) {
			f := newDocumentationNativeFixture(t, provider)
			invoked := 0
			router, err := urls.New(urls.Include("/products/", "shop", urls.Path("<int:id>/", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { invoked++ }), "product", http.MethodGet)))
			if err != nil {
				t.Fatal(err)
			}
			engine := templates.New(templates.Config{
				Tags: map[string]templates.Tag{"fixture_tag": func(context.Context, templates.Context, []any) (any, error) {
					invoked++
					return "must not execute", nil
				}},
				Filters: map[string]templates.Filter{"fixture_filter": func(context.Context, any, any) (any, error) { invoked++; return "must not execute", nil }},
			})
			options := admindocs.Options{
				Models: []admin.DocumentationModel{{Key: "shop.Product", Description: "Selected product reference", Fields: []admin.DocumentationField{{Name: "name", Description: "Visible name <script>invalid()</script>"}}}},
				Router: router, Templates: engine,
				Views:   []admin.DocumentationView{{Route: "shop:product", Title: "Product route reference", Description: "Explicit application view description"}},
				Tags:    []admin.DocumentationExtension{{Name: "fixture_tag", Description: "Declared tag help"}},
				Filters: []admin.DocumentationExtension{{Name: "fixture_filter", Description: "Declared filter help"}},
			}
			documentation, err := admindocs.New(f.site, options)
			if err != nil {
				t.Fatal(err)
			}
			// Subsequent edits to the input and even replacement of the public
			// router value cannot retarget the immutable documentation inventory.
			options.Models[0].Description = "mutated private description"
			options.Models[0].Fields[0].Name = "secret"
			options.Views[0].Title = "mutated private view"
			*router = urls.Router{}
			client := &documentationNativeClient{t: t, handler: f.wrap(documentation), cookies: map[string]*http.Cookie{}}
			assertDenied := func(response *httptest.ResponseRecorder) {
				t.Helper()
				if response.Code != http.StatusForbidden && response.Code != http.StatusNotFound {
					t.Fatal("reference denial must not redirect or disclose", response.Code)
				}
				for _, private := range []string{"Selected product reference", "Product route reference", "fixture_tag", "shop.Product"} {
					if strings.Contains(response.Body.String(), private) {
						t.Fatal("denial disclosed metadata", private)
					}
				}
			}
			assertDenied(client.call(http.MethodGet, "/admin/doc/", nil))
			client.login()
			assertDenied(client.call(http.MethodGet, "/admin/doc/", nil)) // Staff alone is insufficient.
			if err := f.accounts.SetUserPermissions(f.operator, f.user.ID, []int64{f.permission}); err != nil {
				t.Fatal(err)
			}
			assertDenied(client.call(http.MethodGet, "/admin/doc/", nil))
			if client.cookies["gogo_session"] != nil {
				t.Fatal("old grant-version session survived invalidation")
			}
			client.login()
			response := client.call(http.MethodGet, "/admin/doc/", nil)
			if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "Selected product reference") || !strings.Contains(response.Body.String(), "Product route reference") {
				t.Fatal("documentation grant did not preserve model-level filtering", response.Code)
			}
			if err := f.accounts.SetUserPermissions(f.operator, f.user.ID, []int64{f.permission, f.modelPermission}); err != nil {
				t.Fatal(err)
			}
			assertDenied(client.call(http.MethodGet, "/admin/doc/", nil))
			client.login()
			response = client.call(http.MethodGet, "/admin/doc/", nil)
			if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") || response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("authorized reference or private headers unavailable", response.Code, response.Header())
			}
			for _, required := range []string{"Selected product reference", "Product route reference", "Declared tag help", "Declared filter help", "&lt;script&gt;invalid()&lt;/script&gt;"} {
				if !strings.Contains(response.Body.String(), required) {
					t.Fatal("missing escaped frozen public descriptor", required)
				}
			}
			for _, private := range []string{"<script>invalid()", "private schema comment", "private field default", "mutated private"} {
				if strings.Contains(response.Body.String(), private) {
					t.Fatal("private or executable descriptor escaped projection", private)
				}
			}
			if invoked != 0 || f.scopeCalls != 0 {
				t.Fatal("documentation invoked a handler/tag/filter or model store", invoked, f.scopeCalls)
			}
			head := client.call(http.MethodHead, "/admin/doc/", nil)
			if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Type") != response.Header().Get("Content-Type") {
				t.Fatal("HEAD did not preserve authorized headers without a body", head.Code)
			}
			unsafe := client.call(http.MethodPost, "/admin/doc/", nil)
			if unsafe.Code != http.StatusMethodNotAllowed && unsafe.Code != http.StatusForbidden {
				t.Fatal("documentation accepted unsafe method", unsafe.Code)
			}
			f.policyFailure = true
			response = client.call(http.MethodGet, "/admin/doc/", nil)
			f.policyFailure = false
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private documentation") || strings.Contains(response.Body.String(), "Selected product reference") {
				t.Fatal("policy outage disclosed a reference or provider details", response.Code)
			}
			f.loaderFailure = true
			response = client.call(http.MethodGet, "/admin/doc/", nil)
			f.loaderFailure = false
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private documentation") || strings.Contains(response.Body.String(), "Product route reference") {
				t.Fatal("identity outage disclosed a reference or provider details", response.Code)
			}
			// A real account/grant write during the label callback must be
			// caught by the final application-policy fence before any page bytes.
			f.beforeRender = func() error { return f.accounts.SetUserPermissions(f.operator, f.user.ID, []int64{f.permission}) }
			assertDenied(client.call(http.MethodGet, "/admin/doc/", nil))
			assertDenied(client.call(http.MethodGet, "/admin/doc/", nil))
			if client.cookies["gogo_session"] != nil {
				t.Fatal("render-time revoked session persisted on the next request")
			}
			client.login()
			if err := f.accounts.SetAccountFlags(f.operator, f.user.ID, true, false, false); err != nil {
				t.Fatal(err)
			}
			assertDenied(client.call(http.MethodGet, "/admin/doc/", nil))
			if client.cookies["gogo_session"] != nil {
				t.Fatal("staff revocation did not flush persisted session")
			}
			if err := f.accounts.SetAccountFlags(f.operator, f.user.ID, true, true, false); err != nil {
				t.Fatal(err)
			}
			client.login()
			client.handler = f.wrap(nil)
			response = client.call(http.MethodGet, "/admin/doc/", nil)
			if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "Product route reference") {
				t.Fatal("omitting optional documentation route did not disable it", response.Code)
			}
			if invoked != 0 || f.scopeCalls != 0 {
				t.Fatal("reference workflow read product rows or invoked documented code", invoked, f.scopeCalls)
			}
		})
	}
}
