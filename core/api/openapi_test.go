package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/urls"
)

type openAPINoIOBackend struct{ db.Backend }

func (*openAPINoIOBackend) Alias() string       { panic("schema generation called backend") }
func (*openAPINoIOBackend) Dialect() db.Dialect { panic("schema generation called dialect") }

func openAPIResource(t testing.TB, serializer *Serializer) *Resource {
	t.Helper()
	schema := models.Schema{AppLabel: "library", Name: "Book", Fields: []models.Field{
		models.BigAutoField("id"), models.TextField("title"), models.TextField("private_value"),
	}}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	if serializer == nil {
		var err error
		serializer, err = New(Definition{Fields: []Field{IntegerField("id"), {Name: "display", Source: "title", Model: models.TextField("title"), Required: true}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	resource, err := NewResource(ResourceConfig{
		Store: orm.New(&openAPINoIOBackend{}, registry), Model: schema.Key(), Serializer: serializer,
		AllowAnonymous: true,
		Policy: auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error {
			panic("schema generation called policy")
		}),
		Scope: func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
			panic("schema generation called scope")
		},
		Filters: FilterConfig{Filters: []Filter{{Parameter: "ids", Field: "id", Lookup: "in"}, {Parameter: "span", Field: "id", Lookup: "range"}}, SearchFields: []string{"title"}, OrderingFields: []string{"title"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resource
}

func openAPIBound(t testing.TB, resource *Resource) []urls.Route {
	t.Helper()
	routes, err := resource.ReadOpenAPIRoutes("books/", "book", ReadOpenAPIOptions{Security: []OpenAPISecurity{{Type: "public"}}})
	if err != nil {
		t.Fatal(err)
	}
	return routes
}

func openAPIRouter(t testing.TB, routes ...urls.Route) *urls.Router {
	t.Helper()
	router, err := urls.New(routes...)
	if err != nil {
		t.Fatal(err)
	}
	return router
}

var openAPITestOptions = OpenAPIOptions{Title: "Library API", Version: "1.0.0"}

func TestOpenAPIUsesPublicBoundMetadataAndNormalizedPagination(t *testing.T) {
	resource := openAPIResource(t, nil)
	resource.config.IncludeCount = true
	resource.config.EntityTags = true
	routes := openAPIBound(t, resource)
	document, err := OpenAPI(context.Background(), openAPIRouter(t, urls.Include("v1/", "v1", routes...)), openAPITestOptions)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private_value", "library.Book", "Source", "scope generation"} {
		if bytes.Contains(document, []byte(private)) {
			t.Fatal("private metadata emitted", private, string(document))
		}
	}
	var result openAPIDocument
	if err := json.Unmarshal(document, &result); err != nil {
		t.Fatal(err)
	}
	if result.OpenAPI != "3.1.1" || result.JSONSchemaDialect != outputSchemaDialect || len(result.Paths) != 2 {
		t.Fatal(string(document))
	}
	list := result.Paths["/v1/books/"]
	if len(list) != 3 || list["get"].ID != "v1:book_list.get" || len(list["head"].Responses["200"].Content) != 0 || len(list["head"].Responses["default"].Content) != 0 || len(list["options"].Security) != 0 || len(list["options"].Responses["204"].Content) != 0 {
		t.Fatal(string(document))
	}
	parameters := map[string]openAPIParameter{}
	for _, parameter := range list["get"].Parameters {
		parameters[parameter.Name] = parameter
	}
	if len(parameters) != 6 || parameters["page_size"].Schema.(map[string]any)["default"] != float64(50) || parameters["page_size"].Schema.(map[string]any)["maximum"] != float64(200) || parameters["span"].Schema.(map[string]any)["minItems"] != float64(2) || parameters["ids"].Explode == nil || !*parameters["ids"].Explode {
		t.Fatal(parameters)
	}
	detail := result.Paths["/v1/books/{pk}/"]
	properties := detail["get"].Responses["200"].Content["application/json"].Schema.(map[string]any)["properties"].(map[string]any)
	if len(properties) != 2 || properties["display"] == nil || properties["id"] == nil || properties["title"] != nil {
		t.Fatal("response schema does not match public field names", properties)
	}
	if detail["get"].Parameters[0].Schema.(map[string]any)["type"] != "string" || detail["get"].Responses["304"].Description == "" || len(detail["get"].Responses["304"].Content) != 0 || len(detail["head"].Responses["304"].Content) != 0 {
		t.Fatal(string(document))
	}
}

func TestReadOpenAPIRejectsMissingMetadataAndInvalidSecurity(t *testing.T) {
	unknown, err := New(Definition{Fields: []Field{ComputedField("unknown", func(context.Context, ValueReader) (any, error) { panic("no sampling") })}})
	if err != nil {
		t.Fatal(err)
	}
	resource := openAPIResource(t, unknown)
	if routes, err := resource.ReadOpenAPIRoutes("books/", "book", ReadOpenAPIOptions{Security: []OpenAPISecurity{{Type: "public"}}}); !errors.Is(err, ErrOpenAPI) || !errors.Is(err, ErrOutputSchema) || routes != nil {
		t.Fatal(routes, err)
	}
	resource = openAPIResource(t, nil)
	for _, security := range [][]OpenAPISecurity{
		nil, {{Type: "unknown"}}, {{Type: "session"}}, {{Type: "session", CookieName: "bad\nname"}}, {{Type: "session", CookieName: "ü"}},
		{{Type: "bearer", CookieName: "cookie"}}, {{Type: "public", CookieName: "cookie"}}, {{Type: "public"}, {Type: "public"}}, make([]OpenAPISecurity, 9),
	} {
		if routes, err := resource.ReadOpenAPIRoutes("books/", "book", ReadOpenAPIOptions{Security: security}); !errors.Is(err, ErrOpenAPI) || routes != nil {
			t.Fatal(routes, err)
		}
	}
	resource.config.AllowAnonymous = false
	if routes, err := resource.ReadOpenAPIRoutes("books/", "book", ReadOpenAPIOptions{Security: []OpenAPISecurity{{Type: "public"}}}); !errors.Is(err, ErrOpenAPI) || routes != nil {
		t.Fatal("public metadata disagrees with handler", routes, err)
	}
}

func TestReadOpenAPIFreezesHandlesAndSecurityWithoutCallbacks(t *testing.T) {
	child, _ := New(Definition{Fields: []Field{StringField("original")}})
	serializer, err := New(Definition{Fields: []Field{{Name: "nested", Source: "title", ReadOnly: true, Nested: child}}})
	if err != nil {
		t.Fatal(err)
	}
	resource := openAPIResource(t, serializer)
	declarations := []OpenAPISecurity{{Type: "session", CookieName: "custom_session"}, {Type: "bearer"}}
	routes, err := resource.ReadOpenAPIRoutes("books/", "book", ReadOpenAPIOptions{Security: declarations})
	if err != nil {
		t.Fatal(err)
	}
	router := openAPIRouter(t, routes...)
	before, err := OpenAPI(context.Background(), router, openAPITestOptions)
	if err != nil {
		t.Fatal(err)
	}
	replacement, _ := New(Definition{Fields: []Field{StringField("replacement")}})
	*child, *serializer = *replacement, *replacement
	*resource.config.Store = orm.Store{}
	*resource = Resource{}
	declarations[0].CookieName = "wrong_session"
	routes[0].Methods[0] = "POST"
	after, err := OpenAPI(context.Background(), router, openAPITestOptions)
	if err != nil || !bytes.Equal(before, after) || !bytes.Contains(after, []byte("custom_session")) || bytes.Contains(after, []byte("replacement")) || !bytes.Contains(after, []byte("original")) {
		t.Fatal(string(after), err)
	}
}

func TestOpenAPIRejectsAlteredOpaqueAndOverlappingRoutes(t *testing.T) {
	resource := openAPIResource(t, nil)
	tests := map[string]func([]urls.Route) []urls.Route{
		"opaque": func(routes []urls.Route) []urls.Route {
			routes[0].Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("opaque invoked") })
			return routes
		},
		"post":                 func(routes []urls.Route) []urls.Route { routes[0].Methods = []string{"POST"}; return routes },
		"implicit all methods": func(routes []urls.Route) []urls.Route { routes[0].Methods = nil; return routes },
		"head only":            func(routes []urls.Route) []urls.Route { routes[0].Methods = []string{"HEAD"}; return routes },
		"extra methods":        func(routes []urls.Route) []urls.Route { routes[0].Methods = []string{"GET", "HEAD"}; return routes },
		"regex":                func(routes []urls.Route) []urls.Route { routes[0].Regex = true; return routes },
		"changed suffix":       func(routes []urls.Route) []urls.Route { routes[0].Pattern = "notbooks/"; return routes },
		"changed converter":    func(routes []urls.Route) []urls.Route { routes[1].Pattern = "books/<int:pk>/"; return routes },
		"changed key name":     func(routes []urls.Route) []urls.Route { routes[1].Pattern = "books/<str:id>/"; return routes },
		"missing name":         func(routes []urls.Route) []urls.Route { routes[0].Name = ""; return routes },
		"invalid name":         func(routes []urls.Route) []urls.Route { routes[0].Name = "bad name"; return routes },
		"parameter prefix": func(routes []urls.Route) []urls.Route {
			return []urls.Route{urls.Include("<str:tenant>/", "tenant", routes...)}
		},
		"literal template braces": func(routes []urls.Route) []urls.Route {
			return []urls.Route{urls.Include("literal/{pk}/", "tenant", routes...)}
		},
		"duplicate paths": func(routes []urls.Route) []urls.Route {
			other := routes[0]
			other.Name = "other"
			return append(routes, other)
		},
		"literal shadows variable": func(routes []urls.Route) []urls.Route {
			other, e := resource.ReadOpenAPIRoutes("books/new/", "new", ReadOpenAPIOptions{Security: []OpenAPISecurity{{Type: "public"}}})
			if e != nil {
				t.Fatal(e)
			}
			return append(other, routes...)
		},
	}
	for name, alter := range tests {
		t.Run(name, func(t *testing.T) {
			router := openAPIRouter(t, alter(openAPIBound(t, resource))...)
			if document, err := OpenAPI(context.Background(), router, openAPITestOptions); !errors.Is(err, ErrOpenAPI) || document != nil {
				t.Fatal(string(document), err)
			}
		})
	}
	for _, converters := range []map[string]urls.Converter{urls.Builtins(), {"str": {Pattern: "[^/]+", Decode: func(string) (any, error) { panic("converter sampled") }, Encode: func(any) (string, error) { panic("converter sampled") }}}} {
		router, err := urls.NewWithConverters(converters, openAPIBound(t, resource)...)
		if err != nil {
			t.Fatal(err)
		}
		if document, err := OpenAPI(context.Background(), router, openAPITestOptions); !errors.Is(err, ErrOpenAPI) || document != nil {
			t.Fatal(string(document), err)
		}
	}
}

type openAPIChangingContext struct {
	context.Context
	change func()
}

func (c *openAPIChangingContext) Err() error {
	if c.change != nil {
		change := c.change
		c.change = nil
		change()
	}
	return c.Context.Err()
}

func TestOpenAPIProjectionPrecedesContextAndIsDeterministic(t *testing.T) {
	router := openAPIRouter(t, openAPIBound(t, openAPIResource(t, nil))...)
	want, err := OpenAPI(context.Background(), router, openAPITestOptions)
	if err != nil {
		t.Fatal(err)
	}
	copy := *router
	ctx := &openAPIChangingContext{Context: context.Background(), change: func() { *router = urls.Router{} }}
	got, err := OpenAPI(ctx, router, openAPITestOptions)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal(string(got), err)
	}
	*router = copy
	got[0] = 'x'
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for n := 0; n < 8; n++ {
				value, err := OpenAPI(context.Background(), router, openAPITestOptions)
				if err != nil || !bytes.Equal(value, want) {
					t.Error("unstable independent projection", err)
					return
				}
			}
		}()
	}
	group.Wait()
	for _, options := range []OpenAPIOptions{{}, {Title: "title"}, {Title: strings.Repeat("x", 257), Version: "v"}, {Title: "title", Version: "bad\nversion"}, {Title: "\xff", Version: "v"}} {
		if value, err := OpenAPI(context.Background(), router, options); !errors.Is(err, ErrOpenAPI) || value != nil {
			t.Fatal(string(value), err)
		}
	}
	if value, err := OpenAPI(nil, router, openAPITestOptions); !errors.Is(err, ErrOpenAPI) || value != nil {
		t.Fatal(string(value), err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if value, err := OpenAPI(canceled, router, openAPITestOptions); err != context.Canceled || value != nil {
		t.Fatal(string(value), err)
	}
}

func TestReadOpenAPIBoundsCyclesRoutesAndSchemaBytes(t *testing.T) {
	resource := openAPIResource(t, nil)
	cyclic, _ := New(Definition{Fields: []Field{StringField("value")}})
	cyclic.fields[0].Nested = cyclic
	resource.config.Serializer = cyclic
	if value, err := resource.ReadOpenAPIRoutes("books/", "book", ReadOpenAPIOptions{Security: []OpenAPISecurity{{Type: "public"}}}); !errors.Is(err, ErrOpenAPI) || value != nil {
		t.Fatal(value, err)
	}
	resource = openAPIResource(t, nil)
	one := openAPIBound(t, resource)[0]
	routes := make([]urls.Route, 257)
	for i := range routes {
		routes[i] = one
		routes[i].Name = fmt.Sprintf("route_%d", i)
	}
	if value, err := OpenAPI(context.Background(), openAPIRouter(t, routes...), openAPITestOptions); !errors.Is(err, ErrOpenAPI) || value != nil {
		t.Fatal(string(value), err)
	}
	// This package-private fixture isolates the emitter's aggregate byte check;
	// public bindings additionally enforce the per-serializer 1-MiB contract.
	handler := *one.Handler.(*readOpenAPIHandler)
	handler.output = strings.Repeat(" ", openAPIMaxBytes) + "{}"
	one.Handler = &handler
	if value, err := OpenAPI(context.Background(), openAPIRouter(t, one), openAPITestOptions); !errors.Is(err, ErrOpenAPI) || value != nil {
		t.Fatal(string(value), err)
	}
}

func TestOpenAPIContextFailuresReturnNoDocument(t *testing.T) {
	router := openAPIRouter(t, openAPIBound(t, openAPIResource(t, nil))...)
	expired, cancel := context.WithDeadline(context.Background(), time.Time{})
	defer cancel()
	var typedNil *outputSchemaContext
	for _, test := range []struct {
		ctx  context.Context
		want error
	}{
		{typedNil, ErrOpenAPI}, {expired, context.DeadlineExceeded},
		{&outputSchemaContext{err: func() error { panic("private context panic") }}, ErrOpenAPI},
		{&outputSchemaContext{err: func() error { return errors.New("private context error") }}, ErrOpenAPI},
	} {
		if document, err := OpenAPI(test.ctx, router, openAPITestOptions); err != test.want || document != nil {
			t.Fatal("context failure returned output or an unsafe cause", string(document), err)
		}
	}
	calls := 0
	ctx := &outputSchemaContext{err: func() error {
		calls++
		if calls == 2 {
			return context.Canceled
		}
		return nil
	}}
	if document, err := OpenAPI(ctx, router, openAPITestOptions); err != context.Canceled || document != nil || calls != 2 {
		t.Fatal("late cancellation retained encoded bytes", string(document), err, calls)
	}
}

func TestOpenAPIPaginationModesAndOptionalCount(t *testing.T) {
	for _, mode := range []pagination.Mode{pagination.PageNumber, pagination.LimitOffset, pagination.CursorMode} {
		for _, count := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/count=%t", mode, count), func(t *testing.T) {
				config := openAPIResource(t, nil).config
				config.Filters = FilterConfig{}
				config.Pagination = pagination.Config{Mode: mode, DefaultSize: 17, MaxSize: 31, MaxOffset: 97}
				config.IncludeCount = count
				if mode == pagination.CursorMode {
					var err error
					config.Serializer, err = New(Definition{Fields: []Field{{Name: "id", Model: models.BigAutoField("id")}, StringField("title")}})
					if err != nil {
						t.Fatal(err)
					}
					// Synthetic test key; no signing or token issuance is needed to
					// describe a valid resource with a real cursor configuration.
					signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: make([]byte, 32)}, nil, "cursor-example")
					if err != nil {
						t.Fatal(err)
					}
					config.Cursor = &ResourceCursorOptions{Name: "books", Version: "v1", Signer: signer, ImmutableFields: []string{"id"}, ScopeIdentity: func(context.Context, auth.Principal, models.Schema) (string, error) {
						panic("generation invoked cursor scope identity")
					}}
				}
				resource, err := NewResource(config)
				if err != nil {
					t.Fatal(err)
				}
				document, err := OpenAPI(context.Background(), openAPIRouter(t, openAPIBound(t, resource)...), openAPITestOptions)
				if err != nil {
					t.Fatal(err)
				}
				var value openAPIDocument
				if err := json.Unmarshal(document, &value); err != nil {
					t.Fatal(err)
				}
				operation := value.Paths["/books/"]["get"]
				parameters := map[string]map[string]any{}
				for _, p := range operation.Parameters {
					parameters[p.Name] = p.Schema.(map[string]any)
				}
				keys, sizeName := []string{"page", "page_size"}, "page_size"
				switch mode {
				case pagination.PageNumber:
					if parameters["page"]["default"] != float64(1) || parameters["page"]["maximum"] != float64(98) {
						t.Fatal(parameters)
					}
				case pagination.LimitOffset:
					keys, sizeName = []string{"limit", "offset"}, "limit"
					if parameters["offset"]["default"] != float64(0) || parameters["offset"]["maximum"] != float64(97) {
						t.Fatal(parameters)
					}
				case pagination.CursorMode:
					keys = []string{"cursor", "page_size"}
					if parameters["cursor"]["minLength"] != float64(1) || parameters["cursor"]["maxLength"] != float64(pagination.MaxCursorBytes) {
						t.Fatal(parameters)
					}
				}
				actual := make([]string, 0, len(parameters))
				for key := range parameters {
					actual = append(actual, key)
				}
				slices.Sort(actual)
				page, err := resource.paginator.Parse(url.Values{})
				if !slices.Equal(actual, keys) || err != nil || page.Size != 17 || page.Offset != 0 || parameters[sizeName]["default"] != float64(page.Size) || parameters[sizeName]["maximum"] != float64(31) {
					t.Fatal(parameters, page, err)
				}
				schema := operation.Responses["200"].Content["application/json"].Schema.(map[string]any)
				properties := schema["properties"].(map[string]any)
				if (properties["count"] != nil) != count || (properties["previous"] != nil) != (mode != pagination.CursorMode) || properties["next"] == nil || properties["results"] == nil {
					t.Fatal(schema)
				}
				wantRequired := []any{"results"}
				if count {
					wantRequired = append(wantRequired, "count")
				}
				if !slices.Equal(schema["required"].([]any), wantRequired) {
					t.Fatal(schema)
				}
			})
		}
	}
}

func FuzzOpenAPIPathProjection(f *testing.F) {
	for _, prefix := range []string{"", "api/", "v1/books/", "{pk}/", "<str:tenant>/", "../", "//", "é/", "bad%2f/"} {
		f.Add(prefix, true)
		f.Add(prefix, false)
	}
	f.Fuzz(func(t *testing.T, prefix string, list bool) {
		if len(prefix) > 4096 {
			return
		}
		suffix := "/books/"
		if !list {
			suffix += "<str:pk>/"
		}
		route := urls.Route{Pattern: "/" + prefix + strings.TrimPrefix(suffix, "/")}
		path, err := openAPIPath(route, &readOpenAPIHandler{list: list, suffix: suffix})
		if err != nil {
			if path != "" || err != ErrOpenAPI {
				t.Fatal("partial or unstable failure", path, err)
			}
			return
		}
		if len(path) > 2048 || !strings.HasPrefix(path, "/") || !strings.HasSuffix(path, "/") || strings.ContainsAny(path, "<>%?#\\") || strings.Contains(path, "//") {
			t.Fatal("unrepresentable path accepted", path)
		}
		want := route.Pattern
		if !list {
			want = strings.TrimSuffix(want, "<str:pk>/") + "{pk}/"
		}
		if path != want || strings.Count(path, "{") != strings.Count(path, "}") || list && strings.Contains(path, "{") || !list && strings.Count(path, "{") != 1 {
			t.Fatal("projection changed literal routing or parameter cardinality", path)
		}
	})
}
