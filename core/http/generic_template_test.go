package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

type genericTestLoader func(context.Context, string) (string, error)

func (f genericTestLoader) Load(ctx context.Context, name string) (string, error) {
	return f(ctx, name)
}

func genericTemplateOptions(source string) TemplateViewOptions {
	return TemplateViewOptions{ReadViewOptions: ReadViewOptions{Authorize: allowGenericRead}, TemplateName: "page.html", Templates: templates.Config{Strict: true, Loaders: []templates.Loader{templates.MapLoader{"page.html": source}}}}
}

func TestGenericTemplateConstructionAndNameLimits(t *testing.T) {
	for _, mutate := range []func(*TemplateViewOptions){
		func(o *TemplateViewOptions) { o.Authorize = nil }, func(o *TemplateViewOptions) { o.TemplateName = "" },
		func(o *TemplateViewOptions) { o.TemplateName = "../private.html" }, func(o *TemplateViewOptions) { o.TemplateName = "/private.html" },
		func(o *TemplateViewOptions) { o.TemplateName = "page.html#" }, func(o *TemplateViewOptions) { o.TemplateName = "page.html#a#b" },
		func(o *TemplateViewOptions) { o.TemplateName = "page.html#a b" }, func(o *TemplateViewOptions) { o.TemplateName = "page\\name.html" },
		func(o *TemplateViewOptions) { o.TemplateName = strings.Repeat("x", 256) }, func(o *TemplateViewOptions) { o.TemplateName = "page\x00.html" },
		func(o *TemplateViewOptions) { o.MaxOutputBytes = -1 }, func(o *TemplateViewOptions) { o.MaxOutputBytes = 16<<20 + 1 },
		func(o *TemplateViewOptions) { o.Templates.Loaders = nil }, func(o *TemplateViewOptions) { o.Templates.Loaders = []templates.Loader{(*templates.MapLoader)(nil)} },
		func(o *TemplateViewOptions) { o.Templates.Processors = []templates.Processor{nil} }, func(o *TemplateViewOptions) { o.Templates.MaxDepth = 65 },
		func(o *TemplateViewOptions) { o.Templates.MaxIterations = 100001 }, func(o *TemplateViewOptions) { o.ExtraContext = templates.Context{"bad": func() {}} },
	} {
		options := genericTemplateOptions("page")
		mutate(&options)
		if handler, err := NewTemplateView(options); handler != nil || err != ErrGenericConfiguration {
			t.Fatal("invalid template construction accepted", err)
		}
	}
}

func TestGenericTemplateContextPrecedenceAndEscaping(t *testing.T) {
	options := genericTemplateOptions(`{{ id }}|{{ from_route }}|{{ from_extra }}|{{ from_processor }}|{{ content }}|{{ safe }}`)
	options.ExtraContext = templates.Context{"id": "extra", "from_extra": "extra", "content": "<script>{{ secret }}</script>"}
	options.Context = func(r *http.Request) (templates.Context, error) {
		if urls.Param(r, "id") != int64(7) {
			t.Error("typed route param missing")
		}
		return templates.Context{"id": "dynamic", "safe": templates.SafeHTML("<b>trusted</b>")}, nil
	}
	options.Templates.Processors = []templates.Processor{func(context.Context) (templates.Context, error) {
		return templates.Context{"id": "processor", "from_extra": "processor", "from_processor": "processor"}, nil
	}}
	handler, err := NewTemplateView(options)
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Path("/<int:id>/<slug:from_route>/", handler, "page"))
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest(method, "/7/route/", nil))
		want := `dynamic|route|extra|processor|&lt;script&gt;{{ secret }}&lt;/script&gt;|<b>trusted</b>`
		if out.Code != 200 || out.Header().Get("Content-Type") != "text/html; charset=utf-8" || out.Header().Get("Content-Length") != strconv.Itoa(len(want)) {
			t.Fatal("template metadata", out)
		}
		if method == "GET" && out.Body.String() != want || method == "HEAD" && out.Body.Len() != 0 {
			t.Fatal("incorrect context/escaping/HEAD", out.Body.String())
		}
	}
}

func TestGenericTemplateCapturesConfigAndDetachesDataBeforeCallbacks(t *testing.T) {
	source := templates.MapLoader{"page.html": "{{ nested.value }}|{{ dynamic.value }}|{{ processor.value }}"}
	extra := map[string]string{"value": "extra"}
	dynamic := map[string]string{"value": "dynamic"}
	processor := map[string]string{"value": "processor"}
	options := genericTemplateOptions("")
	options.Templates.Loaders = []templates.Loader{&source}
	options.ExtraContext = templates.Context{"nested": extra}
	options.Context = func(*http.Request) (templates.Context, error) { return templates.Context{"dynamic": dynamic}, nil }
	options.Templates.Processors = []templates.Processor{
		func(context.Context) (templates.Context, error) {
			dynamic["value"] = "mutated"
			return templates.Context{"processor": processor}, nil
		},
		func(context.Context) (templates.Context, error) { processor["value"] = "mutated"; return nil, nil },
	}
	grants := 0
	options.Authorize = func(*http.Request) error {
		grants++
		if grants == 2 {
			dynamic["value"] = "final"
			processor["value"] = "final"
		}
		return nil
	}
	handler, err := NewTemplateView(options)
	if err != nil {
		t.Fatal(err)
	}
	extra["value"] = "changed"
	source["page.html"] = "wrong"
	options.TemplateName = "wrong.html"
	options.Context = nil
	options.Templates.Processors[0] = nil
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, httptest.NewRequest("GET", "/", nil))
	if out.Code != 200 || out.Body.String() != "extra|dynamic|processor" || grants != 2 {
		t.Fatal("config/data alias changed authorized representation", out, grants)
	}
}

func TestGenericTemplateDropsPartialOutputForEveryFailure(t *testing.T) {
	for _, stage := range []string{"context", "processor", "loader", "render", "limit", "final_grant"} {
		t.Run(stage, func(t *testing.T) {
			options := genericTemplateOptions("private page")
			grants := 0
			options.Authorize = func(*http.Request) error {
				grants++
				if stage == "final_grant" && grants == 2 {
					return auth.ErrPermissionDenied
				}
				return nil
			}
			switch stage {
			case "context":
				options.Context = func(*http.Request) (templates.Context, error) {
					return templates.Context{"private": "data"}, errors.New("private")
				}
			case "processor":
				options.Templates.Processors = []templates.Processor{func(context.Context) (templates.Context, error) {
					return templates.Context{"private": "data"}, errors.New("private")
				}}
			case "loader":
				options.Templates.Loaders = []templates.Loader{genericTestLoader(func(context.Context, string) (string, error) { return "private page", errors.New("private") })}
			case "render":
				options.Templates.Loaders = []templates.Loader{templates.MapLoader{"page.html": "private page{{ missing }}"}}
			case "limit":
				options.MaxOutputBytes = 3
			}
			handler, err := NewTemplateView(options)
			if err != nil {
				t.Fatal(err)
			}
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, httptest.NewRequest("GET", "/", nil))
			want := 503
			if stage == "final_grant" {
				want = 403
			}
			if out.Code != want || strings.Contains(out.Body.String(), "private") {
				t.Fatal("partial or failed template escaped", out)
			}
		})
	}
}

func TestGenericTemplatePanicAndCancellationFailBeforeHeaders(t *testing.T) {
	for _, stage := range []string{"context", "processor", "loader"} {
		for _, mode := range []string{"cancel", "panic"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				fail := func() {
					if mode == "panic" {
						panic("private")
					}
					cancel()
				}
				options := genericTemplateOptions("private page")
				switch stage {
				case "context":
					options.Context = func(*http.Request) (templates.Context, error) { fail(); return nil, nil }
				case "processor":
					options.Templates.Processors = []templates.Processor{func(context.Context) (templates.Context, error) { fail(); return nil, nil }}
				case "loader":
					options.Templates.Loaders = []templates.Loader{genericTestLoader(func(context.Context, string) (string, error) { fail(); return "private page", nil })}
				}
				handler, err := NewTemplateView(options)
				if err != nil {
					t.Fatal(err)
				}
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
				if out.Code != 503 || strings.Contains(out.Body.String(), "private") {
					t.Fatal("partial page escaped", out)
				}
			})
		}
	}
}

func TestGenericTemplateNamedInheritanceAndPartial(t *testing.T) {
	options := genericTemplateOptions("")
	options.TemplateName = "child.html"
	options.Templates.Loaders = []templates.Loader{templates.MapLoader{
		"base.html":    `<main>{% block body %}base{% endblock %}</main>`,
		"child.html":   `{% extends "base.html" %}{% block body %}{% include "piece.html" %}{% endblock %}`,
		"piece.html":   `{{ text }}`,
		"partial.html": `outside{% partialdef item %}<b>{{ text }}</b>{% endpartialdef %}`,
	}}
	options.ExtraContext = templates.Context{"text": "<value>"}
	for _, item := range []struct{ name, want string }{{"child.html", "<main>&lt;value&gt;</main>"}, {"partial.html#item", "<b>&lt;value&gt;</b>"}} {
		options.TemplateName = item.name
		handler, err := NewTemplateView(options)
		if err != nil {
			t.Fatal(err)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest("GET", "/", nil))
		if out.Code != 200 || out.Body.String() != item.want {
			t.Fatal("named template composition failed", item.name, out)
		}
	}
}

func TestGenericTemplateConcurrentRequestsDoNotShareContext(t *testing.T) {
	options := genericTemplateOptions("{{ id }}|{{ query }}")
	options.Context = func(r *http.Request) (templates.Context, error) {
		return templates.Context{"query": r.URL.Query().Get("q")}, nil
	}
	handler, err := NewTemplateView(options)
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Path("/<int:id>/", handler, "page"))
	if err != nil {
		t.Fatal(err)
	}
	var work sync.WaitGroup
	for index := range 32 {
		work.Go(func() {
			value := strconv.Itoa(index)
			out := httptest.NewRecorder()
			router.ServeHTTP(out, httptest.NewRequest("GET", "/"+value+"/?q="+value, nil))
			if out.Code != 200 || out.Body.String() != value+"|"+value {
				t.Errorf("cross-request context: %d %q", out.Code, out.Body.String())
			}
		})
	}
	work.Wait()
}
