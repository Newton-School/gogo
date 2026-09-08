package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
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

type openAPINativeFixture struct {
	config                  api.ResourceConfig
	backend                 *observedAPIBackend
	nested                  *api.Serializer
	policyCalls, scopeCalls int
	fieldCalls              int
	deny                    bool
}

func newOpenAPINativeFixture(t *testing.T) *openAPINativeFixture {
	t.Helper()
	ctx := context.Background()
	backend := testservice.Postgres(t)
	schema := models.Schema{AppLabel: "openapi", Name: "Book", Fields: []models.Field{
		models.BigIntegerField("id", models.Primary), models.TextField("tenant"),
		models.TextField("internal_title"), models.TextField("password_hash"),
		models.JSONField("internal_profile"), models.TextField("internal_note"),
	}}
	if err := backend.SchemaEditor().CreateModel(ctx, backend, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Exec(ctx, `INSERT INTO openapi_book (id,tenant,internal_title,password_hash,internal_profile,internal_note) VALUES
		(1,'one','First','private password','{"display_name":"Ada","private_nested":"private nested"}','private note'),
		(2,'one','Second','private password','{"display_name":"Bea","extra_note":"Visible note","private_nested":"private nested"}','private note'),
		(3,'two','Hidden','private password','{"display_name":"Hidden","private_nested":"private nested"}','private note')`); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	display := api.StringField("display")
	display.Source = "display_name"
	note := api.StringField("note", models.Optional)
	note.Source = "extra_note"
	nested, err := api.New(api.Definition{Fields: []api.Field{display, note}})
	if err != nil {
		t.Fatal(err)
	}
	id := api.IntegerField("id")
	id.ReadOnly = true
	caption := api.StringField("caption")
	caption.Source = "internal_title"
	profile := api.NestedField("profile", nested)
	profile.Source = "internal_profile"
	password := api.StringField("password")
	password.Source, password.WriteOnly = "password_hash", true
	owner := api.StringField("owner")
	owner.Source, owner.Hidden = "tenant", true
	owner.Default = func(context.Context) (any, error) { panic("hidden default must never run on read or generation") }
	redacted := api.StringField("redacted")
	redacted.Source, redacted.ReadOnly = "internal_note", true
	redacted.Compute = func(context.Context, api.ValueReader) (any, error) { panic("field policy must precede computation") }
	serializer, err := api.New(api.Definition{Fields: []api.Field{id, caption, profile, password, owner, redacted}})
	if err != nil {
		t.Fatal(err)
	}
	f := &openAPINativeFixture{backend: &observedAPIBackend{Backend: backend}, nested: nested}
	f.config = api.ResourceConfig{
		Store: orm.New(f.backend, registry), Model: schema.Key(), Serializer: serializer,
		AllowAnonymous: true, IncludeCount: true, EntityTags: true,
		Pagination: pagination.Config{DefaultSize: 1},
		Policy: auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error {
			f.policyCalls++
			if f.deny {
				return auth.ErrPermissionDenied
			}
			return nil
		}),
		Scope: func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
			f.scopeCalls++
			return orm.Q("tenant", "one"), nil
		},
		AllowField: func(_ context.Context, _ auth.Principal, _ models.Record, name string) (bool, error) {
			f.fieldCalls++
			return name != "redacted", nil
		},
	}
	return f
}

func openAPINativeBind(t *testing.T, config api.ResourceConfig, security []api.OpenAPISecurity) (*api.Resource, *urls.Router) {
	t.Helper()
	resource, err := api.NewResource(config)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := resource.ReadOpenAPIRoutes("books/", "book", api.ReadOpenAPIOptions{Security: security})
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Include("api/", "v1", routes...))
	if err != nil {
		t.Fatal(err)
	}
	return resource, router
}

func openAPINativeJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("invalid JSON %q: %v", data, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatal("trailing JSON", err)
	}
	return value
}

func openAPINativeMap(t *testing.T, value any) map[string]any {
	t.Helper()
	m, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON object, got %T: %v", value, value)
	}
	return m
}

func openAPINativeKeys(t *testing.T, value map[string]any, expected ...string) {
	t.Helper()
	actual := make([]string, 0, len(value))
	for key := range value {
		actual = append(actual, key)
	}
	slices.Sort(actual)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		t.Fatalf("keys = %v, want %v", actual, expected)
	}
}

func openAPINativeOperation(t *testing.T, doc map[string]any, path, method string) map[string]any {
	t.Helper()
	return openAPINativeMap(t, openAPINativeMap(t, openAPINativeMap(t, doc["paths"])[path])[method])
}

func openAPINativeResponse(t *testing.T, operation map[string]any, status string) map[string]any {
	t.Helper()
	return openAPINativeMap(t, openAPINativeMap(t, operation["responses"])[status])
}

func openAPINativeSchema(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	return openAPINativeMap(t, openAPINativeMap(t, openAPINativeMap(t, response["content"])["application/json"])["schema"])
}

func openAPINativeCall(router *urls.Router, method, path, etag string, principal auth.Principal) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://example.test"+path, nil)
	if principal.Authenticated {
		r = r.WithContext(auth.WithPrincipal(r.Context(), principal))
	}
	if etag != "" {
		r.Header.Set("If-None-Match", etag)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

// These are independent assertions of the emitted vocabulary and actual native
// responses, not a claim that this test implements a complete OpenAPI validator.
func TestPostgresAPIOpenAPIBoundDocumentMatchesScopedLiveResponses(t *testing.T) {
	f := newOpenAPINativeFixture(t)
	resource, router := openAPINativeBind(t, f.config, []api.OpenAPISecurity{{Type: "public"}})
	options := api.OpenAPIOptions{Title: "Books API", Version: "1.0"}
	document, err := api.OpenAPI(context.Background(), router, options)
	if err != nil {
		t.Fatal(err)
	}
	if f.backend.queries != 0 || f.policyCalls != 0 || f.scopeCalls != 0 || f.fieldCalls != 0 {
		t.Fatal("binding or generation executed runtime callbacks or SQL")
	}
	doc := openAPINativeJSON(t, document)
	if doc["openapi"] != "3.1.1" || doc["jsonSchemaDialect"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatal("document version or dialect", doc)
	}
	openAPINativeKeys(t, openAPINativeMap(t, doc["paths"]), "/api/books/", "/api/books/{pk}/")
	for _, forbidden := range []string{"internal_title", "password_hash", "internal_profile", "internal_note", "display_name", "extra_note", "private_nested", "tenant"} {
		if bytes.Contains(document, []byte(forbidden)) {
			t.Fatal("private source metadata escaped", forbidden)
		}
	}
	listOperation := openAPINativeOperation(t, doc, "/api/books/", "get")
	detailOperation := openAPINativeOperation(t, doc, "/api/books/{pk}/", "get")
	if listOperation["operationId"] != "v1:book_list.get" || detailOperation["operationId"] != "v1:book_detail.get" {
		t.Fatal("flattened name provenance was lost")
	}
	listSchema := openAPINativeSchema(t, openAPINativeResponse(t, listOperation, "200"))
	properties := openAPINativeMap(t, listSchema["properties"])
	openAPINativeKeys(t, properties, "results", "count", "next", "previous")
	if listSchema["additionalProperties"] != false || !reflect.DeepEqual(listSchema["required"], []any{"results", "count"}) {
		t.Fatal("collection envelope differs", listSchema)
	}
	resultsSchema := openAPINativeMap(t, properties["results"])
	itemSchema := openAPINativeMap(t, resultsSchema["items"])
	if resultsSchema["type"] != "array" || itemSchema["$schema"] != nil || itemSchema["required"] != nil || itemSchema["additionalProperties"] != false {
		t.Fatal("inline schema dialect or root redaction contract", itemSchema)
	}
	itemProperties := openAPINativeMap(t, itemSchema["properties"])
	openAPINativeKeys(t, itemProperties, "id", "caption", "profile", "redacted")
	if !reflect.DeepEqual(openAPINativeMap(t, itemProperties["id"])["type"], []any{"integer", "null"}) {
		t.Fatal("read-only primary key output omitted or changed type")
	}
	nestedSchema := openAPINativeMap(t, itemProperties["profile"])
	openAPINativeKeys(t, openAPINativeMap(t, nestedSchema["properties"]), "display", "note")
	if !reflect.DeepEqual(nestedSchema["required"], []any{"display"}) || nestedSchema["additionalProperties"] != false {
		t.Fatal("root redaction widened the nested required contract", nestedSchema)
	}
	if !reflect.DeepEqual(itemSchema, openAPINativeSchema(t, openAPINativeResponse(t, detailOperation, "200"))) {
		t.Fatal("detail and collection use different output definitions")
	}
	// Verify normalized pagination defaults in the document against a request
	// without query parameters, not against the unnormalized input Config.
	parameters, ok := listOperation["parameters"].([]any)
	if !ok {
		t.Fatal("missing collection parameters")
	}
	foundSize := false
	for _, raw := range parameters {
		parameter := openAPINativeMap(t, raw)
		if parameter["name"] == "page_size" {
			foundSize = true
			schema := openAPINativeMap(t, parameter["schema"])
			if schema["default"] != json.Number("1") || schema["maximum"] != json.Number("200") {
				t.Fatal("normalized pagination metadata", schema)
			}
		}
	}
	if !foundSize {
		t.Fatal("page_size missing")
	}
	// Replacing each original exported handle cannot retarget already bound
	// live handlers or their private root/nested output descriptor snapshots.
	*resource = api.Resource{}
	*f.config.Serializer = api.Serializer{}
	*f.nested = api.Serializer{}
	*f.config.Store = orm.Store{}
	again, err := api.OpenAPI(context.Background(), router, options)
	if err != nil || !bytes.Equal(document, again) {
		t.Fatal("caller replacement changed the sealed document", err)
	}
	first := openAPINativeCall(router, "GET", "/api/books/", "", auth.Principal{})
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	page := openAPINativeJSON(t, first.Body.Bytes())
	openAPINativeKeys(t, page, "results", "count", "next")
	rows, ok := page["results"].([]any)
	if !ok || len(rows) != 1 || page["count"] != json.Number("2") {
		t.Fatal("count/page escaped visibility or default size", page)
	}
	checkObject := func(raw any, id, title, name string, note bool) {
		t.Helper()
		object := openAPINativeMap(t, raw)
		openAPINativeKeys(t, object, "id", "caption", "profile")
		if object["id"] != json.Number(id) || object["caption"] != title {
			t.Fatal("wrong object", object)
		}
		profile := openAPINativeMap(t, object["profile"])
		if note {
			openAPINativeKeys(t, profile, "display", "note")
			if profile["note"] != "Visible note" {
				t.Fatal(profile)
			}
		} else {
			openAPINativeKeys(t, profile, "display")
		}
		if profile["display"] != name {
			t.Fatal(profile)
		}
	}
	checkObject(rows[0], "1", "First", "Ada", false)
	next, ok := page["next"].(string)
	if !ok || next == "" {
		t.Fatal("missing next link", page)
	}
	second := openAPINativeCall(router, "GET", "/api/books/"+next, "", auth.Principal{})
	if second.Code != 200 {
		t.Fatal(second.Code, second.Body.String())
	}
	page = openAPINativeJSON(t, second.Body.Bytes())
	openAPINativeKeys(t, page, "results", "count", "previous")
	rows, ok = page["results"].([]any)
	if !ok || len(rows) != 1 || page["count"] != json.Number("2") || page["previous"] == "" {
		t.Fatal(page)
	}
	checkObject(rows[0], "2", "Second", "Bea", true)
	detail := openAPINativeCall(router, "GET", "/api/books/1/", "", auth.Principal{})
	if detail.Code != 200 || detail.Header().Get("ETag") == "" {
		t.Fatal(detail.Code, detail.Body.String(), detail.Header())
	}
	checkObject(openAPINativeJSON(t, detail.Body.Bytes()), "1", "First", "Ada", false)
	beforeReads, beforeGrants := f.backend.queries, f.policyCalls
	unchanged := openAPINativeCall(router, "GET", "/api/books/1/", detail.Header().Get("ETag"), auth.Principal{})
	if unchanged.Code != 304 || unchanged.Body.Len() != 0 || f.backend.queries <= beforeReads || f.policyCalls <= beforeGrants {
		t.Fatal("conditional response skipped current reads/grants", unchanged.Code)
	}
	if openAPINativeResponse(t, detailOperation, "304")["content"] != nil {
		t.Fatal("304 documented a body")
	}
	for _, path := range []string{"/api/books/", "/api/books/1/"} {
		docPath := path
		if path != "/api/books/" {
			docPath = "/api/books/{pk}/"
		}
		head := openAPINativeCall(router, "HEAD", path, "", auth.Principal{})
		if head.Code != 200 || head.Body.Len() != 0 {
			t.Fatal("HEAD response", head.Code, head.Body.String())
		}
		headOperation := openAPINativeOperation(t, doc, docPath, "head")
		for _, status := range []string{"200", "default"} {
			if openAPINativeResponse(t, headOperation, status)["content"] != nil {
				t.Fatal("HEAD documented a body", status)
			}
		}
		queries, policies, scopes, fields := f.backend.queries, f.policyCalls, f.scopeCalls, f.fieldCalls
		options := openAPINativeCall(router, "OPTIONS", path, "", auth.Principal{})
		if options.Code != 204 || options.Body.Len() != 0 || options.Header().Get("Allow") != "GET, HEAD, OPTIONS" || f.backend.queries != queries || f.policyCalls != policies || f.scopeCalls != scopes || f.fieldCalls != fields {
			t.Fatal("router OPTIONS invoked resource or sent body", options.Code, options.Header())
		}
		operation := openAPINativeOperation(t, doc, docPath, "options")
		if !reflect.DeepEqual(operation["security"], []any{}) || openAPINativeResponse(t, operation, "204")["content"] != nil {
			t.Fatal("OPTIONS contract", operation)
		}
	}
	for _, path := range []string{"/api/books/3/", "/api/books/99/"} {
		missing := openAPINativeCall(router, "GET", path, "", auth.Principal{})
		if missing.Code != 404 {
			t.Fatal("scoped absence", path, missing.Code, missing.Body.String())
		}
		errorBody := openAPINativeJSON(t, missing.Body.Bytes())
		openAPINativeKeys(t, errorBody, "code", "detail")
		if _, ok := errorBody["code"].(string); !ok {
			t.Fatal("error code is not a string")
		}
	}
	unsafe := openAPINativeCall(router, "POST", "/api/books/", "", auth.Principal{})
	defaultContent := openAPINativeMap(t, openAPINativeResponse(t, listOperation, "default")["content"])
	if unsafe.Code != 405 || !strings.HasPrefix(unsafe.Header().Get("Content-Type"), "text/plain") || defaultContent["text/plain"] == nil || defaultContent["application/json"] == nil {
		t.Fatal("router error media mismatch", unsafe.Code, unsafe.Header())
	}
	f.backend.failAt = f.backend.queries + 1
	unavailable := openAPINativeCall(router, "GET", "/api/books/", "", auth.Principal{})
	f.backend.failAt = 0
	if unavailable.Code != 503 || strings.Contains(unavailable.Body.String(), "private") || strings.Contains(unavailable.Body.String(), "results") {
		t.Fatal("provider failure leaked a representation", unavailable.Code, unavailable.Body.String())
	}
	f.deny = true
	beforeReads = f.backend.queries
	denied := openAPINativeCall(router, "GET", "/api/books/1/", detail.Header().Get("ETag"), auth.Principal{})
	if denied.Code != 403 || denied.Body.Len() == 0 || f.backend.queries != beforeReads {
		t.Fatal("cached validator bypassed current request denial", denied.Code)
	}
}

func TestPostgresAPIOpenAPISecurityAssertionsDoNotInstallAuthentication(t *testing.T) {
	f := newOpenAPINativeFixture(t)
	f.config.AllowAnonymous = false
	_, router := openAPINativeBind(t, f.config, []api.OpenAPISecurity{{Type: "session", CookieName: "books_session"}, {Type: "bearer"}})
	document, err := api.OpenAPI(context.Background(), router, api.OpenAPIOptions{Title: "Private books", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	doc := openAPINativeJSON(t, document)
	schemes := openAPINativeMap(t, openAPINativeMap(t, doc["components"])["securitySchemes"])
	if len(schemes) != 2 {
		t.Fatal("security alternatives collapsed", schemes)
	}
	sessionName := ""
	for name, raw := range schemes {
		scheme := openAPINativeMap(t, raw)
		if name == "bearer" {
			if scheme["type"] != "http" || scheme["scheme"] != "bearer" {
				t.Fatal(scheme)
			}
		} else {
			sessionName = name
			if scheme["type"] != "apiKey" || scheme["in"] != "cookie" || scheme["name"] != "books_session" {
				t.Fatal(scheme)
			}
		}
	}
	security := openAPINativeOperation(t, doc, "/api/books/", "get")["security"]
	want := []any{map[string]any{"bearer": []any{}}, map[string]any{sessionName: []any{}}}
	if sessionName == "" || !reflect.DeepEqual(security, want) {
		t.Fatal("security is not explicit OR", security)
	}
	for _, attach := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer fixture-not-a-credential") },
		func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "books_session", Value: "fixture-not-a-session"})
		},
	} {
		r := httptest.NewRequest("GET", "/api/books/", nil)
		attach(r)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 401 || f.backend.queries != 0 || f.policyCalls != 0 {
			t.Fatal("documentation declaration trusted raw identity", w.Code)
		}
	}
	verified := auth.Principal{ID: "one", Authenticated: true, Active: true}
	out := openAPINativeCall(router, "GET", "/api/books/", "", verified)
	if out.Code != 200 || f.backend.queries == 0 {
		t.Fatal("verified middleware principal could not read", out.Code, out.Body.String())
	}
}
