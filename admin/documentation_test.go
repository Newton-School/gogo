package admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/templates"
)

type documentationNoStore struct{}

func (documentationNoStore) Scope(context.Context, auth.Principal, string, models.Schema) (ScopedStore, error) {
	panic("documentation queried model data")
}

func documentationTestSite(t *testing.T, configure func(*Config)) *Site {
	t.Helper()
	key, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "documentation-test", Value: []byte(key)}, nil, "documentation-test")
	if err != nil {
		t.Fatal(err)
	}
	config := Config{Store: documentationNoStore{}, Policy: auth.ModelPolicy{}, Signer: signer}
	if configure != nil {
		configure(&config)
	}
	site, err := NewSite(config)
	if err != nil {
		t.Fatal(err)
	}
	schema := (&testRecord{}).Schema()
	schema.Fields = append(schema.Fields, models.Field{Name: "Updated", Kind: models.DateTime, Editable: true, AutoNow: true})
	if err := site.Register(ModelAdmin{Schema: schema, Fields: []string{"Name"}}); err != nil {
		t.Fatal(err)
	}
	return site
}

func documentationTestOptions() DocumentationOptions {
	return DocumentationOptions{
		Models: []DocumentationModel{{Key: "shop.Product", Description: "Selected model", Fields: []DocumentationField{{Name: "Name", Description: "Chosen field help"}}}},
		Routes: []DocumentationRoute{{Name: "shop:detail", Pattern: "/shop/<int:id>/", Methods: []string{"GET"}}},
		Views:  []DocumentationView{{Route: "shop:detail", Title: "Declared view", Description: "View help"}},
		Tags:   []DocumentationExtension{{Name: "if", Description: "Conditional syntax"}}, Filters: []DocumentationExtension{{Name: "lower"}},
	}
}

func documentationTestPrincipal() auth.Principal {
	return auth.Principal{ID: "staff", AuthVersion: 1, Authenticated: true, Active: true, Staff: true,
		Permissions: []string{DocumentationPermission, "shop.view_product"}}
}

func documentationRequest(handler http.Handler, method, target string, principal auth.Principal) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://example.test"+target, nil)
	r.Header.Set("If-None-Match", "*")
	r.Header.Set("If-Modified-Since", "Wed, 01 Jan 2100 00:00:00 GMT")
	r = r.WithContext(auth.WithPrincipal(r.Context(), principal))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestDocumentationAccessAndExactRoute(t *testing.T) {
	site := documentationTestSite(t, nil)
	h, err := site.DocumentationHandler(documentationTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"anonymous", "inactive", "nonstaff", "no docs grant", "token ceiling", "no model grant", "permitted"} {
		t.Run(mode, func(t *testing.T) {
			p := documentationTestPrincipal()
			switch mode {
			case "anonymous":
				p.Authenticated = false
			case "inactive":
				p.Active = false
			case "nonstaff":
				p.Staff = false
			case "no docs grant":
				p.Permissions = []string{"shop.view_product"}
			case "token ceiling":
				p, err = auth.ConstrainPrincipal(p, []string{"shop.view_product"})
				if err != nil {
					t.Fatal(err)
				}
			case "no model grant":
				p.Permissions = []string{DocumentationPermission}
			}
			w := documentationRequest(h, "GET", "/admin/doc/", p)
			want := 403
			if mode == "permitted" || mode == "no model grant" {
				want = 200
			}
			if w.Code != want || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal(w.Code, w.Header(), w.Body.String())
			}
			if mode != "permitted" && strings.Contains(w.Body.String(), "Selected model") || want != 200 && strings.Contains(w.Body.String(), "Declared view") {
				t.Fatal("denied reference metadata disclosed", w.Body.String())
			}
		})
	}
	for _, target := range []string{"/doc/", "/other/admin/doc/", "/admin/doc", "/admin/doc/extra/", "/admin/%64oc/"} {
		if w := documentationRequest(h, "GET", target, documentationTestPrincipal()); w.Code != 404 || strings.Contains(w.Body.String(), "Declared view") {
			t.Fatal("wrong mount exposed topology", target, w.Code)
		}
	}
	if w := documentationRequest(h, "POST", "/admin/doc/", documentationTestPrincipal()); w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal(w.Code, w.Header())
	}
	if w := documentationRequest(h, "HEAD", "/admin/doc/", documentationTestPrincipal()); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("HEAD/conditional contract", w.Code, w.Body.String())
	}
	if err := site.Unregister("shop.Product"); err == nil {
		t.Fatal("documentation did not freeze registration")
	}
}

type documentationPoison struct{}

func (documentationPoison) MarshalJSON() ([]byte, error) { panic("private schema value") }
func (documentationPoison) String() string               { panic("private schema value") }
func (documentationPoison) Encode(any) (any, error)      { panic("private codec") }
func (documentationPoison) Decode(any) (any, error)      { panic("private codec") }

func TestDocumentationProjectionAndFrozenMetadata(t *testing.T) {
	site := documentationTestSite(t, nil)
	registered := site.models["shop.Product"]
	registered.Schema.Comment = "private schema comment"
	field := &registered.Schema.Fields[2]
	field.Default, field.Min, field.Max, field.Codec = documentationPoison{}, documentationPoison{}, documentationPoison{}, documentationPoison{}
	field.DBDefault, field.GeneratedExpression, field.HelpText = "private db default", "private generated SQL", "implicit schema help"
	field.DefaultFunc = func() any { panic("default called") }
	field.Validators = []models.Validator{func(context.Context, any) error { panic("validator called") }}
	field.Choices = []models.Choice{{Value: documentationPoison{}, Label: "private choice"}}
	options := documentationTestOptions()
	options.Models[0].Fields[0].Description = `<script>description()</script>`
	h, err := site.DocumentationHandler(options)
	if err != nil {
		t.Fatal(err)
	}
	options.Models[0].Fields[0].Name = "Secret"
	options.Models[0].Description = "mutated model description"
	options.Routes[0].Methods[0] = "MUTATED"
	options.Views[0].Title = "mutated view title"
	options.Tags[0].Description = "mutated tag description"
	field.Name, field.Kind = "mutated field", models.Custom
	w := documentationRequest(h, "GET", "/admin/doc/", documentationTestPrincipal())
	if w.Code != 200 || !strings.Contains(w.Body.String(), "&lt;script&gt;description()&lt;/script&gt;") || !strings.Contains(w.Body.String(), "Declared view") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, unwanted := range []string{"<script>description()", "private schema", "private choice", "private db", "private generated", "implicit schema help", "mutated", "MUTATED", "Secret"} {
		if strings.Contains(w.Body.String(), unwanted) {
			t.Fatal("unselected/unsafe/mutable metadata leaked", unwanted)
		}
	}
}

type documentationOpaqueError struct{}

func (documentationOpaqueError) Error() string { panic("private Error callback") }
func (documentationOpaqueError) Is(error) bool { panic("private Is callback") }
func (documentationOpaqueError) As(any) bool   { panic("private As callback") }

func TestDocumentationFinalGrantsAndSafeOperationalErrors(t *testing.T) {
	for _, mode := range []string{"docs revoke", "model revoke", "opaque error", "joined denial", "policy panic", "label panic", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			revoked := false
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			site := documentationTestSite(t, func(c *Config) {
				c.Policy = auth.PolicyFunc(func(ctx context.Context, p auth.Principal, action string, resource auth.Resource) error {
					if revoked {
						switch mode {
						case "docs revoke":
							if resource.App == "admindocs" {
								return auth.ErrPermissionDenied
							}
						case "model revoke":
							if resource.App == "shop" {
								return auth.ErrPermissionDenied
							}
						case "opaque error":
							return documentationOpaqueError{}
						case "joined denial":
							return errors.Join(auth.ErrPermissionDenied, errors.New("private provider failure"))
						case "policy panic":
							panic("private policy panic")
						}
					}
					return (auth.ModelPolicy{}).Authorize(ctx, p, action, resource)
				})
				c.ActorLabel = func(context.Context, auth.Principal) (string, error) {
					revoked = true
					if mode == "label panic" {
						panic("private label panic")
					}
					if mode == "cancel" {
						cancel()
					}
					return "Staff", nil
				}
			})
			h, err := site.DocumentationHandler(documentationTestOptions())
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/admin/doc/", nil).WithContext(auth.WithPrincipal(ctx, documentationTestPrincipal()))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 503
			if mode == "docs revoke" || mode == "model revoke" {
				want = 403
			}
			if w.Code != want || strings.Contains(w.Body.String(), "Declared view") || strings.Contains(w.Body.String(), "private") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

type documentationUnconsumedBody struct{}

func (documentationUnconsumedBody) Read([]byte) (int, error) { panic("body read") }
func (documentationUnconsumedBody) Close() error             { panic("body closed") }

func TestDocumentationRootPrefixEmptyBodyAndEffectiveEditable(t *testing.T) {
	site := documentationTestSite(t, func(c *Config) { c.Prefix = "/" })
	options := documentationTestOptions()
	options.Models[0].Fields = []DocumentationField{{Name: "ID"}, {Name: "Updated"}, {Name: "Name"}}
	h, err := site.DocumentationHandler(options)
	if err != nil {
		t.Fatal(err)
	}
	page := h.(*documentationPage)
	if page.models[0].fields[0].editable || page.models[0].fields[1].editable || !page.models[0].fields[2].editable {
		t.Fatal("documentation does not reflect effective field editability", page.models[0].fields)
	}
	for _, body := range []io.ReadCloser{io.NopCloser(strings.NewReader("")), documentationUnconsumedBody{}} {
		r := httptest.NewRequest("GET", "/doc/", nil).WithContext(auth.WithPrincipal(context.Background(), documentationTestPrincipal()))
		r.Body = body
		r.ContentLength = 0
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal("zero-length body handle was consumed or refused", w.Code, w.Body.String())
		}
	}
}

func TestDocumentationConfigurationBudgetsAndNoImplicitFields(t *testing.T) {
	for _, mode := range []string{"unknown model", "empty fields", "unknown field", "duplicate field", "unknown view", "large text", "large fields", "duplicate extension"} {
		t.Run(mode, func(t *testing.T) {
			site := documentationTestSite(t, nil)
			o := documentationTestOptions()
			switch mode {
			case "unknown model":
				o.Models[0].Key = "private.Unknown"
			case "empty fields":
				o.Models[0].Fields = nil
			case "unknown field":
				o.Models[0].Fields[0].Name = "Unknown"
			case "duplicate field":
				o.Models[0].Fields = append(o.Models[0].Fields, o.Models[0].Fields[0])
			case "unknown view":
				o.Views[0].Route = "unknown"
			case "large text":
				o.Views[0].Description = strings.Repeat("x", 4097)
			case "large fields":
				o.Models[0].Fields = make([]DocumentationField, 257)
			case "duplicate extension":
				o.Tags = append(o.Tags, o.Tags[0])
			}
			if h, err := site.DocumentationHandler(o); err != ErrDocumentation || h != nil {
				t.Fatal("invalid config published", h, err)
			}
			if site.frozen {
				t.Fatal("failed documentation registration froze Site")
			}
		})
	}
}

func TestDocumentationConcurrentRequestIsolation(t *testing.T) {
	site := documentationTestSite(t, nil)
	h, err := site.DocumentationHandler(documentationTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := documentationTestPrincipal()
			p.Permissions = slices.Clone(p.Permissions)
			want := 200
			if i%2 != 0 {
				p.Permissions = nil
				want = 403
			}
			w := documentationRequest(h, "GET", "/admin/doc/", p)
			if w.Code != want || want == 403 && strings.Contains(w.Body.String(), "Selected model") {
				t.Error("request permission isolation", w.Code)
			}
		}()
	}
	wg.Wait()
}

// The dedicated page uses the existing Site engine and fixed template, including
// project loader overrides. A provider failure cannot fall back to other data.
func TestDocumentationUsesExistingTemplatesAndFinalLoaderFence(t *testing.T) {
	denied := false
	site := documentationTestSite(t, func(c *Config) {
		c.Policy = auth.PolicyFunc(func(ctx context.Context, p auth.Principal, action string, r auth.Resource) error {
			if denied {
				return auth.ErrPermissionDenied
			}
			return (auth.ModelPolicy{}).Authorize(ctx, p, action, r)
		})
		c.TemplateLoaders = []templates.Loader{documentationTestLoader(func(_ context.Context, name string) (string, error) {
			if name != "documentation.html" {
				t.Error("unexpected requested template", name)
			}
			denied = true
			return `<h1>{{ title }}</h1><p>private rendered descriptor</p>`, nil
		})}
	})
	h, err := site.DocumentationHandler(documentationTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	w := documentationRequest(h, "GET", "/admin/doc/", documentationTestPrincipal())
	if w.Code != 403 || strings.Contains(w.Body.String(), "private rendered") {
		t.Fatal(w.Code, w.Body.String())
	}
}

type documentationTestLoader func(context.Context, string) (string, error)

func (f documentationTestLoader) Load(ctx context.Context, name string) (string, error) {
	return f(ctx, name)
}

type documentationBrokenWriter struct {
	header          http.Header
	mode            string
	headers, writes int
}

func (w *documentationBrokenWriter) Header() http.Header { return w.header }
func (w *documentationBrokenWriter) WriteHeader(int) {
	w.headers++
	if w.mode == "header panic" {
		panic("private writer panic")
	}
}
func (w *documentationBrokenWriter) Write(b []byte) (int, error) {
	w.writes++
	switch w.mode {
	case "write panic":
		panic("private writer panic")
	case "short":
		return len(b) - 1, nil
	case "overcount":
		return len(b) + 1, nil
	default:
		return 0, errors.New("private write error")
	}
}

func TestDocumentationTransferFailureNeverAppendsSecondResponse(t *testing.T) {
	site := documentationTestSite(t, nil)
	h, err := site.DocumentationHandler(documentationTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, authorized := range []bool{true, false} {
		for _, mode := range []string{"header panic", "write panic", "short", "overcount", "error"} {
			t.Run(mode, func(t *testing.T) {
				p := documentationTestPrincipal()
				if !authorized {
					p.Permissions = nil
				}
				r := httptest.NewRequest("GET", "/admin/doc/", nil).WithContext(auth.WithPrincipal(context.Background(), p))
				w := &documentationBrokenWriter{header: make(http.Header), mode: mode}
				func() {
					defer func() {
						if recover() != http.ErrAbortHandler {
							t.Error("missing transfer abort")
						}
					}()
					h.ServeHTTP(w, r)
				}()
				if w.headers != 1 || w.writes > 1 {
					t.Fatal("appended response", w.headers, w.writes)
				}
			})
		}
	}
}

type documentationBadContext struct {
	context.Context
	valuePanic bool
}

func (c documentationBadContext) Err() error {
	if !c.valuePanic {
		panic("private context error")
	}
	return nil
}
func (c documentationBadContext) Value(key any) any {
	if c.valuePanic {
		panic("private context value")
	}
	return c.Context.Value(key)
}

func TestDocumentationContextFailureAndHEADDoNotDisclose(t *testing.T) {
	site := documentationTestSite(t, nil)
	h, err := site.DocumentationHandler(documentationTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, valuePanic := range []bool{false, true} {
		for _, method := range []string{"GET", "HEAD"} {
			ctx := documentationBadContext{Context: auth.WithPrincipal(context.Background(), documentationTestPrincipal()), valuePanic: valuePanic}
			r := httptest.NewRequest(method, "/admin/doc/", nil).WithContext(ctx)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 503 || strings.Contains(w.Body.String(), "private") || method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
}

func TestDocumentationMalformedHEADHasNoBody(t *testing.T) {
	site := documentationTestSite(t, nil)
	h, err := site.DocumentationHandler(documentationTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		r := httptest.NewRequest(method, "/admin/doc/", nil)
		r.URL = nil
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || w.Header().Get("Cache-Control") != "private, no-store" ||
			method == http.MethodHead && w.Body.Len() != 0 || method == http.MethodGet && w.Body.String() != "Invalid request" {
			t.Fatal(method, w.Code, w.Header(), w.Body.String())
		}
	}
}

func TestDocumentationHEADSuppressesMiddlewareErrorBody(t *testing.T) {
	site := documentationTestSite(t, nil)
	h, err := site.DocumentationHandler(documentationTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	// Standard middleware may write an error directly instead of calling the
	// page writer. The outer response boundary must preserve HEAD there too.
	h.(*documentationPage).protected = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Unavailable", http.StatusServiceUnavailable)
	})
	w := documentationRequest(h, "HEAD", "/admin/doc/", documentationTestPrincipal())
	if w.Code != 503 || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
}
