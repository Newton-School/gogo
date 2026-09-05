package integration_test

import (
	"context"
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

type readonlyArticle struct {
	models.Base
	ID           int64
	OwnerID      *int64
	Tenant, Name string
}

func (*readonlyArticle) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "ReadonlyArticle", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.CharField("tenant", models.WithStructField("Tenant"), models.ReadOnly), models.CharField("name", models.WithStructField("Name")), models.ForeignKeyField("owner", models.Relation{Target: "shop.Product", OnDelete: models.Cascade}, models.WithStructField("OwnerID"), models.Nullable, models.Optional), models.ManyToManyField("labels", models.Relation{Target: "shop.Label", Through: "shop.ReadonlyLink", ThroughFields: []string{"article_id", "label_id"}}, models.Optional)}}
}

type readonlyLink struct {
	models.Base
	ID, ArticleID, LabelID int64
	Tenant                 string
}

func (*readonlyLink) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "ReadonlyLink", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.CharField("tenant", models.WithStructField("Tenant")), models.ForeignKeyField("article_id", models.Relation{Target: "shop.ReadonlyArticle", OnDelete: models.Cascade}, models.WithStructField("ArticleID")), models.ForeignKeyField("label_id", models.Relation{Target: "shop.Label", OnDelete: models.Cascade}, models.WithStructField("LabelID"))}}
}

func TestAdminReadonlyExplicitRelationshipsRespectIntermediateScopeAndViewOnly(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	registry := &models.Registry{}
	schemas := []models.Schema{(&adminProduct{}).Schema(), (&readonlyArticle{}).Schema(), (&adminLabel{}).Schema(), (&readonlyLink{}).Schema()}
	for _, schema := range schemas {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	editor, err := backend.SchemaEditor().(db.SchemaResolverEditor).WithSchemas(registry.All())
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range schemas {
		if err := editor.CreateModel(ctx, backend, schema); err != nil {
			t.Fatal(err)
		}
	}
	store := orm.New(backend, registry)
	article := &readonlyArticle{Tenant: "one", Name: "Visible article"}
	if err := store.Save(ctx, article, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	labels := []*adminLabel{{Tenant: "one", Name: "Visible"}, {Tenant: "one", Name: "Hidden intermediary"}, {Tenant: "two", Name: "Hidden target"}, {Tenant: "one", Name: "Policy denied"}}
	for index, label := range labels {
		if err := store.Save(ctx, label, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
		tenant := "one"
		if index == 1 {
			tenant = "two"
		}
		if err := store.Save(ctx, &readonlyLink{Tenant: tenant, ArticleID: article.ID, LabelID: label.ID}, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	throughScopes := 0
	adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{"shop.ReadonlyArticle": func() models.Model { return &readonlyArticle{} }, "shop.Label": func() models.Model { return &adminLabel{} }, "shop.ReadonlyLink": func() models.Model { return &readonlyLink{} }}, QueryScope: func(_ context.Context, p auth.Principal, schema models.Schema) (admin.QueryScope, error) {
		if schema.Key() == "shop.ReadonlyLink" {
			throughScopes++
		}
		return admin.QueryScope{Predicate: orm.Q("tenant", p.ID), Identity: p.ID}, nil
	}, ValidateWrite: func(context.Context, auth.Principal, models.Record) error { return auth.ErrPermissionDenied }})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := security.RandomToken(32)
	signer, _ := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "readonly-relations")
	policy := auth.PolicyFunc(func(_ context.Context, _ auth.Principal, action string, resource auth.Resource) error {
		if action != "view" {
			return auth.ErrPermissionDenied
		}
		if resource.Model == "Label" {
			if record, ok := resource.Object.(models.Record); ok && record != nil {
				value, _ := record.Get("id")
				if value == labels[3].ID {
					return auth.ErrPermissionDenied
				}
			}
		}
		return nil
	})
	site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Register(admin.ModelAdmin{Schema: article.Schema(), ReadonlyFields: []string{"labels"}, Fieldsets: []admin.Fieldset{{Name: "Details", Fields: []string{"name", "labels"}}}, ListDisplay: []string{"name"}}); err != nil {
		t.Fatal(err)
	}
	if err := site.Register(admin.ModelAdmin{Schema: labels[0].Schema(), Fields: []string{"name"}}); err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{ID: "one", Authenticated: true, Active: true, Staff: true}
	call := func(method, path string, values url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(values.Encode()))
		request = request.WithContext(auth.WithPrincipal(request.Context(), principal))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		site.ServeHTTP(response, request)
		return response
	}
	list := call("GET", "/admin/shop/readonlyarticle/", nil, nil)
	link := regexp.MustCompile(`href="(/admin/shop/readonlyarticle/[^"]+/change/)"`).FindStringSubmatch(list.Body.String())
	if len(link) != 2 {
		t.Fatal("readonly parent link missing", list.Code)
	}
	form := call("GET", link[1], nil, nil)
	if form.Code != 200 || !strings.Contains(form.Body.String(), "Label [1]") || strings.Contains(form.Body.String(), `name="name"`) || strings.Contains(form.Body.String(), `name="labels"`) {
		t.Fatal("view-only readonly relation failed", form.Code, form.Body.String())
	}
	for _, hidden := range []string{"Label [2]", "Label [3]", "Label [4]", "Hidden intermediary", "Hidden target", "Policy denied"} {
		if strings.Contains(form.Body.String(), hidden) {
			t.Fatal("readonly relation disclosed hidden endpoint")
		}
	}
	if throughScopes == 0 {
		t.Fatal("explicit intermediary scope never enforced")
	}
	token := regexp.MustCompile(`name="csrfmiddlewaretoken" value="([^"]+)"`).FindStringSubmatch(form.Body.String())
	if len(token) != 2 {
		t.Fatal("missing CSRF")
	}
	post := call("POST", link[1], url.Values{"csrfmiddlewaretoken": {token[1]}, "labels": {"2"}, "name": {"forged"}}, form.Result().Cookies())
	if post.Code != 403 {
		t.Fatal("view-only POST was not denied", post.Code)
	}
	count, err := orm.For(store, func() *readonlyLink { return &readonlyLink{} }).Count(ctx)
	if err != nil || count != 4 {
		t.Fatal("readonly request mutated intermediaries", count, err)
	}
}

func TestAdminInlineReadonlyRelationshipsUseScopedReaderAndIgnoreForgedChoices(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	registry := &models.Registry{}
	schemas := []models.Schema{(&adminProduct{}).Schema(), (&readonlyArticle{}).Schema(), (&adminLabel{}).Schema(), (&readonlyLink{}).Schema(), admin.LogSchema()}
	for _, schema := range schemas {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	editor, err := backend.SchemaEditor().(db.SchemaResolverEditor).WithSchemas(registry.All())
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range schemas {
		if err := editor.CreateModel(ctx, backend, schema); err != nil {
			t.Fatal(err)
		}
	}
	store := orm.New(backend, registry)
	parent := &adminProduct{Tenant: "one", Name: "Parent", Secret: "server"}
	if err := store.Save(ctx, parent, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	article := &readonlyArticle{OwnerID: &parent.ID, Tenant: "one", Name: "Inline article"}
	if err := store.Save(ctx, article, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	labels := []*adminLabel{{Tenant: "one", Name: "Visible"}, {Tenant: "one", Name: "Hidden intermediary"}, {Tenant: "two", Name: "Hidden target"}, {Tenant: "one", Name: "Policy hidden"}, {Tenant: "one", Name: "Resolver hidden"}}
	for index, label := range labels {
		if err := store.Save(ctx, label, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
		tenant := "one"
		if index == 1 {
			tenant = "two"
		}
		if err := store.Save(ctx, &readonlyLink{Tenant: tenant, ArticleID: article.ID, LabelID: label.ID}, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{"shop.Product": func() models.Model { return &adminProduct{} }, "shop.ReadonlyArticle": func() models.Model { return &readonlyArticle{} }, "shop.Label": func() models.Model { return &adminLabel{} }, "shop.ReadonlyLink": func() models.Model { return &readonlyLink{} }}, QueryScope: func(_ context.Context, p auth.Principal, _ models.Schema) (admin.QueryScope, error) {
		return admin.QueryScope{Predicate: orm.Q("tenant", p.ID), Identity: p.ID}, nil
	}, ValidateWrite: func(_ context.Context, p auth.Principal, record models.Record) error {
		tenant, err := record.Get("tenant")
		if err != nil || tenant != p.ID {
			return auth.ErrPermissionDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := security.RandomToken(32)
	signer, _ := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "readonly-inline")
	site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, resource auth.Resource) error {
		if resource.Model == "Label" {
			if record, ok := resource.Object.(models.Record); ok && record != nil {
				id, _ := record.Get("id")
				if id == labels[3].ID {
					return auth.ErrPermissionDenied
				}
			}
		}
		return nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Register(admin.ModelAdmin{Schema: parent.Schema(), Fields: []string{"name"}, ListDisplay: []string{"name"}, ConstraintChecker: store, Inlines: []admin.Inline{{Name: "articles", Schema: article.Schema(), FKName: "owner", Fields: []string{"name", "labels"}, Readonly: []string{"labels"}, Maximum: 5, ConstraintChecker: store, ResolveRelation: func(_ context.Context, _ models.Field, ids []string) ([]any, error) {
		if ids[0] == "5" {
			return nil, auth.ErrPermissionDenied
		}
		return []any{ids[0]}, nil
	}}}}); err != nil {
		t.Fatal(err)
	}
	if err := site.Register(admin.ModelAdmin{Schema: labels[0].Schema(), Fields: []string{"name"}}); err != nil {
		t.Fatal(err)
	}
	p := auth.Principal{ID: "one", Authenticated: true, Active: true, Staff: true}
	call := func(method, path string, values url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(values.Encode()))
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		site.ServeHTTP(w, r)
		return w
	}
	list := call("GET", "/admin/shop/product/", nil, nil)
	link := regexp.MustCompile(`href="(/admin/shop/product/[^"]+/change/)"`).FindStringSubmatch(list.Body.String())
	if len(link) != 2 {
		t.Fatal("missing parent link", list.Code)
	}
	get := call("GET", link[1], nil, nil)
	body := get.Body.String()
	if get.Code != 200 || !strings.Contains(body, "Label [1]") || strings.Contains(body, `name="articles-0-labels"`) {
		t.Fatal("readonly inline relationship failed", get.Code, body)
	}
	for _, hidden := range []string{"Label [2]", "Label [3]", "Label [4]", "Label [5]", "Hidden intermediary", "Hidden target", "Policy hidden", "Resolver hidden"} {
		if strings.Contains(body, hidden) {
			t.Fatal("readonly inline disclosed hidden target")
		}
	}
	values := url.Values{"name": {"Updated parent"}, "articles-TOTAL_FORMS": {"1"}, "articles-INITIAL_FORMS": {"1"}, "articles-0-name": {"Updated inline"}, "articles-0-labels": {"2"}}
	for _, name := range []string{"csrfmiddlewaretoken", "_edit_token", "articles-0-_id", "articles-0-_edit_token"} {
		match := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(body)
		if len(match) != 2 {
			t.Fatal("missing form token", name)
		}
		values.Set(name, match[1])
	}
	post := call("POST", link[1], values, get.Result().Cookies())
	if post.Code != 303 {
		t.Fatal("readonly inline blocked permitted scalar edit", post.Code, post.Body.String())
	}
	saved, err := orm.For(store, func() *readonlyArticle { return &readonlyArticle{} }).Filter(orm.Q("id", article.ID)).Get(ctx)
	if err != nil || saved.Name != "Updated inline" {
		t.Fatal("permitted inline edit not saved", err)
	}
	links, err := orm.For(store, func() *readonlyLink { return &readonlyLink{} }).OrderBy("label_id").All(ctx)
	if err != nil || len(links) != len(labels) {
		t.Fatal("forged readonly choices changed links", err)
	}
	for index, link := range links {
		if link.LabelID != labels[index].ID || link.ArticleID != article.ID {
			t.Fatal("forged readonly choices rewrote relationship")
		}
	}
}
