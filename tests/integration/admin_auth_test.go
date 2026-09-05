package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/admin"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redisfixture "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/auth"
	authviews "github.com/Newton-School/gogo/core/auth/views"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/ratelimit"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

func TestAdminPostgresRedisStaffCredentialWorkflow(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	registry := &models.Registry{}
	for _, schema := range append(auth.Schemas(), (&contenttypes.ContentType{}).Schema(), (&adminProduct{}).Schema(), admin.LogSchema()) {
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
	for _, schema := range []models.Schema{(&adminProduct{}).Schema(), admin.LogSchema()} {
		if err := backend.SchemaEditor().CreateModel(ctx, backend, schema); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := contenttypes.Sync(ctx, store, registry, nil); err != nil {
		t.Fatal(err)
	}
	if err := auth.SyncPermissions(ctx, store, registry, nil); err != nil {
		t.Fatal(err)
	}
	operatorCtx := auth.WithPrincipal(ctx, auth.Principal{ID: "fixture-operator", Authenticated: true, Active: true})
	accounts, err := auth.NewAccounts(auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, _ auth.AccountChange) error {
		if auth.FromContext(ctx).ID != "fixture-operator" {
			return auth.ErrPermissionDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	password := "  fixture staff password  "
	staff, err := accounts.CreateUser(operatorCtx, "staff-reviewer", password, auth.CreateUserOptions{Staff: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.CreateUser(operatorCtx, "ordinary-account", password, auth.CreateUserOptions{}); err != nil {
		t.Fatal(err)
	}
	grants, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).Filter(orm.Q("codename__in", []string{"view_product", "change_product"})).All(ctx)
	if err != nil || len(grants) != 2 {
		t.Fatal(grants, err)
	}
	grantIDs := []int64{grants[0].ID, grants[1].ID}
	if err := accounts.SetUserPermissions(operatorCtx, staff.ID, grantIDs); err != nil {
		t.Fatal(err)
	}
	product := &adminProduct{Tenant: staff.ID, Name: "Staff-owned product", Secret: "server-owned"}
	for _, row := range []*adminProduct{product, {Tenant: "another-workspace", Name: "Unrelated private product", Secret: "hidden"}} {
		if err := store.Save(ctx, row, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{"shop.Product": func() models.Model { return &adminProduct{} }}, QueryScope: func(_ context.Context, p auth.Principal, _ models.Schema) (admin.QueryScope, error) {
		return admin.QueryScope{Predicate: orm.Q("tenant", p.ID), Identity: p.ID}, nil
	}, ValidateWrite: func(_ context.Context, p auth.Principal, row models.Record) error {
		tenant, _ := row.Get("tenant")
		if tenant != p.ID {
			return auth.ErrPermissionDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	redisConfig := redisfixture.Start(t)
	redisConfig.Role = connector.SessionRole
	connection, err := connector.Open(ctx, redisConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	key, _ := security.RandomToken(32)
	signer, _ := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "admin-credential-integration")
	site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.ModelPolicy{}, LoginURL: "/admin/login/", LogoutURL: "/admin/logout/"})
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Register(admin.ModelAdmin{Schema: product.Schema(), Fields: []string{"name", "secret"}, ReadonlyFields: []string{"secret"}, ListDisplay: []string{"name"}, ConstraintChecker: store}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewAuthenticator(accounts, 2)
	if err != nil {
		t.Fatal(err)
	}
	login, err := site.LoginHandler(authviews.LoginConfig{Authenticator: authenticator, NormalizeIdentifier: accounts.NormalizeLoginIdentifier, Limiter: &connector.Limiter{Connection: connection}, RateSecret: []byte(key), IdentityLimit: ratelimit.Limit{Rate: 50, Burst: 50, Period: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	logout, err := site.LogoutHandler(authviews.LogoutConfig{})
	if err != nil {
		t.Fatal(err)
	}
	sessionMiddleware, err := sessions.Middleware(sessions.MiddlewareConfig{Store: &connector.Sessions{Connection: connection}, Signer: signer, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	identityMiddleware, err := auth.SessionMiddleware(accounts)
	if err != nil {
		t.Fatal(err)
	}
	headers, err := security.Headers(security.HeadersConfig{AllowedHosts: []string{"example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/admin/login/", login)
	mux.Handle("/admin/logout/", logout)
	mux.Handle("/admin/", site)
	handler := headers(sessionMiddleware(identityMiddleware(mux)))
	cookies := map[string]*http.Cookie{}
	call := func(method, path string, values url.Values) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(values.Encode()))
		request.RemoteAddr = "192.0.2.50:3000"
		if method == "POST" {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		for _, cookie := range response.Result().Cookies() {
			if cookie.MaxAge < 0 {
				delete(cookies, cookie.Name)
			} else {
				cookies[cookie.Name] = cookie
			}
		}
		return response
	}
	hidden := func(body, name string) string {
		t.Helper()
		match := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(body)
		if len(match) != 2 {
			t.Fatalf("missing %s", name)
		}
		return match[1]
	}
	signIn := func(identifier string) *httptest.ResponseRecorder {
		t.Helper()
		form := call("GET", "/admin/login/", nil)
		if form.Code != 200 || strings.Contains(form.Body.String(), "Model navigation") || strings.Contains(form.Body.String(), "Product") {
			t.Fatal("anonymous login leaked navigation", form.Code, form.Body.String())
		}
		return call("POST", "/admin/login/", url.Values{"identifier": {identifier}, "password": {password}, "next": {"//attacker.test"}, "csrfmiddlewaretoken": {hidden(form.Body.String(), "csrfmiddlewaretoken")}})
	}
	if response := call("GET", "/admin/shop/product/", nil); response.Code != 303 || !strings.HasPrefix(response.Header().Get("Location"), "/admin/login/?next=") {
		t.Fatal("anonymous request did not redirect safely", response.Code, response.Header())
	}
	if response := signIn("ordinary-account"); response.Code != 401 || cookies["gogo_session"] != nil || strings.Contains(response.Body.String(), password) {
		t.Fatal("non-staff received Admin identity or password echo", response.Code)
	}
	if response := signIn("staff-reviewer"); response.Code != 303 || response.Header().Get("Location") != "/admin/" || cookies["gogo_session"] == nil {
		t.Fatal("staff sign-in failed", response.Code, response.Header())
	}
	list := call("GET", "/admin/shop/product/", nil)
	if list.Code != 200 || !strings.Contains(list.Body.String(), product.Name) || strings.Contains(list.Body.String(), "Unrelated private product") || !strings.Contains(list.Body.String(), `action="/admin/logout/"`) || strings.Contains(list.Body.String(), `href="/admin/shop/product/add/"`) {
		t.Fatal("staff grants/scope/logout UI mismatch", list.Code, list.Body.String())
	}
	link := regexp.MustCompile(`href="(/admin/shop/product/[^"]+/change/)"`).FindStringSubmatch(list.Body.String())
	if len(link) != 2 {
		t.Fatal("missing staff-authorized edit link")
	}
	form := call("GET", link[1], nil)
	response := call("POST", link[1], url.Values{"name": {"Edited after real login"}, "secret": {"forged"}, "_edit_token": {hidden(form.Body.String(), "_edit_token")}, "csrfmiddlewaretoken": {hidden(form.Body.String(), "csrfmiddlewaretoken")}})
	if response.Code != 303 {
		t.Fatal("authenticated edit failed", response.Code, response.Body.String())
	}
	fresh, err := orm.For(store, func() *adminProduct { return &adminProduct{} }).Filter(orm.Q("id", product.ID)).Get(ctx)
	if err != nil || fresh.Name != "Edited after real login" || fresh.Secret != "server-owned" {
		t.Fatal("credential edit did not preserve readonly field", fresh, err)
	}
	var auditActor string
	if err := db.QueryRow(ctx, backend, "SELECT actor_id FROM gogo_admin_log ORDER BY occurred_at DESC LIMIT 1", nil, &auditActor); err != nil || auditActor != staff.ID {
		t.Fatal("audit actor did not come from persisted session", auditActor, err)
	}
	if err := accounts.SetUserPermissions(operatorCtx, staff.ID, nil); err != nil {
		t.Fatal(err)
	}
	if response := call("GET", "/admin/shop/product/", nil); response.Code != 303 || cookies["gogo_session"] != nil {
		t.Fatal("revoked grant version left session authenticated", response.Code)
	}
	if response := signIn("staff-reviewer"); response.Code != 303 {
		t.Fatal(response.Code)
	}
	if response := call("GET", "/admin/shop/product/", nil); response.Code != 403 {
		t.Fatal("staff flag bypassed missing model permission", response.Code)
	}
	if response := call("GET", "/admin/", nil); response.Code != 200 || strings.Contains(response.Body.String(), "Products") {
		t.Fatal("ungranted model disclosed in dashboard", response.Code)
	}
	if err := accounts.SetAccountFlags(operatorCtx, staff.ID, true, false, false); err != nil {
		t.Fatal(err)
	}
	if response := call("GET", "/admin/", nil); response.Code != 303 {
		t.Fatal("staff revocation did not invalidate session", response.Code)
	}
	if response := signIn("staff-reviewer"); response.Code != 401 || cookies["gogo_session"] != nil {
		t.Fatal("revoked staff user could sign in again", response.Code)
	}
	if err := accounts.SetAccountFlags(operatorCtx, staff.ID, true, true, false); err != nil {
		t.Fatal(err)
	}
	if response := signIn("staff-reviewer"); response.Code != 303 {
		t.Fatal(response.Code)
	}
	oldCookie := *cookies["gogo_session"]
	if response := call("GET", "/admin/logout/", nil); response.Code != 405 {
		t.Fatal("GET logout mutated identity", response.Code)
	}
	if response := call("POST", "/admin/logout/", nil); response.Code != 403 {
		t.Fatal("logout bypassed CSRF", response.Code)
	}
	page := call("GET", "/admin/", nil)
	response = call("POST", "/admin/logout/", url.Values{"csrfmiddlewaretoken": {hidden(page.Body.String(), "csrfmiddlewaretoken")}})
	if response.Code != 303 || response.Header().Get("Location") != "/admin/login/" || cookies["gogo_session"] != nil {
		t.Fatal("logout did not revoke session", response.Code, response.Header())
	}
	cookies["gogo_session"] = &oldCookie
	if response := call("GET", "/admin/", nil); response.Code != 303 || cookies["gogo_session"] != nil {
		t.Fatal("replayed logged-out session recovered Admin access", response.Code)
	}
}
