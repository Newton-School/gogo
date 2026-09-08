package redirects

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/urls"
)

func handlerFixture(events *[]string) *redirectHandler {
	info := sites.Info{ID: "12345678-1234-4234-9234-123456789abc", Domain: "example.test", Active: true}
	return &redirectHandler{
		current: func(*http.Request) (sites.Info, error) { *events = append(*events, "site"); return info, nil },
		lookup: func(_ context.Context, in LookupInput) (Match, bool, error) {
			*events = append(*events, "lookup")
			if in.SiteID != info.ID || in.Origin != "http://example.test" {
				return Match{}, false, ErrInvalid
			}
			return Match{SiteID: info.ID, Location: "/new", Permanent: true}, true, nil
		},
		options: HandlerOptions{Timeout: time.Second, Authorize: func(context.Context, sites.Info) error {
			*events = append(*events, "authorize")
			return nil
		}},
	}
}

func serveRedirect(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

func TestRedirectHTTPOutcomesAndFinalGrant(t *testing.T) {
	for _, test := range []struct {
		name     string
		match    Match
		found    bool
		status   int
		location string
		events   []string
	}{
		{"permanent", Match{Location: "/new", Permanent: true}, true, 301, "/new", []string{"site", "authorize", "lookup", "authorize"}},
		{"temporary", Match{Location: "/new"}, true, 302, "/new", []string{"site", "authorize", "lookup", "authorize"}},
		{"gone", Match{}, true, 410, "", []string{"site", "authorize", "lookup", "authorize"}},
		{"missing", Match{}, false, 404, "", []string{"site", "authorize", "lookup"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, method := range []string{"GET", "HEAD"} {
				var events []string
				h := handlerFixture(&events)
				h.lookup = func(context.Context, LookupInput) (Match, bool, error) {
					events = append(events, "lookup")
					return test.match, test.found, nil
				}
				response := serveRedirect(h, method, "http://example.test/old?a=1")
				if response.Code != test.status || response.Header().Get("Location") != test.location || !reflect.DeepEqual(events, test.events) {
					t.Fatal(response.Code, response.Header(), events)
				}
				if test.found && (response.Body.Len() != 0 || response.Header().Get("Cache-Control") != "no-store") {
					t.Fatal("redirect or gone response exposed body/cache", response)
				}
				if method == "HEAD" && response.Body.Len() != 0 {
					t.Fatal("HEAD wrote a body")
				}
			}
		})
	}
}

func TestRedirectHTTPNeverReplaysUnsafeBodiesOrInterceptsMatchedViews(t *testing.T) {
	var events []string
	h := handlerFixture(&events)
	router, err := urls.New(urls.Path("/view", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }), "view"))
	if err != nil {
		t.Fatal(err)
	}
	router, err = router.WithNotFound(h)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"http://example.test/view", "http://example.test/view?x=1"} {
		if got := serveRedirect(router, "GET", target); got.Code != 404 || len(events) != 0 {
			t.Fatal(got.Code, events)
		}
	}
	request := httptest.NewRequest("POST", "http://example.test/old", nil)
	body := &unreadRedirectBody{}
	request.Body = body
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 404 || len(events) != 0 || body.reads != 0 {
		t.Fatal(response.Code, events, body.reads)
	}
	request = httptest.NewRequest("GET", "http://example.test/old", nil)
	request.Body = body
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 400 || len(events) != 0 || body.reads != 0 {
		t.Fatal(response.Code, events, body.reads)
	}
	if got := serveRedirect(router, "GET", "http://example.test/old"); got.Code != 301 {
		t.Fatal(got.Code)
	}
}

type unreadRedirectBody struct{ reads int }

func (b *unreadRedirectBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (*unreadRedirectBody) Close() error               { return nil }

func TestRedirectHTTPExternalDestinationsRequireSeparateGrant(t *testing.T) {
	for _, policy := range []string{"missing", "allow", "deny", "error", "joined", "panic", "cancel"} {
		t.Run(policy, func(t *testing.T) {
			var events []string
			h := handlerFixture(&events)
			h.lookup = func(context.Context, LookupInput) (Match, bool, error) {
				events = append(events, "lookup")
				return Match{Location: "https://outside.test/new?x=1", External: true}, true, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if policy != "missing" {
				h.options.AllowExternal = func(_ context.Context, info sites.Info, location string) error {
					events = append(events, "external")
					if info.Domain != "example.test" || location != "https://outside.test/new?x=1" {
						t.Fatal(info, location)
					}
					switch policy {
					case "deny":
						return ErrForbidden
					case "error":
						return errors.New("private-policy-data")
					case "joined":
						return errors.Join(ErrForbidden, errors.New("private-policy-data"))
					case "panic":
						panic("private-policy-data")
					case "cancel":
						cancel()
					}
					return nil
				}
			}
			request := httptest.NewRequest("GET", "http://example.test/old", nil).WithContext(ctx)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			want := 503
			if policy == "missing" || policy == "deny" {
				want = 403
			}
			if policy == "allow" {
				want = 302
			}
			if response.Code != want || strings.Contains(response.Body.String(), "private") {
				t.Fatal(response.Code, response.Body.String())
			}
			if want != 302 && response.Header().Get("Location") != "" {
				t.Fatal("denied target leaked")
			}
			if want == 302 && !reflect.DeepEqual(events, []string{"site", "authorize", "lookup", "external", "authorize"}) {
				t.Fatal(events)
			}
		})
	}
}

func TestRedirectHTTPFailsClosedAtEveryCallback(t *testing.T) {
	for _, stage := range []string{"site", "lookup", "first grant", "last grant"} {
		for _, outcome := range []string{"error", "panic", "cancel", "deny"} {
			t.Run(stage+"/"+outcome, func(t *testing.T) {
				var events []string
				h := handlerFixture(&events)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				fail := func() error {
					switch outcome {
					case "panic":
						panic("private-provider-data")
					case "cancel":
						cancel()
						return nil
					case "deny":
						return ErrForbidden
					}
					return errors.New("private-provider-data")
				}
				switch stage {
				case "site":
					h.current = func(*http.Request) (sites.Info, error) { return sites.Info{}, fail() }
				case "lookup":
					h.lookup = func(context.Context, LookupInput) (Match, bool, error) {
						return Match{Location: "/do-not-emit"}, true, fail()
					}
				default:
					calls := 0
					h.options.Authorize = func(context.Context, sites.Info) error {
						calls++
						if stage == "first grant" || calls == 2 {
							return fail()
						}
						return nil
					}
				}
				r := httptest.NewRequest("GET", "http://example.test/old", nil).WithContext(ctx)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				want := 503
				if outcome == "deny" && (stage == "first grant" || stage == "last grant") {
					want = 403
				}
				if w.Code != want || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "private") {
					t.Fatal(w.Code, w.Header(), w.Body.String())
				}
			})
		}
	}
}

func TestRedirectHTTPRequestValidationAndIdentitySnapshot(t *testing.T) {
	for _, change := range []func(*http.Request){
		func(r *http.Request) { r.URL = nil },
		func(r *http.Request) { r.URL.RawPath = "/elsewhere" },
		func(r *http.Request) { r.URL.Host = "elsewhere.test" },
		func(r *http.Request) { r.URL.Scheme = "https" },
		func(r *http.Request) { r.Host = "bad host" },
		func(r *http.Request) { r.URL.Fragment = "untrusted" },
		func(r *http.Request) { r.URL.RawQuery = "bad=%0a" },
		func(r *http.Request) { r.URL.Path = "/../elsewhere" },
	} {
		var events []string
		h := handlerFixture(&events)
		r := httptest.NewRequest("GET", "http://example.test/old", nil)
		change(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || len(events) != 0 {
			t.Fatal(w.Code, events)
		}
	}
	var events []string
	h := handlerFixture(&events)
	r := httptest.NewRequest("GET", "http://example.test/old?x=1&x=2", nil)
	r.Header.Set("X-Forwarded-Host", "evil.test")
	r.Header.Set("X-Forwarded-Proto", "https")
	ctx := &redirectMutationContext{Context: context.Background(), change: func() {
		r.Host = "evil.test"
		r.URL.Path = "/evil"
		r.URL.RawQuery = "evil=1"
		r.Method = "POST"
		h.options.Authorize = func(context.Context, sites.Info) error { return ErrForbidden }
	}}
	r = r.WithContext(ctx)
	h.lookup = func(_ context.Context, in LookupInput) (Match, bool, error) {
		if in.URI != "/old?x=1&x=2" || in.Origin != "http://example.test" {
			t.Fatal("callback retargeted lookup", in)
		}
		return Match{Location: "/new"}, true, nil
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 302 {
		t.Fatal(w.Code)
	}
}

type redirectMutationContext struct {
	context.Context
	change func()
}

func (c *redirectMutationContext) Err() error {
	if c.change != nil {
		f := c.change
		c.change = nil
		f()
	}
	return nil
}

func TestRedirectHTTPFallbackAndHeadPreserveCallerContext(t *testing.T) {
	var events []string
	h := handlerFixture(&events)
	type key struct{}
	h.lookup = func(context.Context, LookupInput) (Match, bool, error) { return Match{}, false, nil }
	h.options.NotFound = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(key{}) != "kept" {
			t.Fatal("lost caller context")
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte("custom not found"))
	})
	for _, method := range []string{"GET", "HEAD"} {
		r := httptest.NewRequest(method, "http://example.test/missing", nil).WithContext(context.WithValue(context.Background(), key{}, "kept"))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 || method == "GET" && w.Body.String() != "custom not found" || method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestRedirectHTTPConstructorFreezesResolversAndOptions(t *testing.T) {
	base, _ := redirectFixture(map[string]string{"/old": "/original"})
	backend := &redirectHTTPBackend{base}
	resolver, err := New(Config{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	selector, err := sites.New(sites.Config{Backend: backend, AllowedHosts: []string{"example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	options := HandlerOptions{Sites: selector, Authorize: func(context.Context, sites.Info) error { return nil }}
	for _, bad := range []HandlerOptions{
		{}, {Sites: selector},
		{Sites: selector, Authorize: options.Authorize, Timeout: -time.Second},
		{Sites: selector, Authorize: options.Authorize, Timeout: time.Nanosecond},
		{Sites: selector, Authorize: options.Authorize, Timeout: time.Hour},
		{Sites: selector, Authorize: options.Authorize, NotFound: http.HandlerFunc(nil)},
	} {
		if _, err := NewHandler(resolver, bad); err != ErrConfiguration {
			t.Fatal("invalid handler configuration accepted", err)
		}
	}
	for _, bad := range []*Resolver{nil, {}} {
		if _, err := NewHandler(bad, options); err != ErrConfiguration {
			t.Fatal("invalid resolver accepted", err)
		}
	}
	handler, err := NewHandler(resolver, options)
	if err != nil {
		t.Fatal(err)
	}
	*resolver = Resolver{}
	*selector = sites.Resolver{}
	options.Authorize = func(context.Context, sites.Info) error { return ErrForbidden }
	response := serveRedirect(handler, "GET", "http://example.test/old")
	if response.Code != 301 || response.Header().Get("Location") != "/original" {
		t.Fatal(response.Code, response.Header())
	}
}

type redirectHTTPBackend struct{ *redirectBackend }

// Sites.Current queries outside the redirect chain's owned transaction. Expose
// only this fixture's read path without weakening the lookup fixture's guard.
func (b *redirectHTTPBackend) Query(ctx context.Context, query string, args ...any) (db.Rows, error) {
	return b.tx.query(ctx, query, args)
}

type redirectBrokenContext struct {
	context.Context
	stage string
}

func (c *redirectBrokenContext) Err() error {
	if c == nil || c.stage == "err" {
		panic("private context failure")
	}
	return nil
}
func (c *redirectBrokenContext) Deadline() (time.Time, bool) {
	if c.stage == "deadline" {
		panic("private context failure")
	}
	return time.Time{}, false
}
func (c *redirectBrokenContext) Value(k any) any {
	if c.stage == "value" {
		panic("private context failure")
	}
	return c.Context.Value(k)
}

func TestRedirectHTTPBrokenContextsNeverExposeTargets(t *testing.T) {
	for _, ctx := range []context.Context{
		(*redirectBrokenContext)(nil),
		&redirectBrokenContext{Context: context.Background(), stage: "err"},
		&redirectBrokenContext{Context: context.Background(), stage: "deadline"},
		&redirectBrokenContext{Context: context.Background(), stage: "value"},
	} {
		var events []string
		h := handlerFixture(&events)
		r := httptest.NewRequest("GET", "http://example.test/old", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 503 || w.Header().Get("Location") != "" || len(events) != 0 {
			t.Fatal(w.Code, w.Header(), events)
		}
	}
}
