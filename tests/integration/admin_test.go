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

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

type adminProduct struct {
	models.Base
	ID                   int64
	Tenant, Name, Secret string
}

func (*adminProduct) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "Product", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.CharField("tenant", models.WithStructField("Tenant"), models.WithMaxLength(128), models.ReadOnly), models.CharField("name", models.WithStructField("Name"), models.WithMaxLength(100)), models.CharField("secret", models.WithStructField("Secret"), models.WithMaxLength(100))}}
}
func TestAdminPostgresCRUDScopeAndAtomicAudit(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx := context.Background()
	for _, schema := range []models.Schema{(&adminProduct{}).Schema(), admin.LogSchema()} {
		if err := backend.SchemaEditor().CreateModel(ctx, backend, schema); err != nil {
			t.Fatal(err)
		}
	}
	store := orm.New(backend, nil)
	adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{"shop.Product": func() models.Model { return &adminProduct{} }}, QueryScope: func(_ context.Context, p auth.Principal, _ models.Schema) (admin.QueryScope, error) {
		return admin.QueryScope{Predicate: orm.Q("tenant", p.ID), Identity: p.ID}, nil
	}, ValidateWrite: func(_ context.Context, p auth.Principal, r models.Record) error {
		tenant, err := r.Get("tenant")
		if err != nil || tenant != p.ID {
			return auth.ErrPermissionDenied
		}
		return nil
	}, Initialize: func(_ context.Context, p auth.Principal, r models.Record) error {
		if err := r.Set("tenant", p.ID); err != nil {
			return err
		}
		return r.Set("secret", "server-owned")
	}})
	if err != nil {
		t.Fatal(err)
	}
	key, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "admin-integration")
	if err != nil {
		t.Fatal(err)
	}
	site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.ModelPolicy{AllowSuperuser: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err = site.Register(admin.ModelAdmin{Schema: (&adminProduct{}).Schema(), Fields: []string{"name", "secret"}, ReadonlyFields: []string{"secret"}, ListDisplay: []string{"id", "name"}, ConstraintChecker: store}); err != nil {
		t.Fatal(err)
	}
	p := auth.Principal{ID: "tenant-one", Authenticated: true, Active: true, Staff: true, Superuser: true}
	request := func(method, path string, data url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(data.Encode()))
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		if method == "POST" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "http://example.test")
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		site.ServeHTTP(w, r)
		return w
	}
	hidden := func(html, name string) string {
		t.Helper()
		match := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`).FindStringSubmatch(html)
		if len(match) != 2 {
			t.Fatalf("missing %s in %s", name, html)
		}
		return match[1]
	}
	get := request("GET", "/admin/shop/product/add/", nil, nil)
	if get.Code != 200 {
		t.Fatal(get.Code, get.Body.String())
	}
	data := url.Values{"name": {"First product"}, "secret": {"forged"}, "csrfmiddlewaretoken": {hidden(get.Body.String(), "csrfmiddlewaretoken")}, "_edit_token": {hidden(get.Body.String(), "_edit_token")}}
	post := request("POST", "/admin/shop/product/add/", data, get.Result().Cookies())
	if post.Code != 303 {
		t.Fatal(post.Code, post.Body.String())
	}
	product, err := orm.For(store, func() *adminProduct { return &adminProduct{} }).Filter(orm.Q("name", "First product")).Get(ctx)
	if err != nil || product.Tenant != "tenant-one" || product.Secret != "server-owned" {
		t.Fatal(product, err)
	}
	var count int64
	if err = db.QueryRow(ctx, backend, "SELECT count(*) FROM gogo_admin_log", nil, &count); err != nil || count != 1 {
		t.Fatal("missing transactional audit", count, err)
	}
	if err = store.Save(ctx, &adminProduct{Tenant: "tenant-two", Name: "Hidden product", Secret: "hidden"}, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	list := request("GET", "/admin/shop/product/", nil, nil)
	if list.Code != 200 || strings.Contains(list.Body.String(), "Hidden product") {
		t.Fatal(list.Code, list.Body.String())
	}
	link := regexp.MustCompile(`href="(/admin/shop/product/[^"]+/change/)"`).FindStringSubmatch(list.Body.String())
	if len(link) != 2 {
		t.Fatal("missing change link", list.Body.String())
	}
	get = request("GET", link[1], nil, nil)
	if get.Code != 200 {
		t.Fatal(get.Code, get.Body.String())
	}
	store.AfterSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if event.Record.Schema().DBTable() == "gogo_admin_log" {
			return errors.New("audit failure")
		}
		return nil
	}}
	data = url.Values{"name": {"Must roll back"}, "csrfmiddlewaretoken": {hidden(get.Body.String(), "csrfmiddlewaretoken")}, "_edit_token": {hidden(get.Body.String(), "_edit_token")}}
	post = request("POST", link[1], data, get.Result().Cookies())
	if post.Code != 503 {
		t.Fatal(post.Code, post.Body.String())
	}
	store.AfterSave = nil
	product, err = orm.For(store, func() *adminProduct { return &adminProduct{} }).Filter(orm.Q("id", product.ID)).Get(ctx)
	if err != nil || product.Name != "First product" {
		t.Fatal("audit failure did not rollback parent", product, err)
	}
	if err = db.QueryRow(ctx, backend, "SELECT count(*) FROM gogo_admin_log", nil, &count); err != nil || count != 1 {
		t.Fatal("audit failure did not rollback log", count, err)
	}
}
