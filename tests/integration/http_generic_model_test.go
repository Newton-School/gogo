package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

func genericHTMLTemplate(name, source string) ghttp.TemplateViewOptions {
	return ghttp.TemplateViewOptions{
		ReadViewOptions: ghttp.ReadViewOptions{AllowOptions: true, Authorize: func(r *http.Request) error { return r.Context().Err() }},
		TemplateName:    name,
		Templates:       templates.Config{Loaders: []templates.Loader{templates.MapLoader{name: source}}},
	}
}

func TestPostgresGenericModelViewsScopedHTMLNavigationAndCurrentAuthority(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	schema := (&adminProduct{}).Schema()
	if err := backend.SchemaEditor().CreateModel(ctx, backend, schema); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	observed := &observedAPIBackend{Backend: backend}
	store := orm.New(observed, registry)
	var rows []*adminProduct
	for _, seed := range [][2]string{{"one", "Beta"}, {"one", "Alpha"}, {"one", "Alpha"}, {"two", "Hidden"}, {"three", "<em>escaped</em>"}} {
		row := &adminProduct{Tenant: seed[0], Name: seed[1], Secret: "private database value"}
		if err := store.Save(ctx, row, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	grant, fieldGrant := true, true
	base := ghttp.ModelReadOptions{
		Store: store, Model: schema.Key(), Fields: []string{"id", "name", "secret"}, PolicyFields: []string{"tenant"},
		Policy: auth.PolicyFunc(func(ctx context.Context, p auth.Principal, action string, resource auth.Resource) error {
			if !grant {
				return auth.ErrPermissionDenied
			}
			if err := (auth.ModelPolicy{AllowSuperuser: true}).Authorize(ctx, p, action, resource); err != nil {
				return err
			}
			if resource.Object != nil {
				record := resource.Object.(models.Record)
				value, err := record.Get("tenant")
				if err != nil || value != p.ID {
					return auth.ErrPermissionDenied
				}
				// Mutating a policy argument must not retarget later decisions.
				_ = record.Set("name", "policy mutation")
				resource.ID.(map[string]any)["id"] = int64(-1)
			}
			return nil
		}),
		Scope: func(_ context.Context, p auth.Principal, _ models.Schema) (db.Predicate, error) {
			return orm.Q("tenant", p.ID), nil
		},
		AllowField: func(_ context.Context, _ auth.Principal, r models.Record, name string) (bool, error) {
			value, err := r.Get("name")
			if err != nil || value == "policy mutation" {
				return false, errors.New("aliased policy record")
			}
			return name != "secret" && fieldGrant, nil
		},
	}
	listTemplate := genericHTMLTemplate("products/list.html", `{% for item in object_list %}{{ item.id }}:{{ item.name }}:{{ item.secret }}:{{ item.tenant }};{% empty %}empty{% endfor %}|next={{ page.next }}|previous={{ page.previous }}`)
	listTemplate.ExtraContext = templates.Context{"object_list": []any{"forged extra"}, "page": "forged page"}
	listTemplate.Context = func(*http.Request) (templates.Context, error) {
		return templates.Context{"object_list": []any{"forged callback"}}, nil
	}
	buildList := func(options ghttp.TemplateViewOptions) http.Handler {
		t.Helper()
		h, err := ghttp.NewListView(ghttp.ListViewOptions{TemplateViewOptions: options, ModelReadOptions: base, Ordering: []string{"name"}, Pagination: pagination.Config{DefaultSize: 2}})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	list := buildList(listTemplate)
	detailTemplate := genericHTMLTemplate("products/detail.html", `{{ object.id }}:{{ object.name }}:{{ object.secret }}:{{ object.tenant }}`)
	detail, err := ghttp.NewDetailView(ghttp.DetailViewOptions{
		TemplateViewOptions: detailTemplate, ModelReadOptions: base,
		Key: func(r *http.Request) (map[string]any, error) { return map[string]any{"id": urls.Param(r, "id")}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Include("products/", "products", urls.Path("", list, "list"), urls.Path("<str:id>/", detail, "detail")))
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{ID: "one", Authenticated: true, Active: true, Superuser: true}
	call := func(handler http.Handler, p auth.Principal, method, path string) *httptest.ResponseRecorder {
		t.Helper()
		observed.queries = 0
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, "https://example.test"+path, nil).WithContext(auth.WithPrincipal(ctx, p)))
		if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "forged") || strings.Contains(w.Body.String(), "policy mutation") {
			t.Fatal("private/aliased context disclosed", w.Code, w.Body.String())
		}
		return w
	}
	get := call(router, principal, "GET", "/products/")
	wantPrefix := fmt.Sprintf("%d:Alpha::;%d:Alpha::;", rows[1].ID, rows[2].ID)
	if get.Code != 200 || !strings.HasPrefix(get.Body.String(), wantPrefix) || !strings.Contains(get.Body.String(), "page=2") || observed.queries != 1 {
		t.Fatal("scoped stable first page", get.Code, get.Body.String(), observed.queries)
	}
	next := call(router, principal, "GET", "/products/?page=2&page_size=2")
	if next.Code != 200 || !strings.HasPrefix(next.Body.String(), fmt.Sprintf("%d:Beta::;", rows[0].ID)) || !strings.Contains(next.Body.String(), "previous=?page=1") {
		t.Fatal("next page", next.Code, next.Body.String())
	}
	empty := call(router, principal, "GET", "/products/?page=3&page_size=2")
	if empty.Code != 200 || !strings.HasPrefix(empty.Body.String(), "empty|") {
		t.Fatal("empty page", empty.Code, empty.Body.String())
	}
	for _, path := range []string{fmt.Sprintf("/products/%d/", rows[3].ID), "/products/99999/", "/products/not-an-integer/"} {
		if w := call(router, principal, "GET", path); w.Code != 404 {
			t.Fatal("hidden/missing/malformed detail", path, w.Code)
		}
	}
	if w := call(router, principal, "GET", fmt.Sprintf("/products/%d/", rows[0].ID)); w.Code != 200 || w.Body.String() != fmt.Sprintf("%d:Beta::", rows[0].ID) {
		t.Fatal("scoped detail", w.Code, w.Body.String())
	}
	third := principal
	third.ID = "three"
	if w := call(router, third, "GET", fmt.Sprintf("/products/%d/", rows[4].ID)); w.Code != 200 || !strings.Contains(w.Body.String(), "&lt;em&gt;escaped&lt;/em&gt;") {
		t.Fatal("HTML not escaped", w.Code, w.Body.String())
	}
	for _, path := range []string{"/products/?tenant=two", "/products/?ordering=secret", "/products/?page=1&page=2", "/products/?page_size=201", "/products/?page=%FF", "/products/?page=1;tenant=two"} {
		if w := call(router, principal, "GET", path); w.Code != 400 || observed.queries != 0 {
			t.Fatal("invalid query reached SQL", path, w.Code, observed.queries)
		}
	}
	for _, p := range []auth.Principal{{}, {ID: "one", Authenticated: true, Active: false}, {ID: "one", Authenticated: true, Active: true}} {
		if w := call(router, p, "GET", "/products/"); w.Code != 401 && w.Code != 403 || observed.queries != 0 {
			t.Fatal("denied principal queried data", w.Code, observed.queries)
		}
	}
	limited, err := auth.ConstrainPrincipal(principal, []string{"shop.change_product"})
	if err != nil {
		t.Fatal(err)
	}
	if w := call(router, limited, "GET", "/products/"); w.Code != 403 || observed.queries != 0 {
		t.Fatal("token ceiling widened", w.Code, observed.queries)
	}
	for _, method := range []string{"OPTIONS", "POST"} {
		w := call(router, principal, method, "/products/")
		want := 200
		if method == "POST" {
			want = 405
		}
		if w.Code != want || observed.queries != 0 {
			t.Fatal("method dispatch queried data", method, w.Code, observed.queries)
		}
	}
	head := call(router, principal, "HEAD", "/products/")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") || head.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("HEAD/private response", head.Code, head.Header())
	}
	for _, revoke := range []string{"model", "field"} {
		grant, fieldGrant = true, true
		template := listTemplate
		template.Context = func(*http.Request) (templates.Context, error) {
			if revoke == "model" {
				grant = false
			} else {
				fieldGrant = false
			}
			return nil, nil
		}
		if w := call(buildList(template), principal, "GET", "/products/"); w.Code != 403 || strings.Contains(w.Body.String(), "Alpha") {
			t.Fatal("late grant lost", revoke, w.Code, w.Body.String())
		}
	}
	grant, fieldGrant = true, true
	observed.failAt = 1
	if w := call(router, principal, "GET", "/products/"); w.Code != 503 || strings.Contains(w.Body.String(), "Alpha") {
		t.Fatal("provider error disclosed page", w.Code, w.Body.String())
	}
	observed.failAt = 0
	var count int64
	if err := db.QueryRow(ctx, backend, `SELECT count(*) FROM shop_product`, nil, &count); err != nil || count != 5 {
		t.Fatal("read view changed business rows", count, err)
	}
}

func TestPostgresGenericModelViewDiscardsLateStatementFailure(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	// This owned, deliberately volatile fault fixture returns a row before a
	// deferred autocommit failure. It is not application read-only SQL advice.
	for _, statement := range []string{
		`CREATE TABLE generic_owners (id bigint PRIMARY KEY)`,
		`CREATE TABLE generic_links (id bigint PRIMARY KEY, owner_id bigint REFERENCES generic_owners(id) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE FUNCTION generic_emit() RETURNS bigint LANGUAGE plpgsql VOLATILE AS $$ BEGIN INSERT INTO generic_links VALUES (1, 99); RETURN 7; END $$`,
		`CREATE VIEW generic_result AS SELECT generic_emit() AS id, 'private prefix'::text AS title`,
	} {
		if _, err := backend.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	schema := models.Schema{AppLabel: "generic", Name: "Result", Table: "generic_result", Unmanaged: true, Fields: []models.Field{models.BigAutoField("id"), models.TextField("title")}}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	hooks := 0
	options := ghttp.ModelReadOptions{
		Store: orm.New(backend, registry), Model: schema.Key(), Fields: []string{"title"}, AllowAnonymous: true,
		Policy: auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, r auth.Resource) error {
			if r.Object != nil {
				hooks++
			}
			return nil
		}),
		Scope: func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) { return db.Predicate{}, nil },
	}
	template := genericHTMLTemplate("fault.html", `{{ object.title }}{% for r in object_list %}{{ r.title }}{% endfor %}`)
	template.Context = func(*http.Request) (templates.Context, error) { hooks++; return nil, nil }
	list, err := ghttp.NewListView(ghttp.ListViewOptions{TemplateViewOptions: template, ModelReadOptions: options})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := ghttp.NewDetailView(ghttp.DetailViewOptions{TemplateViewOptions: template, ModelReadOptions: options, Key: func(*http.Request) (map[string]any, error) { return map[string]any{"id": int64(7)}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []http.Handler{list, detail} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 503 || strings.Contains(w.Body.String(), "private") || hooks != 0 {
			t.Fatal("late SQL failure became HTML", w.Code, w.Body.String(), hooks)
		}
		var count int64
		if err := db.QueryRow(ctx, backend, `SELECT count(*) FROM generic_links`, nil, &count); err != nil || count != 0 {
			t.Fatal("fault fixture did not roll back", count, err)
		}
	}
}

func TestPostgresGenericModelViewPreservesStoredScalarValues(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	schema := models.Schema{AppLabel: "generic", Name: "Scalar", Fields: []models.Field{
		models.BigIntegerField("id", models.Primary), models.DecimalField("amount", 24, 4),
		models.DateField("day"), models.TimeField("clock"), models.DateTimeField("instant"),
		models.DurationField("elapsed"), models.UUIDField("uuid"), models.GenericIPAddressField("address"),
		models.JSONField("payload"), models.JSONField("json_null"), models.JSONField("sql_null", models.Nullable),
	}}
	if err := backend.SchemaEditor().CreateModel(ctx, backend, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Exec(ctx, `INSERT INTO generic_scalar VALUES
		(9007199254740993, 9007199254740993.1200, '2024-02-29', '23:59:59.123456',
		'2024-02-29T23:59:59.123456+05:30', '2 hours 3.000004 seconds',
		'a987fbc9-4bed-3078-cf07-9141ba07c9f3', '2001:db8::1',
		'{"n":9007199254740993,"text":"<b>data</b>"}', 'null', NULL)`); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	fields := make([]string, 0, len(schema.Fields))
	for _, field := range schema.Fields {
		fields = append(fields, field.Name)
	}
	policyCalls := 0
	options := ghttp.ModelReadOptions{
		Store: orm.New(backend, registry), Model: schema.Key(), Fields: fields, AllowAnonymous: true,
		Scope: func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) { return db.Predicate{}, nil },
		Policy: auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, resource auth.Resource) error {
			if resource.Object == nil {
				return nil
			}
			policyCalls++
			record := resource.Object.(models.Record)
			payload, _ := record.Get("payload")
			jsonNull, _ := record.Get("json_null")
			sqlNull, _ := record.Get("sql_null")
			amount, _ := record.Get("amount")
			elapsed, _ := record.Get("elapsed")
			instant, _ := record.Get("instant")
			if value, ok := instant.(time.Time); !ok || !value.Equal(time.Date(2024, 2, 29, 18, 29, 59, 123456000, time.UTC)) {
				t.Fatal("stored instant changed in policy snapshot", instant)
			}
			if payload.(map[string]any)["n"] != json.Number("9007199254740993") || jsonNull != models.JSONNull || sqlNull != nil || amount != "9007199254740993.1200" || elapsed != 2*time.Hour+3*time.Second+4*time.Microsecond {
				t.Fatal("stored policy types lost precision or null distinction", payload, jsonNull, sqlNull, amount, elapsed)
			}
			return nil
		}),
	}
	template := genericHTMLTemplate("scalar.html", `{{ object.id }}|{{ object.amount }}|{{ object.day }}|{{ object.clock }}|{{ object.instant|date:"c" }}|{{ object.elapsed }}|{{ object.uuid }}|{{ object.address }}|{{ object.payload.n }}|{{ object.payload.text }}|{% if object.json_null %}bad{% else %}null{% endif %}|{% if object.sql_null %}bad{% else %}null{% endif %}`)
	handler, err := ghttp.NewDetailView(ghttp.DetailViewOptions{
		TemplateViewOptions: template, ModelReadOptions: options,
		Key: func(*http.Request) (map[string]any, error) { return map[string]any{"id": "9007199254740993"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	want := "9007199254740993|9007199254740993.1200|2024-02-29|23:59:59.123456|2024-02-29T18:29:59.123456&#43;00:00|2h0m3.000004s|a987fbc9-4bed-3078-cf07-9141ba07c9f3|2001:db8::1|9007199254740993|&lt;b&gt;data&lt;/b&gt;|null|null"
	if w.Code != 200 || w.Body.String() != want || policyCalls != 2 {
		t.Fatal("native scalar projection", w.Code, w.Body.String(), policyCalls)
	}
}
