package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/security"
)

func TestPostgresAPICursorBindsScopeAndQueryWithExactStableKeys(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	schema := models.Schema{AppLabel: "shop", Name: "CursorItem", Fields: []models.Field{models.BigIntegerField("id", models.Primary), models.CharField("tenant", models.WithMaxLength(20)), models.CharField("name", models.WithMaxLength(100))}}
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
	firstID := int64(9007199254740993)
	save := func(id int64, tenant, name string) {
		t.Helper()
		row, _ := models.NewRecord(schema)
		_ = row.Set("id", id)
		_ = row.Set("tenant", tenant)
		_ = row.Set("name", name)
		if err := store.Save(ctx, row, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for i, name := range []string{"Alpha", "Alpha", "Beta", "Beta", "Gamma"} {
		save(firstID+int64(i), "one", name)
	}
	save(firstID+99, "two", "Alpha")
	serializer, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"id", "name"}})
	if err != nil {
		t.Fatal(err)
	}
	key, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "cursor-resource-integration")
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion := "scope-v1"
	options := api.ResourceCursorOptions{Name: "products", Version: "v1", Signer: signer, ImmutableFields: []string{"id", "name"}, ScopeIdentity: func(context.Context, auth.Principal, models.Schema) (string, error) { return scopeVersion, nil }}
	base := api.ResourceConfig{Store: store, Model: schema.Key(), Serializer: serializer, Policy: auth.ModelPolicy{AllowSuperuser: true}, Scope: func(_ context.Context, p auth.Principal, _ models.Schema) (db.Predicate, error) {
		return orm.Q("tenant", p.ID), nil
	}, Pagination: pagination.Config{Mode: pagination.CursorMode, DefaultSize: 2}, Cursor: &options, IncludeCount: true, Filters: api.FilterConfig{SearchFields: []string{"name"}, DefaultOrdering: []string{"name"}, OrderingFields: []string{"name", "id"}}}
	build := func(config api.ResourceConfig) http.Handler {
		t.Helper()
		resource, err := api.NewResource(config)
		if err != nil {
			t.Fatal(err)
		}
		return resource.ListHandler()
	}
	principal := auth.Principal{ID: "one", Authenticated: true, Active: true, Superuser: true, AuthVersion: 1}
	call := func(handler http.Handler, p auth.Principal, query string) *httptest.ResponseRecorder {
		t.Helper()
		observed.queries = 0
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "https://example.test/products/"+query, nil).WithContext(auth.WithPrincipal(ctx, p))
		handler.ServeHTTP(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) api.Collection {
		t.Helper()
		var result api.Collection
		decoder := json.NewDecoder(strings.NewReader(w.Body.String()))
		decoder.UseNumber()
		if w.Code != 200 || decoder.Decode(&result) != nil {
			t.Fatal("cursor page failed", w.Code, w.Body.String())
		}
		return result
	}
	handler := build(base)
	first := decode(call(handler, principal, ""))
	if len(first.Results) != 2 || first.Count == nil || *first.Count != 5 || first.Next == "" {
		t.Fatal(first)
	}
	if first.Results[0]["id"].(json.Number).String() != strconv.FormatInt(firstID, 10) || first.Results[1]["id"].(json.Number).String() != strconv.FormatInt(firstID+1, 10) {
		t.Fatal("large key precision lost")
	}
	seen := []string{}
	for _, item := range first.Results {
		seen = append(seen, item["id"].(json.Number).String())
	}
	// Earlier insertions do not shift a keyset page into duplicating old rows.
	save(firstID-1, "one", "Aardvark")
	next := first.Next
	for range 4 {
		page := decode(call(handler, principal, next))
		for _, item := range page.Results {
			seen = append(seen, item["id"].(json.Number).String())
		}
		next = page.Next
		if next == "" {
			break
		}
	}
	if next != "" || len(seen) != 5 {
		t.Fatal("cursor did not finish original ordered set", seen)
	}
	for i, id := range seen {
		if id != strconv.FormatInt(firstID+int64(i), 10) {
			t.Fatal("cursor duplicated/skipped a stable row", seen)
		}
	}
	parsed, err := url.Parse(first.Next)
	if err != nil {
		t.Fatal(err)
	}
	original := parsed.Query()
	for _, change := range []url.Values{{"search": {"Alpha"}}, {"ordering": {"-name"}}, {"page_size": {"3"}}, {"page": {"2"}}, {"cursor": {"forged"}}} {
		values := url.Values{}
		for k, v := range original {
			values[k] = append([]string(nil), v...)
		}
		for k, v := range change {
			values[k] = v
		}
		w := call(handler, principal, "?"+values.Encode())
		if w.Code != 400 || observed.queries != 0 {
			t.Fatal("cursor rebound to another query", w.Code, observed.queries)
		}
	}
	for _, change := range []string{"actor", "version", "grants", "scope", "resource", "api_version"} {
		p := principal
		config := base
		cursor := options
		config.Cursor = &cursor
		switch change {
		case "actor":
			p.ID = "two"
		case "version":
			p.AuthVersion++
		case "grants":
			p.Permissions = []string{"shop.view_cursoritem"}
		case "scope":
			scopeVersion = "scope-v2"
		case "resource":
			cursor.Name = "other"
		case "api_version":
			cursor.Version = "v2"
		}
		w := call(build(config), p, first.Next)
		scopeVersion = "scope-v1"
		if w.Code != 400 || observed.queries != 0 {
			t.Fatal("cursor rebound across identity/resource scope", change, w.Code, observed.queries)
		}
	}
	hidden := base
	hidden.AllowField = func(_ context.Context, _ auth.Principal, _ models.Record, name string) (bool, error) {
		return name != "id", nil
	}
	w := call(build(hidden), principal, "")
	if w.Code != 403 || strings.Contains(w.Body.String(), "cursor") || strings.Contains(w.Body.String(), "results") {
		t.Fatal("hidden cursor key disclosed", w.Code, w.Body.String())
	}
	for _, mutation := range []string{"missing options", "mutable ordering", "no identity", "no signer", "hidden key"} {
		config := base
		cursor := options
		config.Cursor = &cursor
		switch mutation {
		case "missing options":
			config.Cursor = nil
		case "mutable ordering":
			cursor.ImmutableFields = []string{"id"}
		case "no identity":
			cursor.ScopeIdentity = nil
		case "no signer":
			cursor.Signer = nil
		case "hidden key":
			config.Serializer, _ = api.FromModel(schema, api.ModelOptions{Fields: []string{"name"}})
		}
		if _, err := api.NewResource(config); err == nil {
			t.Fatal("invalid cursor contract accepted", mutation)
		}
	}
}

func TestPostgresAPICursorTypedPositionsAcrossTemporalDecimalAndNetworkFields(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	for _, test := range []struct {
		name   string
		field  models.Field
		values []any
	}{
		{"Dates", models.DateField("value"), []any{"2026-01-01", "2026-01-02", "2026-01-03"}},
		{"Times", models.TimeField("value"), []any{"01:00:00.000001", "02:00:00.000002", "03:00:00.000003"}},
		{"Instants", models.DateTimeField("value"), []any{"2026-01-01T01:00:00.000001Z", "2026-01-01T02:00:00.000002Z", "2026-01-01T03:00:00.000003Z"}},
		{"Durations", models.DurationField("value"), []any{time.Second + time.Microsecond, 2*time.Second + 2*time.Microsecond, 3*time.Second + 3*time.Microsecond}},
		{"Decimals", models.DecimalField("value", 30, 9), []any{"9007199254740993.000000001", "9007199254740993.000000002", "9007199254740993.000000003"}},
		{"Addresses", models.GenericIPAddressField("value"), []any{"192.0.2.1", "192.0.2.2", "192.0.2.3"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema := models.Schema{AppLabel: "cursor", Name: test.name, Fields: []models.Field{models.BigAutoField("id"), test.field}}
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
			for _, value := range test.values {
				row, _ := models.NewRecord(schema)
				cleaned, err := test.field.Clean(ctx, value)
				if err != nil {
					t.Fatal(err)
				}
				_ = row.Set("value", cleaned)
				if err := store.Save(ctx, row, orm.SaveOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			serializer, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"id", "value"}})
			if err != nil {
				t.Fatal(err)
			}
			key, _ := security.RandomToken(32)
			signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "typed-cursor")
			if err != nil {
				t.Fatal(err)
			}
			for _, order := range []string{"value", "-value"} {
				resource, err := api.NewResource(api.ResourceConfig{Store: store, Model: schema.Key(), Serializer: serializer, AllowAnonymous: true, Policy: auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { return nil }), Scope: func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) { return db.Predicate{}, nil }, Filters: api.FilterConfig{DefaultOrdering: []string{order}}, Pagination: pagination.Config{Mode: pagination.CursorMode, DefaultSize: 1}, Cursor: &api.ResourceCursorOptions{Name: test.name, Version: "v1", Signer: signer, ImmutableFields: []string{"id", "value"}, ScopeIdentity: func(context.Context, auth.Principal, models.Schema) (string, error) { return "public-v1", nil }}})
				if err != nil {
					t.Fatal(err)
				}
				next := ""
				for page := 0; page < 3; page++ {
					w := httptest.NewRecorder()
					resource.ListHandler().ServeHTTP(w, httptest.NewRequest("GET", "https://example.test/items/"+next, nil))
					var result api.Collection
					decoder := json.NewDecoder(strings.NewReader(w.Body.String()))
					decoder.UseNumber()
					if w.Code != 200 || decoder.Decode(&result) != nil || len(result.Results) != 1 {
						t.Fatal("typed cursor failed", order, page, w.Code, w.Body.String())
					}
					want := page + 1
					if strings.HasPrefix(order, "-") {
						want = 3 - page
					}
					if result.Results[0]["id"].(json.Number).String() != strconv.Itoa(want) {
						t.Fatal("typed cursor order mismatch", order, page, result.Results)
					}
					next = result.Next
					if page < 2 && next == "" || page == 2 && next != "" {
						t.Fatal("typed cursor terminated incorrectly", order, page)
					}
				}
			}
		})
	}
}
