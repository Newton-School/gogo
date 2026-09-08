package flatpages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/templates"
)

func renderPageFixture() Info {
	return Info{ID: "00000000-0000-4000-8000-000000000001", URL: "/about/", Title: "About", Content: "Body"}
}
func renderSiteFixture() sites.Info {
	return sites.Info{ID: "00000000-0000-4000-8000-000000000002", Domain: "example.test", DisplayName: "Example", Active: true}
}
func pageRenderOptions(source string) RenderOptions {
	return RenderOptions{Templates: templates.Config{Loaders: []templates.Loader{templates.MapLoader{defaultPageTemplate: source}}}}
}
func mustPageRenderer(t *testing.T, options RenderOptions) *pageRenderer {
	t.Helper()
	r, err := newPageRenderer(options)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFlatpageRenderEscapesContentWithoutParsingIt(t *testing.T) {
	r := mustPageRenderer(t, pageRenderOptions(`<title>{{ flatpage.title }}</title><main>{{ flatpage.content }}</main><aside>{{ site.display_name }}</aside>`))
	page, site := renderPageFixture(), renderSiteFixture()
	page.Title = "<title>"
	page.Content = `<script>alert(1)</script>{{ site.domain }}{% include "private.html" %}`
	site.DisplayName = "<site>"
	out, err := r.render(context.Background(), page, site)
	if err != nil || !strings.Contains(out, "&lt;script&gt;") || !strings.Contains(out, "&lt;title&gt;") || !strings.Contains(out, "&lt;site&gt;") || strings.Contains(out, "<script>") || !strings.Contains(out, "{{ site.domain }}") || !strings.Contains(out, "{% include") {
		t.Fatal("content escaping or source separation failed", out, err)
	}
}

func TestFlatpageRenderExplicitSanitizerAndReadonlyViews(t *testing.T) {
	options := pageRenderOptions(`{{ flatpage.title }}|{{ flatpage.content }}|{{ site.domain }}`)
	page, site := renderPageFixture(), renderSiteFixture()
	calls := 0
	options.Sanitize = func(_ context.Context, value Info) (templates.SafeHTML, error) {
		calls++
		if value != page {
			t.Fatal("sanitizer did not receive the exact selected page")
		}
		value.Title = "changed"
		return templates.SafeHTML(`<b>trusted</b>{{ flatpage.title }}`), nil
	}
	r := mustPageRenderer(t, options)
	out, err := r.render(context.Background(), page, site)
	if err != nil || calls != 1 || out != `About|<b>trusted</b>{{ flatpage.title }}|example.test` || page.Title != "About" {
		t.Fatal("sanitized HTML or scalar snapshot changed", out, calls, err)
	}
}

func TestFlatpageRenderReservedContextAndStartupSnapshots(t *testing.T) {
	loader := templates.MapLoader{defaultPageTemplate: `{{ flatpage.title }}|{{ flatpage.content }}|{{ site.domain }}`}
	allowed := []string{defaultPageTemplate}
	processors := []templates.Processor{func(context.Context) (templates.Context, error) {
		return templates.Context{"flatpage": templates.Context{"title": "override", "content": templates.SafeHTML("<script>bad</script>")}, "site": templates.Context{"domain": "wrong.test"}}, nil
	}}
	options := RenderOptions{AllowedTemplates: allowed, Templates: templates.Config{Loaders: []templates.Loader{loader}, Processors: processors}}
	r := mustPageRenderer(t, options)
	loader[defaultPageTemplate] = "changed source"
	allowed[0] = "wrong.html"
	processors[0] = func(context.Context) (templates.Context, error) { panic("changed processor") }
	out, err := r.render(context.Background(), renderPageFixture(), renderSiteFixture())
	if err != nil || out != "About|Body|example.test" {
		t.Fatal("startup or reserved context snapshot changed", out, err)
	}
}

func TestFlatpageRenderAllowlistAndMissingTemplate(t *testing.T) {
	for _, options := range []RenderOptions{
		{DefaultTemplate: "../secret.html"}, {DefaultTemplate: "page.html#partial"}, {AllowedTemplates: []string{}},
		{AllowedTemplates: []string{"other.html"}}, {AllowedTemplates: []string{defaultPageTemplate, defaultPageTemplate}},
		{AllowedTemplates: make([]string, 65)}, {MaxOutputBytes: -1}, {MaxOutputBytes: (16 << 20) + 1},
		{Templates: templates.Config{Loaders: []templates.Loader{nil}}},
	} {
		if r, err := newPageRenderer(options); err != ErrConfiguration || r != nil {
			t.Fatal("invalid render configuration accepted", err)
		}
	}
	r := mustPageRenderer(t, RenderOptions{})
	if out, err := r.render(context.Background(), renderPageFixture(), renderSiteFixture()); err != ErrUnavailable || out != "" {
		t.Fatal("missing default template was silently supplied", out, err)
	}
	options := pageRenderOptions("default")
	options.AllowedTemplates = []string{defaultPageTemplate, "custom.html"}
	options.Templates.Loaders[0].(templates.MapLoader)["custom.html"] = "custom"
	calls := 0
	options.Sanitize = func(context.Context, Info) (templates.SafeHTML, error) { calls++; return "", nil }
	r = mustPageRenderer(t, options)
	page := renderPageFixture()
	page.TemplateName = "custom.html"
	if out, err := r.render(context.Background(), page, renderSiteFixture()); err != nil || out != "custom" {
		t.Fatal("explicit allowed template failed", out, err)
	}
	page.TemplateName = "other.html"
	if out, err := r.render(context.Background(), page, renderSiteFixture()); err != ErrUnavailable || out != "" || calls != 1 {
		t.Fatal("disallowed template reached sanitizer or returned body", out, calls, err)
	}
}

func TestFlatpageRenderDenialFailureCancellationAndBounds(t *testing.T) {
	for _, test := range []struct {
		name     string
		sanitize func(context.Context, Info) (templates.SafeHTML, error)
		want     error
	}{
		{"denied", func(context.Context, Info) (templates.SafeHTML, error) { return "private", ErrForbidden }, ErrForbidden},
		{"wrapped-denial", func(context.Context, Info) (templates.SafeHTML, error) {
			return "private", fmt.Errorf("provider-private: %w", ErrForbidden)
		}, ErrUnavailable},
		{"contradictory-denial", func(context.Context, Info) (templates.SafeHTML, error) {
			return "private", errors.Join(ErrForbidden, ErrUnavailable)
		}, ErrUnavailable},
		{"panic", func(context.Context, Info) (templates.SafeHTML, error) { panic("provider-private") }, ErrUnavailable},
		{"oversize", func(context.Context, Info) (templates.SafeHTML, error) {
			return templates.SafeHTML(strings.Repeat("x", MaxContentBytes+1)), nil
		}, ErrUnavailable},
		{"invalid-utf8", func(context.Context, Info) (templates.SafeHTML, error) { return templates.SafeHTML("\xff"), nil }, ErrUnavailable},
		{"nul", func(context.Context, Info) (templates.SafeHTML, error) { return templates.SafeHTML("\x00"), nil }, ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := pageRenderOptions(`prefix{{ flatpage.content }}`)
			options.Sanitize = test.sanitize
			r := mustPageRenderer(t, options)
			if out, err := r.render(context.Background(), renderPageFixture(), renderSiteFixture()); out != "" || err != test.want {
				t.Fatal("failed sanitizer exposed partial output or error", out, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	options := pageRenderOptions(`prefix{{ flatpage.content }}`)
	options.Sanitize = func(context.Context, Info) (templates.SafeHTML, error) { cancel(); return "private", nil }
	r := mustPageRenderer(t, options)
	if out, err := r.render(ctx, renderPageFixture(), renderSiteFixture()); err != context.Canceled || out != "" {
		t.Fatal("sanitizer cancellation returned output", out, err)
	}
	options = pageRenderOptions(`{{ flatpage.content }}`)
	options.MaxOutputBytes = 4
	r = mustPageRenderer(t, options)
	page := renderPageFixture()
	page.Content = "<"
	if out, err := r.render(context.Background(), page, renderSiteFixture()); err != nil || out != "&lt;" {
		t.Fatal("exact escaped output boundary failed", out, err)
	}
	page.Content = "<<"
	if out, err := r.render(context.Background(), page, renderSiteFixture()); err != ErrUnavailable || out != "" {
		t.Fatal("escaped output overflow returned partial body", out, err)
	}
}

type pageRenderContext struct {
	context.Context
	before   func()
	panicErr bool
}

func (c *pageRenderContext) Err() error {
	if c.panicErr {
		panic("private context failure")
	}
	if c.before != nil {
		before := c.before
		c.before = nil
		before()
	}
	return c.Context.Err()
}

func TestFlatpageRenderOperationSnapshotAndInvalidContexts(t *testing.T) {
	for _, replaceAtContext := range []bool{false, true} {
		t.Run(fmt.Sprintf("context=%v", replaceAtContext), func(t *testing.T) {
			other := mustPageRenderer(t, pageRenderOptions("replacement"))
			var r *pageRenderer
			options := pageRenderOptions(`{{ flatpage.content }}`)
			options.Sanitize = func(context.Context, Info) (templates.SafeHTML, error) {
				if !replaceAtContext {
					*r = *other
				}
				return "original", nil
			}
			r = mustPageRenderer(t, options)
			ctx := &pageRenderContext{Context: context.Background()}
			if replaceAtContext {
				ctx.before = func() { *r = *other }
			}
			if out, err := r.render(ctx, renderPageFixture(), renderSiteFixture()); err != nil || out != "original" {
				t.Fatal("callback replaced in-flight renderer", out, err)
			}
			if out, err := r.render(context.Background(), renderPageFixture(), renderSiteFixture()); err != nil || out != "replacement" {
				t.Fatal("subsequent call failed to observe replacement", out, err)
			}
		})
	}
	r := mustPageRenderer(t, pageRenderOptions("body"))
	var typedNil *pageRenderContext
	for _, ctx := range []context.Context{nil, typedNil, &pageRenderContext{Context: context.Background(), panicErr: true}} {
		if out, err := r.render(ctx, renderPageFixture(), renderSiteFixture()); err == nil || out != "" {
			t.Fatal("invalid context returned body", out, err)
		}
	}
}

func TestFlatpageRenderRejectsMalformedProviderMetadata(t *testing.T) {
	calls := 0
	options := pageRenderOptions("body")
	options.Sanitize = func(context.Context, Info) (templates.SafeHTML, error) { calls++; return "", nil }
	r := mustPageRenderer(t, options)
	for _, alter := range []func(*Info){
		func(p *Info) { p.ID = "invalid" }, func(p *Info) { p.URL = "/%7eabout" }, func(p *Info) { p.Title = "" },
		func(p *Info) { p.Content = strings.Repeat("x", MaxContentBytes+1) }, func(p *Info) { p.TemplateName = "page.html#partial" },
	} {
		page := renderPageFixture()
		alter(&page)
		if out, err := r.render(context.Background(), page, renderSiteFixture()); err != ErrInvalid || out != "" {
			t.Fatal("malformed page returned output", out, err)
		}
	}
	for _, alter := range []func(*sites.Info){
		func(s *sites.Info) { s.ID = "invalid" }, func(s *sites.Info) { s.Active = false }, func(s *sites.Info) { s.Domain = "EXAMPLE.TEST" },
		func(s *sites.Info) { s.DisplayName = "\x00" }, func(s *sites.Info) { s.DisplayName = strings.Repeat("x", 1021) },
	} {
		site := renderSiteFixture()
		alter(&site)
		if out, err := r.render(context.Background(), renderPageFixture(), site); err != ErrInvalid || out != "" {
			t.Fatal("malformed site returned output", out, err)
		}
	}
	if calls != 0 {
		t.Fatal("malformed metadata reached sanitizer", calls)
	}
}

type pageRenderLoader func(context.Context, string) (string, error)

func (f pageRenderLoader) Load(ctx context.Context, name string) (string, error) {
	return f(ctx, name)
}

func TestFlatpageRenderProviderFailureReturnsNoBody(t *testing.T) {
	for _, stage := range []string{"loader-error", "loader-panic", "processor-error", "processor-panic", "loader-cancel"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := pageRenderOptions(`prefix{{ flatpage.content }}`)
			if strings.HasPrefix(stage, "loader") {
				options.Templates.Loaders = []templates.Loader{pageRenderLoader(func(context.Context, string) (string, error) {
					switch stage {
					case "loader-error":
						return "private-source", errors.New("provider-private")
					case "loader-panic":
						panic("provider-private")
					default:
						cancel()
						return "private-source", nil
					}
				})}
			} else {
				options.Templates.Processors = []templates.Processor{func(context.Context) (templates.Context, error) {
					if stage == "processor-panic" {
						panic("provider-private")
					}
					return templates.Context{"private": "value"}, errors.New("provider-private")
				}}
			}
			r := mustPageRenderer(t, options)
			want := ErrUnavailable
			if stage == "loader-cancel" {
				want = context.Canceled
			}
			if out, err := r.render(ctx, renderPageFixture(), renderSiteFixture()); err != want || out != "" {
				t.Fatal("provider failure returned body or private error", out, err)
			}
		})
	}
}

func TestFlatpageRenderConcurrentRequestIsolation(t *testing.T) {
	r := mustPageRenderer(t, pageRenderOptions(`{{ flatpage.title }}:{{ flatpage.content }}:{{ site.domain }}`))
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Go(func() {
			page := renderPageFixture()
			page.Title = fmt.Sprintf("Page%d", i)
			for j := 0; j < 10; j++ {
				page.Content = fmt.Sprintf("Body%d", j)
				want := page.Title + ":" + page.Content + ":example.test"
				if out, err := r.render(context.Background(), page, renderSiteFixture()); err != nil || out != want {
					t.Error("concurrent render mixed request values", out, err)
					return
				}
			}
		})
	}
	group.Wait()
}
