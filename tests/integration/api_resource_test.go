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

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/urls"
)

type observedAPIBackend struct {
	db.Backend
	queries, failAt int
}

func (b *observedAPIBackend) Query(ctx context.Context, sql string, args ...any) (db.Rows, error) {
	b.queries++
	if b.failAt > 0 && b.queries == b.failAt {
		return nil, &db.Error{Code: db.Unavailable, Message: "private provider failure"}
	}
	return b.Backend.Query(ctx, sql, args...)
}

func TestPostgresAPIResourceScopeProjectionPaginationAndSafeFailure(t *testing.T) {
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
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	observed := &observedAPIBackend{Backend: backend}
	store := orm.New(observed, registry)
	var rows []*adminProduct
	for _, item := range [][2]string{{"one", "Beta"}, {"one", "Alpha"}, {"one", "Alpha"}, {"two", "Alpha"}} {
		row := &adminProduct{Tenant: item[0], Name: item[1], Secret: "private model value"}
		if err := store.Save(ctx, row, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	serializer, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"id", "name", "secret"}, Overrides: map[string]api.Field{"secret": api.ComputedField("secret", func(context.Context, api.ValueReader) (any, error) { panic("forbidden field computation") })}})
	if err != nil {
		t.Fatal(err)
	}
	base := api.ResourceConfig{Store: store, Model: schema.Key(), Serializer: serializer, Policy: auth.ModelPolicy{AllowSuperuser: true}, Scope: func(_ context.Context, p auth.Principal, _ models.Schema) (db.Predicate, error) {
		return orm.Q("tenant", p.ID), nil
	}, AllowField: func(_ context.Context, _ auth.Principal, _ models.Record, name string) (bool, error) {
		return name != "secret", nil
	}, Filters: api.FilterConfig{Filters: []api.Filter{{Parameter: "ids", Field: "id", Lookup: "in"}, {Parameter: "name", Field: "name", Lookup: "exact"}}, SearchFields: []string{"name"}, OrderingFields: []string{"name", "id"}, DefaultOrdering: []string{"name"}}, Pagination: pagination.Config{DefaultSize: 2}, IncludeCount: true}
	principal := auth.Principal{ID: "one", Authenticated: true, Active: true, Superuser: true}
	build := func(config api.ResourceConfig) http.Handler {
		t.Helper()
		resource, err := api.NewResource(config)
		if err != nil {
			t.Fatal(err)
		}
		routes, err := resource.Routes("products/", "product")
		if err != nil {
			t.Fatal(err)
		}
		router, err := urls.New(urls.Include("api/", "shop", routes...))
		if err != nil {
			t.Fatal(err)
		}
		return router
	}
	call := func(handler http.Handler, p auth.Principal, method, path, accept string) *httptest.ResponseRecorder {
		t.Helper()
		observed.queries = 0
		r := httptest.NewRequest(method, "https://example.test"+path, nil).WithContext(auth.WithPrincipal(ctx, p))
		if accept != "" {
			r.Header.Set("Accept", accept)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	handler := build(base)
	resource, err := api.NewResource(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []struct {
		err    error
		status int
	}{{api.ErrInvalidKey, 404}, {&db.Error{Code: db.Unavailable, Message: "private key provider"}, 503}, {context.DeadlineExceeded, 504}, {errors.New("private key failure"), 500}} {
		w := call(resource.DetailHandler(func(*http.Request) (api.Values, error) { return nil, failure.err }), principal, "GET", "/api/products/key/", "")
		if w.Code != failure.status || observed.queries != 0 || strings.Contains(w.Body.String(), "private") {
			t.Fatal("route decoder failure misclassified", w.Code, failure.status, w.Body.String())
		}
	}
	for _, decoderFails := range []bool{false, true} {
		canceled, cancel := context.WithCancel(auth.WithPrincipal(ctx, principal))
		h := resource.DetailHandler(func(*http.Request) (api.Values, error) {
			cancel()
			if decoderFails {
				return nil, api.ErrInvalidKey
			}
			return api.Values{}, nil
		})
		observed.queries = 0
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "https://example.test/key/", nil).WithContext(canceled))
		if w.Code == 404 || w.Code == 200 || observed.queries != 0 {
			t.Fatal("canceled decoder became absence", w.Code, observed.queries)
		}
	}
	list := call(handler, principal, "GET", "/api/products/", "")
	if list.Code != 200 {
		t.Fatal(list.Code, list.Body.String())
	}
	var collection api.Collection
	if err := json.Unmarshal(list.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	if collection.Count == nil || *collection.Count != 3 || len(collection.Results) != 2 || collection.Results[0]["id"] != float64(rows[1].ID) || collection.Results[1]["id"] != float64(rows[2].ID) || collection.Next == "" || strings.Contains(list.Body.String(), "secret") || strings.Contains(list.Body.String(), "private") || observed.queries != 2 {
		t.Fatal("scope/projection/order/page mismatch", list.Body.String(), observed.queries)
	}
	if list.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(list.Header().Get("Vary"), "Authorization") {
		t.Fatal("private resource cache policy missing")
	}
	next := call(handler, principal, "GET", "/api/products/"+collection.Next, "")
	if next.Code != 200 {
		t.Fatal(next.Code, next.Body.String())
	}
	if err := json.Unmarshal(next.Body.Bytes(), &collection); err != nil || len(collection.Results) != 1 || collection.Results[0]["id"] != float64(rows[0].ID) {
		t.Fatal("next page", next.Body.String(), err)
	}
	for _, path := range []string{fmt.Sprintf("/api/products/%d/", rows[3].ID), "/api/products/99999/", "/api/products/not-a-number/"} {
		w := call(handler, principal, "GET", path, "")
		if w.Code != 404 || strings.Contains(w.Body.String(), "private") {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	filtered := call(handler, principal, "GET", fmt.Sprintf("/api/products/?ids=%d&ids=%d&search=Alpha&ordering=-id", rows[2].ID, rows[3].ID), "")
	if err := json.Unmarshal(filtered.Body.Bytes(), &collection); err != nil || filtered.Code != 200 || collection.Count == nil || *collection.Count != 1 || len(collection.Results) != 1 {
		t.Fatal("filter escaped scope", filtered.Code, filtered.Body.String(), err)
	}
	for _, path := range []string{"/api/products/?tenant=two", "/api/products/?ordering=secret", "/api/products/?page_size=201", "/api/products/?page=1&page=2", "/api/products/?search=%FF", "/api/products/?name=%ZZ", "/api/products/?name=Alpha;tenant=two"} {
		w := call(handler, principal, "GET", path, "")
		if w.Code != 400 || observed.queries != 0 {
			t.Fatal("invalid query reached database", path, w.Code, observed.queries)
		}
	}
	for _, p := range []auth.Principal{{}, {ID: "one", Authenticated: true, Active: true}, {ID: "one", Authenticated: true, Active: false, Superuser: true}} {
		w := call(handler, p, "GET", "/api/products/", "")
		if w.Code != 401 && w.Code != 403 || observed.queries != 0 {
			t.Fatal("denied identity queried data", w.Code, observed.queries)
		}
	}
	constrained, err := auth.ConstrainPrincipal(principal, []string{"shop.change_product"})
	if err != nil {
		t.Fatal(err)
	}
	if w := call(handler, constrained, "GET", "/api/products/", ""); w.Code != 403 || observed.queries != 0 {
		t.Fatal("token scope widened", w.Code, observed.queries)
	}
	if w := call(handler, principal, "GET", "/api/products/", "application/json;q=0, */*;q=1"); w.Code != 406 || observed.queries != 0 {
		t.Fatal("unacceptable representation queried data", w.Code, observed.queries)
	}
	if w := call(handler, principal, "HEAD", "/api/products/", ""); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("HEAD body", w.Code, w.Body.String())
	}
	if w := call(handler, principal, "POST", "/api/products/", ""); w.Code != 405 || observed.queries != 0 {
		t.Fatal("read-only route mutated", w.Code)
	}
	for _, failAt := range []int{1, 2} {
		observed.failAt = failAt
		w := call(handler, principal, "GET", "/api/products/", "")
		if w.Code != 503 || strings.Contains(w.Body.String(), "results") || strings.Contains(w.Body.String(), "private") {
			t.Fatal("partial result or provider leak", w.Code, w.Body.String())
		}
	}
	observed.failAt = 0
	objectDenied := base
	objectDenied.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, r auth.Resource) error {
		if r.Object != nil {
			return auth.ErrPermissionDenied
		}
		return nil
	})
	if w := call(build(objectDenied), principal, "GET", fmt.Sprintf("/api/products/%d/", rows[0].ID), ""); w.Code != 404 {
		t.Fatal("hidden object disclosed", w.Code)
	}
	if w := call(build(objectDenied), principal, "GET", "/api/products/", ""); w.Code != 403 || strings.Contains(w.Body.String(), "results") {
		t.Fatal("partial object-denied list", w.Code, w.Body.String())
	}
	public := base
	public.AllowAnonymous = true
	public.Policy = auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { return nil })
	public.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
		return orm.Q("tenant", "one"), nil
	}
	if w := call(build(public), auth.Principal{}, "GET", "/api/products/", ""); w.Code != 200 {
		t.Fatal("explicit anonymous resource denied", w.Code, w.Body.String())
	}
	for _, stage := range []string{"permission", "scope", "field"} {
		config := base
		switch stage {
		case "permission":
			config.Policy = auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { panic("private policy value") })
		case "scope":
			config.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
				panic("private scope value")
			}
		case "field":
			config.AllowField = func(context.Context, auth.Principal, models.Record, string) (bool, error) {
				panic("private field value")
			}
		}
		w := call(build(config), principal, "GET", "/api/products/", "")
		if w.Code != 503 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "results") {
			t.Fatal("callback panic escaped", stage, w.Code, w.Body.String())
		}
	}
	failedScope := base
	failedScope.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
		return db.Predicate{}, errors.New("private failure")
	}
	if w := call(build(failedScope), principal, "GET", "/api/products/", ""); w.Code != 500 || observed.queries != 0 || strings.Contains(w.Body.String(), "private") {
		t.Fatal("failed scope fell back", w.Code, observed.queries)
	}
}

func TestPostgresAPITextFilterOperandsUseTypedQueryNotModelValidation(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	schema := models.Schema{AppLabel: "shop", Name: "Contact", Fields: []models.Field{models.BigAutoField("id"), models.EmailField("email"), models.URLField("url"), models.CharField("code", models.WithMinLength(5), models.WithMaxLength(100))}}
	if err := backend.SchemaEditor().CreateModel(ctx, backend, schema); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, registry)
	row, _ := models.NewRecord(schema)
	_ = row.Set("email", "person@example.com")
	_ = row.Set("url", "https://example.com/path")
	_ = row.Set("code", "alpha")
	if err := store.Save(ctx, row, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	serializer, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"id", "code"}})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := api.NewResource(api.ResourceConfig{Store: store, Model: schema.Key(), Serializer: serializer, AllowAnonymous: true, Policy: auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { return nil }), Scope: func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) { return db.Predicate{}, nil }, Filters: api.FilterConfig{Filters: []api.Filter{{Parameter: "email", Field: "email", Lookup: "icontains"}, {Parameter: "url", Field: "url", Lookup: "startswith"}, {Parameter: "code", Field: "code", Lookup: "contains"}}}})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	resource.ListHandler().ServeHTTP(w, httptest.NewRequest("GET", "https://example.test/contacts/?email=%40example.com&url=https%3A%2F%2F&code=a", nil))
	var result api.Collection
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || len(result.Results) != 1 || result.Results[0]["code"] != "alpha" {
		t.Fatal("partial text query failed", w.Code, w.Body.String(), err)
	}
}
