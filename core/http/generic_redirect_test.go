package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/urls"
)

func genericRedirectAllow(*http.Request) error { return nil }

func TestGenericRedirectConfiguration(t *testing.T) {
	read := ReadViewOptions{Authorize: genericRedirectAllow}
	reverse := func(string, map[string]any, url.Values) (string, error) { return "/detail", nil }
	target := func(*http.Request) (string, error) { return "/dynamic", nil }
	external := func(*http.Request, string) error { return nil }
	for _, options := range []RedirectViewOptions{
		{},
		{ReadViewOptions: read, URL: "/one", Target: target},
		{ReadViewOptions: read, URL: "/one", PatternName: "detail", Reverse: reverse},
		{ReadViewOptions: read, Target: target, PatternName: "detail", Reverse: reverse},
		{ReadViewOptions: read, PatternName: "detail"},
		{ReadViewOptions: read, Reverse: reverse},
		{ReadViewOptions: read, PatternName: ":detail", Reverse: reverse},
		{ReadViewOptions: read, PatternName: "detail:", Reverse: reverse},
		{ReadViewOptions: read, PatternName: "bad name", Reverse: reverse},
		{ReadViewOptions: read, PatternName: strings.Repeat("x", 256), Reverse: reverse},
		{ReadViewOptions: read, URL: "https://example.test/"},
		{ReadViewOptions: read, URL: "//example.test/", AuthorizeExternal: external},
		{ReadViewOptions: read, URL: "/%252f"},
		{ReadViewOptions: read, URL: "/" + strings.Repeat("x", maxGenericRedirectURI)},
		{ReadViewOptions: ReadViewOptions{Authorize: genericRedirectAllow, Timeout: time.Nanosecond}},
	} {
		if handler, err := NewRedirectView(options); handler != nil || err != ErrGenericConfiguration {
			t.Fatalf("invalid configuration accepted: %#v, %v", handler, err)
		}
	}
	calls := 0
	for _, options := range []RedirectViewOptions{
		{ReadViewOptions: read},
		{ReadViewOptions: read, URL: "/literal/%25"},
		{ReadViewOptions: read, URL: "https://example.test/", AuthorizeExternal: func(*http.Request, string) error { calls++; return nil }},
		{ReadViewOptions: read, Target: func(*http.Request) (string, error) { calls++; return "", nil }},
		{ReadViewOptions: read, PatternName: "articles:detail", Reverse: func(string, map[string]any, url.Values) (string, error) { calls++; return "", nil }},
	} {
		if handler, err := NewRedirectView(options); err != nil || handler == nil {
			t.Fatal("valid configuration rejected", err)
		}
	}
	if calls != 0 {
		t.Fatal("constructor invoked application callbacks", calls)
	}
}

func TestGenericRedirectMethodsGoneAndLiteralURL(t *testing.T) {
	for _, test := range []struct {
		name, method, target string
		permanent, options   bool
		status, grants       int
	}{
		{"temporary", "GET", "/target/%25", false, false, 302, 2},
		{"permanent", "GET", "/target", true, false, 301, 2},
		{"head", "HEAD", "/target", false, false, 302, 2},
		{"empty", "GET", "", false, false, 410, 2},
		{"empty_head", "HEAD", "", true, false, 410, 2},
		{"post", "POST", "/target", false, true, 405, 0},
		{"put", "PUT", "/target", false, true, 405, 0},
		{"delete", "DELETE", "/target", false, true, 405, 0},
		{"options_disabled", "OPTIONS", "/target", false, false, 405, 0},
		{"options_enabled", "OPTIONS", "/target", false, true, 200, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			grants := 0
			handler, err := NewRedirectView(RedirectViewOptions{
				ReadViewOptions: ReadViewOptions{Authorize: func(*http.Request) error { grants++; return nil }, AllowOptions: test.options},
				URL:             test.target, Permanent: test.permanent,
			})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, "/old?discarded=1", nil))
			if response.Code != test.status || grants != test.grants {
				t.Fatal("unexpected response or grant count", response.Code, grants)
			}
			location := ""
			if test.status == 301 || test.status == 302 {
				location = test.target
			}
			if response.Header().Get("Location") != location {
				t.Fatal("unexpected Location", response.Header())
			}
			if test.method == "HEAD" && response.Body.Len() != 0 {
				t.Fatal("HEAD emitted a body")
			}
			if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("missing private response headers", response.Header())
			}
			if test.status == 405 || test.method == "OPTIONS" {
				want := "GET, HEAD"
				if test.options {
					want += ", OPTIONS"
				}
				if response.Header().Get("Allow") != want {
					t.Fatal("invalid method advertisement", response.Header())
				}
			}
		})
	}
}

func TestGenericRedirectOptionsDoesNotResolveTarget(t *testing.T) {
	for _, deny := range []bool{false, true} {
		grants, reads := 0, 0
		handler, err := NewRedirectView(RedirectViewOptions{
			ReadViewOptions: ReadViewOptions{AllowOptions: true, Authorize: func(*http.Request) error {
				grants++
				if deny {
					return auth.ErrPermissionDenied
				}
				return nil
			}},
			Target: func(*http.Request) (string, error) { reads++; return "/unused", nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("OPTIONS", "/old", nil))
		want, wantGrants := 200, 2
		if deny {
			want, wantGrants = 403, 1
		}
		if response.Code != want || grants != wantGrants || reads != 0 {
			t.Fatal("OPTIONS bypassed authority or read data", response.Code, grants, reads)
		}
	}
}

func TestGenericRedirectNamedRouteUsesFrozenParams(t *testing.T) {
	routes, err := urls.New(urls.Include("articles/", "articles", urls.Path("<int:id>/", http.NotFoundHandler(), "detail")))
	if err != nil {
		t.Fatal(err)
	}
	grants, reversals := 0, 0
	handler, err := NewRedirectView(RedirectViewOptions{
		ReadViewOptions: ReadViewOptions{Authorize: func(request *http.Request) error {
			grants++
			if urls.Param(request, "id") != int64(42) {
				t.Error("route identity changed across callbacks")
			}
			params := urls.Params(request)
			params["id"] = int64(99)
			request.URL.Path = "/other/99/"
			return nil
		}},
		PatternName: "articles:detail", QueryString: true,
		Reverse: func(name string, params map[string]any, query url.Values) (string, error) {
			reversals++
			if name != "articles:detail" || params["id"] != int64(42) || len(params) != 1 || query != nil {
				t.Error("reverse received mutable or incorrectly forwarded arguments", name, params, query)
			}
			result, err := routes.Reverse(name, params, query)
			params["id"] = int64(100)
			return result, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Path("old/<int:id>/", handler, "old"))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/old/42/?x=1&x=%2f&x=+", nil))
	if response.Code != 302 || response.Header().Get("Location") != "/articles/42/?x=1&x=%2f&x=+" || grants != 2 || reversals != 1 {
		t.Fatal("named redirect failed", response.Code, response.Header(), grants, reversals)
	}
}

func TestGenericRedirectExternalGrantsInspectFinalLocation(t *testing.T) {
	var sequence []string
	want := "https://example.test/new?own=1&x=2&x=%2f#part"
	handler, err := NewRedirectView(RedirectViewOptions{
		ReadViewOptions: ReadViewOptions{Authorize: func(*http.Request) error { sequence = append(sequence, "access"); return nil }},
		Target: func(*http.Request) (string, error) {
			sequence = append(sequence, "target")
			return "HTTPS://EXAMPLE.TEST.:443/new?own=1#part", nil
		},
		QueryString: true,
		AuthorizeExternal: func(request *http.Request, location string) error {
			sequence = append(sequence, "external")
			if location != want || request.Host != "example.test" || request.URL.RawQuery != "x=2&x=%2f" {
				t.Error("external policy did not receive final target and original request", location, request.URL)
			}
			request.Host, request.URL.RawQuery = "changed.test", "changed=1"
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "https://example.test/old?x=2&x=%2f", nil))
	if response.Code != 302 || response.Header().Get("Location") != want || !reflect.DeepEqual(sequence, []string{"access", "target", "external", "access", "external"}) {
		t.Fatal("external grant ordering or final representation changed", response.Code, response.Header(), sequence)
	}
	// The same request host is deliberately not an external-redirect grant.
	ungranted, err := NewRedirectView(RedirectViewOptions{ReadViewOptions: ReadViewOptions{Authorize: genericRedirectAllow}, Target: func(*http.Request) (string, error) {
		return "https://example.test/new", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	ungranted.ServeHTTP(response, httptest.NewRequest("GET", "https://example.test/old", nil))
	if response.Code != 403 || response.Header().Get("Location") != "" {
		t.Fatal("request host conferred external authority", response.Code, response.Header())
	}
}

func TestGenericRedirectCallbackFailureNeverDisclosesTarget(t *testing.T) {
	private := errors.New("private callback diagnostic")
	for _, stage := range []string{"access", "target", "external", "final_access", "final_external"} {
		for _, failure := range []struct {
			name   string
			err    error
			status int
			panic  bool
		}{
			{"unauthenticated", auth.ErrUnauthenticated, 401, false},
			{"denied", auth.ErrPermissionDenied, 403, false},
			{"not_found", ErrNotFound, 404, false},
			{"provider", private, 503, false},
			{"joined", errors.Join(auth.ErrPermissionDenied, private), 503, false},
			{"wrapped", fmt.Errorf("private wrapped: %w", auth.ErrPermissionDenied), 503, false},
			{"panic", nil, 503, true},
		} {
			t.Run(stage+"/"+failure.name, func(t *testing.T) {
				fail := func() error {
					if failure.panic {
						panic("private callback panic")
					}
					return failure.err
				}
				grants, external := 0, 0
				handler, err := NewRedirectView(RedirectViewOptions{
					ReadViewOptions: ReadViewOptions{Authorize: func(*http.Request) error {
						grants++
						if stage == "access" && grants == 1 || stage == "final_access" && grants == 2 {
							return fail()
						}
						return nil
					}},
					Target: func(*http.Request) (string, error) {
						if stage == "target" {
							return "", fail()
						}
						return "https://example.test/private-destination", nil
					},
					AuthorizeExternal: func(*http.Request, string) error {
						external++
						if stage == "external" && external == 1 || stage == "final_external" && external == 2 {
							return fail()
						}
						return nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest("GET", "/old", nil))
				if response.Code != failure.status || response.Header().Get("Location") != "" || strings.Contains(response.Body.String(), "private") || response.Header().Get("Cache-Control") != "private, no-store" {
					t.Fatal("callback failure leaked or became redirect/Gone", response.Code, response.Header(), response.Body.String())
				}
			})
		}
	}
}

func TestGenericRedirectSourceAndOriginalRequestAreFrozen(t *testing.T) {
	type scopeKey struct{}
	principal := auth.Principal{ID: "account", Active: true, Authenticated: true, Permissions: []string{"articles.view"}}
	ctx := auth.WithPrincipal(context.WithValue(context.Background(), scopeKey{}, "tenant-one"), principal)
	original := httptest.NewRequest("HEAD", "/old?x=1&x=%2f", nil).WithContext(ctx)
	original.Header.Set("X-Test", "original")
	original.SetPathValue("id", "original-id")
	grants, targets := 0, 0
	var options RedirectViewOptions
	inspect := func(request *http.Request) {
		if request.Method != "HEAD" || request.URL.Path != "/old" || request.URL.RawQuery != "x=1&x=%2f" || request.Header.Get("X-Test") != "original" || request.PathValue("id") != "original-id" || request.Context().Value(scopeKey{}) != "tenant-one" || auth.FromContext(request.Context()).ID != "account" {
			t.Error("frozen metadata or inherited authority changed", request.Method, request.URL, request.Header, request.PathValue("id"))
		}
		request.Method = "POST"
		request.URL.RawQuery = "injected=1"
		request.Header.Set("X-Test", "hook")
		request.SetPathValue("id", "hook-id")
		*request = *request.WithContext(context.Background())
	}
	options = RedirectViewOptions{
		ReadViewOptions: ReadViewOptions{Authorize: func(request *http.Request) error {
			grants++
			inspect(request)
			original.Method, original.URL.Path, original.URL.RawQuery = "GET", "/changed", "changed=1"
			original.Header.Set("X-Test", "changed")
			original.SetPathValue("id", "changed-id")
			options = RedirectViewOptions{URL: "/replacement", Permanent: false}
			if grants == 2 {
				return auth.ErrPermissionDenied
			}
			return nil
		}},
		Target: func(request *http.Request) (string, error) {
			targets++
			inspect(request)
			return "/new?own=1#part", nil
		},
		Permanent: true, QueryString: true,
	}
	handler, err := NewRedirectView(options)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, original)
	if response.Code != 403 || response.Body.Len() != 0 || response.Header().Get("Location") != "" || grants != 2 || targets != 1 {
		t.Fatal("mutation changed final authority or HEAD emission", response.Code, response.Header(), response.Body.String(), grants, targets)
	}
}

func TestGenericRedirectRegistrationSnapshotsStatusAndQueryChoice(t *testing.T) {
	options := RedirectViewOptions{
		ReadViewOptions: ReadViewOptions{Authorize: genericRedirectAllow},
		URL:             "/new?own=1#part", Permanent: true, QueryString: true,
	}
	handler, err := NewRedirectView(options)
	if err != nil {
		t.Fatal(err)
	}
	options.URL, options.Permanent, options.QueryString = "/replacement", false, false
	options.Authorize = func(*http.Request) error { return auth.ErrPermissionDenied }
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/old?x=2&x=%2f", nil))
	if response.Code != 301 || response.Header().Get("Location") != "/new?own=1&x=2&x=%2f#part" {
		t.Fatal("registration retained mutable caller options", response.Code, response.Header())
	}
}

func TestGenericRedirectCanceledTargetDoesNotInvokeExternalPolicy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	grants, external := 0, 0
	handler, err := NewRedirectView(RedirectViewOptions{
		ReadViewOptions:   ReadViewOptions{Authorize: func(*http.Request) error { grants++; return nil }},
		Target:            func(*http.Request) (string, error) { cancel(); return "https://example.test/new", nil },
		AuthorizeExternal: func(*http.Request, string) error { external++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/old", nil).WithContext(ctx))
	if response.Code != 503 || response.Header().Get("Location") != "" || grants != 1 || external != 0 {
		t.Fatal("canceled target invoked another application callback", response.Code, grants, external)
	}
}

func TestGenericRedirectDynamicGoneAndMalformedReverse(t *testing.T) {
	for _, test := range []struct {
		name    string
		options RedirectViewOptions
		status  int
	}{
		{"target_gone", RedirectViewOptions{Target: func(*http.Request) (string, error) { return "", nil }}, 410},
		{"target_error_not_gone", RedirectViewOptions{Target: func(*http.Request) (string, error) { return "", errors.New("private") }}, 503},
		{"target_invalid", RedirectViewOptions{Target: func(*http.Request) (string, error) { return "/%2fother", nil }}, 503},
		{"reverse_empty", RedirectViewOptions{PatternName: "detail", Reverse: func(string, map[string]any, url.Values) (string, error) { return "", nil }}, 503},
		{"reverse_error", RedirectViewOptions{PatternName: "detail", Reverse: func(string, map[string]any, url.Values) (string, error) { return "/ignored", urls.ErrReverse }}, 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.options.Authorize = genericRedirectAllow
			handler, err := NewRedirectView(test.options)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/old", nil))
			if response.Code != test.status || response.Header().Get("Location") != "" || strings.Contains(response.Body.String(), "private") {
				t.Fatal("invalid source result", response.Code, response.Header(), response.Body.String())
			}
		})
	}
}

func TestGenericRedirectConcurrentRequestsStayIsolated(t *testing.T) {
	handler, err := NewRedirectView(RedirectViewOptions{
		ReadViewOptions: ReadViewOptions{Authorize: func(request *http.Request) error {
			if request.URL.Query().Get("id") != request.Header.Get("X-Identity") {
				return auth.ErrPermissionDenied
			}
			request.Header.Set("X-Identity", "callback-local")
			return nil
		}},
		Target: func(request *http.Request) (string, error) {
			id := request.URL.Query().Get("id")
			request.URL.RawQuery = "callback-local=1"
			return "/new/" + id, nil
		},
		QueryString: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			id := fmt.Sprint(i)
			request := httptest.NewRequest("GET", "/old?id="+id, nil)
			request.Header.Set("X-Identity", id)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 302 || response.Header().Get("Location") != "/new/"+id+"?id="+id {
				t.Error("concurrent redirect crossed request identities", response.Code, response.Header())
			}
		}()
	}
	group.Wait()
}
