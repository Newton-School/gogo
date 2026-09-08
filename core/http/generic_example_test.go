package http_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"

	"github.com/Newton-School/gogo/core/auth"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

func ExampleNewTemplateView() {
	page, err := ghttp.NewTemplateView(ghttp.TemplateViewOptions{
		ReadViewOptions: ghttp.ReadViewOptions{
			// This page is deliberately public. Private pages must check the
			// verified principal and application scope here instead.
			Authorize: func(r *http.Request) error { return r.Context().Err() },
		},
		TemplateName: "articles/page.html",
		Templates: templates.Config{
			Loaders: []templates.Loader{templates.MapLoader{
				"articles/page.html": `{{ title }}|{{ id }}|{{ site }}|{{ content }}`,
			}},
			Processors: []templates.Processor{func(context.Context) (templates.Context, error) {
				return templates.Context{"title": "Processor default"}, nil
			}},
		},
		ExtraContext: templates.Context{"title": "Extra default", "site": "Gogo"},
		Context: func(r *http.Request) (templates.Context, error) {
			return templates.Context{
				"title":   fmt.Sprintf("Article %v", urls.Param(r, "id")),
				"content": r.URL.Query().Get("text"), // Data, never template source.
			}, nil
		},
	})
	if err != nil {
		panic(err)
	}
	// Let the generic handler enforce its own method and access policy.
	router, err := urls.New(urls.Path("articles/<int:id>/", page, "article"))
	if err != nil {
		panic(err)
	}
	path := "/articles/7/?" + url.Values{"text": {"<em>{{ title }}</em>"}}.Encode()
	get := httptest.NewRecorder()
	router.ServeHTTP(get, httptest.NewRequest("GET", path, nil))
	fmt.Println(get.Code, get.Body.String())
	// HEAD runs the same context/render/grant path, but sends no body.
	head := httptest.NewRecorder()
	router.ServeHTTP(head, httptest.NewRequest("HEAD", path, nil))
	fmt.Println(head.Code, head.Body.Len(), head.Header().Get("Content-Length") == get.Header().Get("Content-Length"))
	// Output:
	// 200 Article 7|7|Gogo|&lt;em&gt;{{ title }}&lt;/em&gt;
	// 200 0 true
}

func ExampleNewRedirectView() {
	publicRead := ghttp.ReadViewOptions{
		// Explicitly public, read-only access; no counters or database writes.
		Authorize: func(r *http.Request) error { return r.Context().Err() },
	}
	page, err := ghttp.NewTemplateView(ghttp.TemplateViewOptions{
		ReadViewOptions: publicRead,
		TemplateName:    "articles/detail.html",
		Templates: templates.Config{Loaders: []templates.Loader{templates.MapLoader{
			"articles/detail.html": "Article {{ id }}",
		}}},
	})
	if err != nil {
		panic(err)
	}
	var router *urls.Router
	legacy, err := ghttp.NewRedirectView(ghttp.RedirectViewOptions{
		ReadViewOptions: publicRead,
		PatternName:     "articles:detail",
		// The router is initialized before serving requests and then kept fixed.
		Reverse: func(name string, params map[string]any, query url.Values) (string, error) {
			return router.Reverse(name, params, query)
		},
		Permanent: true, QueryString: true,
	})
	if err != nil {
		panic(err)
	}
	router, err = urls.New(
		urls.Include("articles/", "articles", urls.Path("<int:id>/", page, "detail")),
		urls.Path("legacy/<int:id>/", legacy, "legacy"),
	)
	if err != nil {
		panic(err)
	}
	redirect := httptest.NewRecorder()
	router.ServeHTTP(redirect, httptest.NewRequest("GET", "/legacy/7/?source=legacy&source=bookmark", nil))
	fmt.Println(redirect.Code, redirect.Header().Get("Location"))
	// Follow the relative Location through the same router, without a network.
	detail := httptest.NewRecorder()
	router.ServeHTTP(detail, httptest.NewRequest("GET", redirect.Header().Get("Location"), nil))
	fmt.Println(detail.Code, detail.Body.String())
	// Output:
	// 301 /articles/7/?source=legacy&source=bookmark
	// 200 Article 7
}

func ExampleNewRedirectView_external() {
	const destination = "https://docs.example.test/guide#intro"
	handler, err := ghttp.NewRedirectView(ghttp.RedirectViewOptions{
		ReadViewOptions: ghttp.ReadViewOptions{Authorize: func(r *http.Request) error { return r.Context().Err() }},
		URL:             destination,
		// This explicit policy permits only the application-owned destination.
		// It never infers trust from the incoming Host or forwarded headers.
		AuthorizeExternal: func(r *http.Request, location string) error {
			if location != destination {
				return auth.ErrPermissionDenied
			}
			return r.Context().Err()
		},
	})
	if err != nil {
		panic(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("HEAD", "/guide", nil))
	fmt.Println(response.Code, response.Header().Get("Location"), response.Body.Len())
	// Output:
	// 302 https://docs.example.test/guide#intro 0
}
