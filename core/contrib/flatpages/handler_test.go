package flatpages

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

const httpSiteID = "00000000-0000-4000-8000-000000000001"
const httpPageID = "00000000-0000-4000-8000-000000000002"

func pageHTTPFixture(events *[]string) *pageHandler {
	return &pageHandler{
		current: func(*http.Request) (sites.Info, error) {
			*events = append(*events, "site")
			return sites.Info{ID: httpSiteID, Domain: "example.test", DisplayName: "Example", Active: true}, nil
		},
		lookup: func(_ context.Context, in LookupInput) (Info, bool, error) {
			*events = append(*events, "lookup")
			if in.SiteID != httpSiteID || in.URL != "/about" {
				return Info{}, false, ErrInvalid
			}
			return Info{ID: httpPageID, URL: "/about", Title: "About", Content: "Hello"}, true, nil
		},
		render: func(context.Context, Info, sites.Info) (string, error) {
			*events = append(*events, "render")
			return "<p>Hello</p>", nil
		},
		options: HandlerOptions{Timeout: time.Second, Authorize: func(context.Context, sites.Info, Info) error { *events = append(*events, "authorize"); return nil }},
	}
}

func pageHTTPServe(h http.Handler, method, target string, ctx context.Context) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	if ctx != nil {
		r = r.WithContext(ctx)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestFlatpageHTTPScopesPathIgnoresQueryAndRendersHead(t *testing.T) {
	for _, method := range []string{"GET", "HEAD"} {
		for _, path := range []string{"/about", "/%61bout?different=page", "/about?invalid=%zz"} {
			var events []string
			h := pageHTTPFixture(&events)
			w := pageHTTPServe(h, method, "http://example.test"+path, nil)
			if w.Code != 200 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Content-Length") != strconv.Itoa(len("<p>Hello</p>")) {
				t.Fatal(w.Code, w.Header())
			}
			if method == "GET" && w.Body.String() != "<p>Hello</p>" || method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal(w.Body.String())
			}
			if !reflect.DeepEqual(events, []string{"site", "lookup", "authorize", "render", "authorize"}) {
				t.Fatal(events)
			}
		}
	}
}

func TestFlatpageHTTPPrivatePageRequiresVerifiedActiveIdentity(t *testing.T) {
	for _, principal := range []auth.Principal{
		{}, {ID: "user", Active: true}, {ID: "user", Authenticated: true}, {Authenticated: true, Active: true}, {ID: "user", Authenticated: true, Active: true},
	} {
		var events []string
		h := pageHTTPFixture(&events)
		lookup := h.lookup
		h.lookup = func(ctx context.Context, in LookupInput) (Info, bool, error) {
			page, found, err := lookup(ctx, in)
			page.RegistrationRequired = true
			return page, found, err
		}
		ctx := auth.WithPrincipal(context.Background(), principal)
		w := pageHTTPServe(h, "GET", "http://example.test/about", ctx)
		if principal.ID != "" && principal.Authenticated && principal.Active {
			if w.Code != 200 {
				t.Fatal(w.Code)
			}
		} else if w.Code != 404 || w.Body.Len() != 0 || !reflect.DeepEqual(events, []string{"site", "lookup"}) {
			t.Fatal(w.Code, w.Body.String(), events)
		}
	}
}

func TestFlatpageHTTPNeverDisclosesFailedOrDeniedRendering(t *testing.T) {
	for _, stage := range []string{"site", "lookup", "first grant", "render", "last grant"} {
		for _, failure := range []string{"error", "deny", "joined", "panic", "cancel"} {
			t.Run(stage+"/"+failure, func(t *testing.T) {
				var events []string
				h := pageHTTPFixture(&events)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				fail := func() error {
					switch failure {
					case "deny":
						return ErrForbidden
					case "joined":
						return errors.Join(ErrForbidden, errors.New("private provider detail"))
					case "panic":
						panic("private provider detail")
					case "cancel":
						cancel()
						return nil
					}
					return errors.New("private provider detail")
				}
				switch stage {
				case "site":
					h.current = func(*http.Request) (sites.Info, error) { return sites.Info{}, fail() }
				case "lookup":
					h.lookup = func(context.Context, LookupInput) (Info, bool, error) {
						return Info{Content: "private body"}, true, fail()
					}
				case "render":
					h.render = func(context.Context, Info, sites.Info) (string, error) { return "private body", fail() }
				default:
					calls := 0
					h.options.Authorize = func(context.Context, sites.Info, Info) error {
						calls++
						if stage == "first grant" || calls == 2 {
							return fail()
						}
						return nil
					}
				}
				w := pageHTTPServe(h, "GET", "http://example.test/about", ctx)
				want := 503
				if failure == "deny" && (stage == "first grant" || stage == "render" || stage == "last grant") {
					want = 404
				}
				if w.Code != want || w.Body.Len() != 0 || w.Header().Get("Location") != "" || w.Header().Get("Cache-Control") != "private, no-store" {
					t.Fatal(w.Code, w.Header(), w.Body.String())
				}
			})
		}
	}
}

type unreadPageBody struct{ reads int }

func (b *unreadPageBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (*unreadPageBody) Close() error               { return nil }

func TestFlatpageHTTPMethodBodyAndPathRejectionBeforeReads(t *testing.T) {
	for _, change := range []func(*http.Request){
		func(r *http.Request) { r.Method = "POST" },
		func(r *http.Request) { r.URL = nil },
		func(r *http.Request) { r.URL.RawPath = "/elsewhere" },
		func(r *http.Request) { r.URL.Path = "/../about" },
		func(r *http.Request) { r.URL.Fragment = "invalid" },
		func(r *http.Request) { r.URL.Host = "other.test" },
		func(r *http.Request) { r.URL.Path = "/" + strings.Repeat("x", MaxURLBytes) },
		func(r *http.Request) { r.URL.RawPath = "/" + strings.Repeat("%61", MaxURLBytes) },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
	} {
		var events []string
		h := pageHTTPFixture(&events)
		r := httptest.NewRequest("GET", "http://example.test/about", nil)
		change(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 400
		if r.Method == "POST" {
			want = 405
			if w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal(w.Header())
			}
		}
		if w.Code != want || len(events) != 0 {
			t.Fatal(w.Code, events)
		}
	}
	var events []string
	h := pageHTTPFixture(&events)
	body := &unreadPageBody{}
	r := httptest.NewRequest("GET", "http://example.test/about", nil)
	r.Body = body
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 || body.reads != 0 || len(events) != 0 {
		t.Fatal(w.Code, body.reads, events)
	}
}

func TestFlatpageHTTPActualTemplateKeepsContentAsData(t *testing.T) {
	var events []string
	h := pageHTTPFixture(&events)
	renderer, err := newPageRenderer(RenderOptions{Templates: templates.Config{Loaders: []templates.Loader{templates.MapLoader{"flatpages/default.html": "<h1>{{ flatpage.title }}</h1><div>{{ flatpage.content }}</div>"}}}})
	if err != nil {
		t.Fatal(err)
	}
	h.render = renderer.render
	h.lookup = func(context.Context, LookupInput) (Info, bool, error) {
		return Info{ID: httpPageID, URL: "/about", Title: "<About>", Content: "<script>bad()</script>{{ unknown }}"}, true, nil
	}
	w := pageHTTPServe(h, "GET", "http://example.test/about", nil)
	if w.Code != 200 || w.Body.String() != "<h1>&lt;About&gt;</h1><div>&lt;script&gt;bad()&lt;/script&gt;{{ unknown }}</div>" {
		t.Fatal(w.Code, w.Body.String())
	}
}

type pageMutationContext struct {
	context.Context
	change func()
}

func (c *pageMutationContext) Err() error {
	if c.change != nil {
		change := c.change
		c.change = nil
		change()
	}
	return c.Context.Err()
}

func TestFlatpageHTTPRequestAndPolicyAreCapturedBeforeCallbacks(t *testing.T) {
	var events []string
	h := pageHTTPFixture(&events)
	r := httptest.NewRequest("GET", "http://example.test/about?ignored=1", nil)
	r = r.WithContext(&pageMutationContext{Context: context.Background(), change: func() {
		r.Host = "other.test"
		r.URL.Path = "/other"
		r.Method = "POST"
		h.options.Authorize = func(context.Context, sites.Info, Info) error { return ErrForbidden }
		h.render = func(context.Context, Info, sites.Info) (string, error) { return "changed", nil }
	}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "<p>Hello</p>" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestFlatpageHTTPFallbackDoesNotInterceptMatchedViews(t *testing.T) {
	var events []string
	h := pageHTTPFixture(&events)
	router, err := urls.New(urls.Path("/view", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }), "view"))
	if err != nil {
		t.Fatal(err)
	}
	router, err = router.WithNotFound(h)
	if err != nil {
		t.Fatal(err)
	}
	if w := pageHTTPServe(router, "GET", "http://example.test/view", nil); w.Code != 404 || len(events) != 0 {
		t.Fatal(w.Code, events)
	}
	if w := pageHTTPServe(router, "GET", "http://example.test/about", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	h.lookup = func(context.Context, LookupInput) (Info, bool, error) { return Info{}, false, nil }
	h.options.NotFound = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte("custom missing"))
	})
	for _, method := range []string{"GET", "HEAD"} {
		w := pageHTTPServe(h, method, "http://example.test/about", nil)
		if w.Code != 404 || method == "GET" && w.Body.String() != "custom missing" || method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

type shortPageWriter struct{ *httptest.ResponseRecorder }

func (w shortPageWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestFlatpageHTTPAbortsPartialNetworkWrites(t *testing.T) {
	var events []string
	h := pageHTTPFixture(&events)
	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Fatal("partial response was not aborted", got)
		}
	}()
	h.ServeHTTP(shortPageWriter{httptest.NewRecorder()}, httptest.NewRequest("GET", "http://example.test/about", nil))
	t.Fatal("short write reported success")
}

type pageHandlerBackend struct{ *flatBackend }

func (b pageHandlerBackend) Query(_ context.Context, statement string, args ...any) (db.Rows, error) {
	value := b.sites[flatSiteA]
	return &flatRows{values: [][]any{{value.ID, value.Domain, value.DisplayName, value.Active}}}, nil
}

func TestFlatpageHTTPConstructorBindsServicesAndRenderer(t *testing.T) {
	backend := pageHandlerBackend{flatFixture()}
	store, err := New(Config{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	selector, err := sites.New(sites.Config{Backend: backend, AllowedHosts: []string{"example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	loader := templates.MapLoader{defaultPageTemplate: "<h1>{{ flatpage.title }}</h1>"}
	options := HandlerOptions{Sites: selector, Authorize: func(context.Context, sites.Info, Info) error { return nil }, Render: RenderOptions{Templates: templates.Config{Loaders: []templates.Loader{loader}}}}
	h, err := NewHandler(store, options)
	if err != nil {
		t.Fatal(err)
	}
	*store, *selector = Store{}, sites.Resolver{}
	loader[defaultPageTemplate] = "changed template"
	options.Authorize = func(context.Context, sites.Info, Info) error { return ErrForbidden }
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "user", Active: true, Authenticated: true})
	w := pageHTTPServe(h, "GET", "http://example.test/page/", ctx)
	if w.Code != 200 || w.Body.String() != "<h1>Page</h1>" || backend.begins != 1 {
		t.Fatal(w.Code, w.Body.String(), backend.begins)
	}
}

func TestFlatpageHTTPConstructorRejectsInvalidConfiguration(t *testing.T) {
	store, err := New(Config{Backend: flatFixture()})
	if err != nil {
		t.Fatal(err)
	}
	base := HandlerOptions{Sites: &sites.Resolver{}, Authorize: func(context.Context, sites.Info, Info) error { return nil }}
	for _, change := range []func(*HandlerOptions){
		func(o *HandlerOptions) { o.Sites = nil },
		func(o *HandlerOptions) { o.Authorize = nil },
		func(o *HandlerOptions) { o.NotFound = http.HandlerFunc(nil) },
		func(o *HandlerOptions) { o.Timeout = -time.Second },
		func(o *HandlerOptions) { o.Timeout = time.Nanosecond },
		func(o *HandlerOptions) { o.Timeout = time.Minute + time.Nanosecond },
		func(o *HandlerOptions) { o.Render.DefaultTemplate = "../private.html" },
	} {
		options := base
		change(&options)
		if h, err := NewHandler(store, options); h != nil || err != ErrConfiguration {
			t.Fatal(h, err)
		}
	}
	for _, store := range []*Store{nil, {}} {
		if h, err := NewHandler(store, base); h != nil || err != ErrConfiguration {
			t.Fatal(h, err)
		}
	}
}
