package postgres_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/flatpages"
	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

func TestPostgresFlatpagesHTTPUsesSelectedSiteAndNamedTemplate(t *testing.T) {
	b, _, all, _ := setupFlatpages(t)
	ctx := context.Background()
	store := newFlatStore(t, b, allowFlatChange)
	page, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{URL: "/about/", Title: "About", Content: "<b>first</b>{{ site.domain }}"}, SiteIDs: []string{all[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{URL: "/about/", Title: "Second", Content: "different site"}, SiteIDs: []string{all[1].ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveDomain(ctx, flatpages.SaveInput{Page: flatpages.Draft{URL: "/members/", Title: "Members", Content: "private", TemplateName: "flatpages/members.html", RegistrationRequired: true}, SiteIDs: []string{all[0].ID}}); err != nil {
		t.Fatal(err)
	}
	selector, err := sites.New(sites.Config{Backend: b, AllowedHosts: []string{"example.test", "second.test", "third.test"}})
	if err != nil {
		t.Fatal(err)
	}
	grants := 0
	allow := true
	options := flatpages.HandlerOptions{
		Sites: selector,
		Authorize: func(context.Context, sites.Info, flatpages.Info) error {
			grants++
			if !allow {
				return flatpages.ErrForbidden
			}
			return nil
		},
		Render: flatpages.RenderOptions{
			AllowedTemplates: []string{"flatpages/default.html", "flatpages/members.html"},
			Templates: templates.Config{Loaders: []templates.Loader{templates.MapLoader{
				"flatpages/default.html": "<h1>{{ flatpage.title }}</h1><main>{{ flatpage.content }}</main>",
				"flatpages/members.html": "<section>{{ flatpage.content }}</section>",
			}}},
		},
	}
	handler, err := flatpages.NewHandler(store, options)
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Path("/view", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }), "view"))
	if err != nil {
		t.Fatal(err)
	}
	router, err = router.WithNotFound(handler)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(h http.Handler, method, target string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, target, nil)
		if authenticated {
			// This fixture supplies an already verified middleware principal;
			// it does not claim credential/session-provider integration.
			request = request.WithContext(auth.WithPrincipal(ctx, auth.Principal{ID: "reader", Active: true, Authenticated: true}))
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		return response
	}
	for _, test := range []struct {
		method, target, body string
		status, grants       int
		authenticated        bool
	}{
		{"GET", "http://example.test/about/?ignored=/members/", "<h1>About</h1><main>&lt;b&gt;first&lt;/b&gt;{{ site.domain }}</main>", 200, 2, false},
		{"HEAD", "http://example.test/about/", "", 200, 2, false},
		{"GET", "http://second.test/about/", "<h1>Second</h1><main>different site</main>", 200, 2, false},
		{"GET", "http://third.test/about/", "404 page not found\n", 404, 0, false},
		{"GET", "http://example.test/members/", "", 404, 0, false},
		{"GET", "http://example.test/members/", "<section>private</section>", 200, 2, true},
		{"GET", "http://example.test/view", "", 404, 0, true},
	} {
		grants = 0
		response := serve(router, test.method, test.target, test.authenticated)
		if response.Code != test.status || response.Body.String() != test.body || grants != test.grants {
			t.Fatal(test.target, response.Code, response.Body.String(), grants)
		}
		if test.status == 200 {
			length, err := strconv.Atoi(response.Header().Get("Content-Length"))
			if err != nil || length <= 0 || test.method == "GET" && length != len(test.body) || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal(response.Header(), err)
			}
		}
	}
	allow = false
	response := serve(router, "GET", "http://example.test/about/", false)
	if response.Code != 404 || response.Body.Len() != 0 {
		t.Fatal("denied reader saw persisted content", response.Code, response.Body.String())
	}
	allow = true
	options.Render.Sanitize = func(context.Context, flatpages.Info) (templates.SafeHTML, error) {
		allow = false
		return "private after revocation", nil
	}
	revoking, err := flatpages.NewHandler(store, options)
	if err != nil {
		t.Fatal(err)
	}
	response = serve(revoking, "GET", "http://example.test/about/", false)
	if response.Code != 404 || response.Body.Len() != 0 {
		t.Fatal("last grant did not conceal revoked output", response.Code, response.Body.String())
	}
	allow = true
	if _, err := b.Exec(ctx, `UPDATE "gogo_flatpage_sites" SET "url"='/drift/' WHERE "page_id"=$1`, page.Page.ID); err != nil {
		t.Fatal(err)
	}
	response = serve(router, "GET", "http://example.test/drift/", true)
	if response.Code != 503 || response.Body.Len() != 0 || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("mapping failure became content or normal fallback", response.Code, response.Body.String())
	}
}
