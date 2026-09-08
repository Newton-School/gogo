package http

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/templates"
)

func TestGenericTemplateProcessorsUseEngineLocale(t *testing.T) {
	resolver, err := i18n.New(i18n.Config{
		Languages:       []string{"en", "fr"},
		TimeZones:       []string{"Asia/Kolkata", "Europe/Paris"},
		DefaultTimeZone: "Asia/Kolkata",
	})
	if err != nil {
		t.Fatal(err)
	}
	type scopeKey struct{}
	base := context.WithValue(context.Background(), scopeKey{}, "selected-scope")
	inherited, err := resolver.WithLocale(base, i18n.Preferences{Language: "fr", TimeZone: "Europe/Paris"})
	if err != nil {
		t.Fatal(err)
	}
	instant := time.Date(2026, 1, 2, 0, 15, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		ctx      context.Context
		resolver *i18n.Resolver
		want     string
	}{
		{"project_default", base, resolver, "en|Asia/Kolkata|2026-01-02 05:45 +0530|2026-01-02 05:45 +0530"},
		{"allowed_inherited", inherited, resolver, "fr|Europe/Paris|2026-01-02 01:15 +0100|2026-01-02 01:15 +0100"},
		{"inherited_without_resolver", inherited, nil, "fr|Europe/Paris|2026-01-02 01:15 +0100|2026-01-02 01:15 +0100"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler, err := NewTemplateView(TemplateViewOptions{
				ReadViewOptions: ReadViewOptions{Authorize: func(*http.Request) error { return nil }},
				TemplateName:    "locale.html",
				Templates: templates.Config{
					LocaleResolver: test.resolver,
					Loaders: []templates.Loader{templates.MapLoader{
						"locale.html": `{{ language }}|{{ zone }}|{{ clock }}|{{ at|date:"Y-m-d H:i O" }}`,
					}},
					Processors: []templates.Processor{func(ctx context.Context) (templates.Context, error) {
						calls++
						locale, found := i18n.FromContext(ctx)
						local, ok := templates.TimeValue(ctx, instant)
						if !found || !ok || ctx.Value(scopeKey{}) != "selected-scope" {
							t.Error("processor lost the resolved locale or inherited request scope")
						}
						return templates.Context{
							"language": locale.Language(), "zone": locale.TimeZone(),
							"clock": local.Format("2006-01-02 15:04 -0700"), "at": instant,
						}, nil
					}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/locale", nil).WithContext(test.ctx))
			if response.Code != 200 || calls != 1 || html.UnescapeString(response.Body.String()) != test.want {
				t.Fatal("processor and template used different locale rules", response.Code, calls, response.Body.String())
			}
		})
	}
}

type genericTemplateLocaleLoader func(context.Context, string) (string, error)

func (load genericTemplateLocaleLoader) Load(ctx context.Context, name string) (string, error) {
	return load(ctx, name)
}

func TestGenericTemplateRejectsForeignLocaleBeforeProcessorsAndLoaders(t *testing.T) {
	resolver, err := i18n.New(i18n.Config{Languages: []string{"en"}, TimeZones: []string{"UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, language, zone string
	}{
		{"language", "de", "UTC"},
		{"timezone", "en", "Europe/Paris"},
	} {
		t.Run(test.name, func(t *testing.T) {
			foreign, err := i18n.New(i18n.Config{Languages: []string{test.language}, DefaultTimeZone: test.zone})
			if err != nil {
				t.Fatal(err)
			}
			ctx, err := foreign.WithLocale(context.Background(), i18n.Preferences{})
			if err != nil {
				t.Fatal(err)
			}
			grants, processors, loads := 0, 0, 0
			handler, err := NewTemplateView(TemplateViewOptions{
				ReadViewOptions: ReadViewOptions{Authorize: func(*http.Request) error { grants++; return nil }},
				TemplateName:    "locale.html",
				Templates: templates.Config{
					LocaleResolver: resolver,
					Loaders: []templates.Loader{genericTemplateLocaleLoader(func(context.Context, string) (string, error) {
						loads++
						return "private locale output", nil
					})},
					Processors: []templates.Processor{func(context.Context) (templates.Context, error) {
						processors++
						return templates.Context{"private": "must not be read"}, nil
					}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/locale", nil).WithContext(ctx))
			if response.Code != 503 || grants != 1 || processors != 0 || loads != 0 || response.Body.String() != "Service Unavailable\n" || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("invalid inherited locale reached rendering callbacks or returned data", response.Code, grants, processors, loads, response.Body.String())
			}
		})
	}
}
