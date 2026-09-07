package templates

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/i18n"
)

func templateLocale(t *testing.T) *i18n.Resolver {
	t.Helper()
	resolver, err := i18n.New(i18n.Config{Languages: []string{"en", "fr"}, TimeZones: []string{"UTC", "Asia/Kolkata", "America/New_York", "Europe/Paris"}, DefaultTimeZone: "Asia/Kolkata"})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func templateLocaleContext(t *testing.T, resolver *i18n.Resolver, zone string) context.Context {
	t.Helper()
	ctx, err := resolver.WithLocale(context.Background(), i18n.Preferences{Language: "fr", TimeZone: zone})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestTemplateTimezoneDefaultsOverridesAndRestoration(t *testing.T) {
	resolver := templateLocale(t)
	engine := New(Config{LocaleResolver: resolver})
	ctx := templateLocaleContext(t, resolver, "America/New_York")
	instant := time.Date(2024, 11, 3, 5, 30, 0, 0, time.UTC)
	cases := []struct{ source, want string }{
		{`{% load tz %}{{ at|date:"Y-m-d H:i O" }}`, "2024-11-03 01:30 -0400"},
		{`{{ at|utc|date:"H:i O" }}`, "05:30 +0000"},
		{`{{ at|timezone:"Asia/Kolkata"|date:"H:i O" }}`, "11:00 +0530"},
		{`{{ at|utc|localtime|date:"H:i O" }}`, "01:30 -0400"},
		{`{% localtime off %}{{ at|date:"H:i O" }};{{ at|localtime|date:"H:i O" }}{% endlocaltime %};{{ at|date:"H:i O" }}`, "05:30 +0000;01:30 -0400;01:30 -0400"},
		{`{% localtime off %}{% localtime %}{{ at|date:"H:i" }}{% endlocaltime %};{{ at|date:"H:i" }}{% endlocaltime %}`, "01:30;05:30"},
		{`{% timezone "Europe/Paris" %}{{ at|date:"H:i" }};{% timezone None %}{{ at|date:"H:i" }}{% endtimezone %};{{ at|date:"H:i" }}{% endtimezone %};{{ at|date:"H:i" }}`, "06:30;11:00;06:30;01:30"},
		{`{% localtime off %}{% timezone "Europe/Paris" %}{{ at|date:"H:i" }};{{ at|localtime|date:"H:i" }}{% endtimezone %}{% endlocaltime %}`, "05:30;06:30"},
		{`{% get_current_timezone as zone %}{{ zone }};{% timezone "Europe/Paris" %}{% get_current_timezone as inner %}{{ inner }}{% endtimezone %};{% get_current_timezone as zone %}{{ zone }}`, "America/New_York;Europe/Paris;America/New_York"},
		{`{% with shown=at|utc %}{% timezone "Europe/Paris" %}{{ shown|date:"H:i O" }}{% endtimezone %}{% endwith %}`, "05:30 +0000"},
		{`{% with shown=at|utc %}{% if shown == at %}same{% endif %}{% if shown < later %}before{% endif %}{% endwith %}`, "samebefore"},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			out, err := engine.RenderString(ctx, tc.source, Context{"at": instant, "later": instant.Add(time.Hour)})
			if err != nil || html.UnescapeString(out) != tc.want {
				t.Fatalf("got %q %v; want %q", out, err, tc.want)
			}
			locale, _ := i18n.FromContext(ctx)
			if locale.TimeZone() != "America/New_York" || locale.Language() != "fr" || instant.Location() != time.UTC {
				t.Fatal("caller context or instant changed")
			}
		})
	}
	out, err := engine.RenderString(context.Background(), `{{ at|date:"H:i O" }}`, Context{"at": instant})
	if err != nil || html.UnescapeString(out) != "11:00 +0530" {
		t.Fatal(out, err)
	}
	out, err = New(Config{}).RenderString(context.Background(), `{{ at|date:"H:i O" }}`, Context{"at": instant.In(time.FixedZone("source", 3600))})
	if err != nil || html.UnescapeString(out) != "05:30 +0000" {
		t.Fatal("host timezone fallback", out, err)
	}
}

func TestTemplateTimezoneCompositionAndPointers(t *testing.T) {
	resolver := templateLocale(t)
	engine := New(Config{LocaleResolver: resolver, Loaders: []Loader{MapLoader{
		"base":  `before{% block body %}base{% endblock %}after{{ at|date:"H:i" }}`,
		"child": `{% extends "base" %}{% block body %}{% timezone "Europe/Paris" %}{% include "part" with at=at only %}{% endtimezone %}{% endblock %}`,
		"part":  `{{ at|date:"H:i" }};`,
	}}})
	instant := time.Date(2024, 11, 3, 5, 30, 0, 0, time.UTC)
	out, err := engine.Render(context.Background(), "child", Context{"at": &instant})
	if err != nil || out != "before06:30;after11:00" {
		t.Fatal(out, err)
	}
	for _, source := range []string{`{{ at }}`, `{% autoescape off %}{{ at }}{% endautoescape %}`} {
		out, err := engine.RenderString(context.Background(), source, Context{"at": instant})
		if err != nil || !strings.Contains(html.UnescapeString(out), "11:00:00 +0530") {
			t.Fatal(out, err)
		}
	}
	out, err = engine.RenderString(context.Background(), `{{ at|utc }}`, Context{"at": instant})
	if err != nil || !strings.Contains(html.UnescapeString(out), "05:30:00 +0000") {
		t.Fatal(out, err)
	}
	var absent *time.Time
	out, err = engine.RenderString(context.Background(), `{{ at|date:"H:i" }}{{ empty|localtime }}`, Context{"at": absent})
	if err != nil || out != "" {
		t.Fatal(out, err)
	}
}

func TestTemplateTimezoneDenialAndErrorIsolation(t *testing.T) {
	resolver := templateLocale(t)
	engine := New(Config{LocaleResolver: resolver})
	instant := time.Now().UTC()
	for _, source := range []string{
		`{% timezone "Local" %}x{% endtimezone %}`, `{% timezone "/etc/passwd" %}x{% endtimezone %}`,
		`{% timezone "Asia/Tokyo" %}x{% endtimezone %}`, `{% timezone True %}x{% endtimezone %}`,
		`{% timezone %}x{% endtimezone %}`, `{% timezone "UTC" extra %}x{% endtimezone %}`,
		`{% timezone "UTC" %}x{% endtimezone extra %}`, `{% localtime yes %}x{% endlocaltime %}`,
		`{% localtime on %}x{% endlocaltime extra %}`, `{% get_current_timezone %}`,
		`{% get_current_timezone as _secret %}`, `{% get_current_timezone as a.b %}`,
		`{% now "H:i" extra %}`, `{% now "H:i" as _secret %}`,
		`{{ at|timezone }}`, `{{ at|timezone:None }}`, `{{ at|timezone:"Local" }}`, `{{ at|utc:True }}`,
		`{% timezone "UTC" %}{% unknown %}{% endtimezone %}`,
	} {
		t.Run(source, func(t *testing.T) {
			out, err := engine.RenderString(context.Background(), source, Context{"at": instant})
			if err == nil || out != "" {
				t.Fatal("invalid timezone request returned output", out, err)
			}
			out, err = engine.RenderString(context.Background(), `{% get_current_timezone as z %}{{ z }}`, nil)
			if err != nil || out != "Asia/Kolkata" {
				t.Fatal("error leaked override", out, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := engine.RenderString(ctx, `{{ at|localtime }}`, Context{"at": instant}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	foreign, _ := i18n.New(i18n.Config{Languages: []string{"de"}})
	foreignCtx, _ := foreign.WithLocale(context.Background(), i18n.Preferences{})
	if out, err := engine.RenderString(foreignCtx, `{{ at|date:"H:i" }}`, Context{"at": instant}); out != "" || err == nil {
		t.Fatal("foreign project locale accepted", out, err)
	}
	if _, err := New(Config{}).RenderString(context.Background(), `{% timezone "UTC" %}x{% endtimezone %}`, nil); err == nil {
		t.Fatal("timezone loading without a resolver")
	}
	for _, source := range []string{`{{ at|date:format }}`, `{% now format %}`} {
		if _, err := engine.RenderString(context.Background(), source, Context{"at": instant, "format": strings.Repeat("Y", 4097)}); err == nil {
			t.Fatal("unbounded format accepted")
		}
	}
}

func TestTemplateTimezoneDSTConcurrencyAndEscaping(t *testing.T) {
	resolver := templateLocale(t)
	engine := New(Config{LocaleResolver: resolver, Loaders: []Loader{MapLoader{"page": `{% timezone zone %}{{ at|date:"Y-m-d H:i O" }}{% endtimezone %}|{{ at|date:"H:i O" }}`}}})
	ctx := templateLocaleContext(t, resolver, "America/New_York")
	for _, tc := range []struct {
		hour int
		want string
	}{{5, "01:30 -0400"}, {6, "01:30 -0500"}} {
		out, err := engine.RenderString(ctx, `{{ at|date:"H:i O" }}`, Context{"at": time.Date(2024, 11, 3, tc.hour, 30, 0, 0, time.UTC)})
		if err != nil || out != tc.want {
			t.Fatal(out, err)
		}
	}
	var wg sync.WaitGroup
	for index := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			zone, expected := "Europe/Paris", "2024-11-03 06:30 +0100"
			if index%2 == 0 {
				zone, expected = "Asia/Kolkata", "2024-11-03 11:00 +0530"
			}
			out, err := engine.Render(ctx, "page", Context{"zone": zone, "at": time.Date(2024, 11, 3, 5, 30, 0, 0, time.UTC)})
			if err != nil || html.UnescapeString(out) != expected+"|01:30 -0400" {
				t.Errorf("%q %v", out, err)
			}
		}()
	}
	wg.Wait()
	out, err := engine.RenderString(ctx, `<p>{{ at|date:format }}</p><script>let date={{ at|date:format }};</script>`, Context{"at": time.Now(), "format": `</script><script>`})
	if err != nil || strings.Contains(out, "</script><script>") || !strings.Contains(out, "&lt;") {
		t.Fatal(out, err)
	}
}

func TestTemplateTimezoneCustomFiltersAndDetachedLocations(t *testing.T) {
	resolver := templateLocale(t)
	zone := time.FixedZone("caller", 3600)
	instant := time.Date(2024, 11, 3, 5, 30, 0, 0, zone)
	engine := New(Config{LocaleResolver: resolver, Filters: map[string]Filter{
		"stamp": func(ctx context.Context, value, _ any) (any, error) {
			at, ok := TimeValue(ctx, value)
			if !ok {
				return nil, ErrRender
			}
			result := at.Format("15:04 -0700")
			*at.Location() = *time.UTC
			return result, nil
		},
		"mutate": func(_ context.Context, value, _ any) (any, error) {
			at := value.(time.Time)
			*at.Location() = *time.UTC
			return "", nil
		},
	}})
	out, err := engine.RenderString(context.Background(), `{{ at|utc|stamp }};{{ at|stamp }}`, Context{"at": instant})
	if err != nil || html.UnescapeString(out) != "04:30 +0000;10:00 +0530" || zone.String() != "caller" || resolver.Default().TimeZone() != "Asia/Kolkata" {
		t.Fatal(out, err, zone, resolver.Default())
	}
	if _, err := engine.RenderString(context.Background(), `{{ at|mutate }}`, Context{"at": &instant}); err != nil || zone.String() != "caller" {
		t.Fatal("source location exposed", err, zone)
	}
	if _, ok := TimeValue(nil, instant); ok {
		t.Fatal("nil context accepted")
	}
}

func TestTemplateNowUsesRequestTimezone(t *testing.T) {
	resolver := templateLocale(t)
	engine := New(Config{LocaleResolver: resolver})
	for _, source := range []string{`{% now "O" %}`, `{% now "O" as offset %}{{ offset }}`, `{% localtime off %}{% now "O" %}{% endlocaltime %}`} {
		out, err := engine.RenderString(context.Background(), source, nil)
		if err != nil || html.UnescapeString(out) != "+0530" {
			t.Fatal(out, err)
		}
	}
	out, err := engine.RenderString(context.Background(), `{% timezone "UTC" %}{% now "O" %}{% endtimezone %}`, nil)
	if err != nil || html.UnescapeString(out) != "+0000" {
		t.Fatal(out, err)
	}
	out, err = engine.RenderString(context.Background(), `{% localtime off %}{% timezone "UTC" %}{% now "O" %}{% endtimezone %};{% now "O" %}{% endlocaltime %}`, nil)
	if err != nil || html.UnescapeString(out) != "+0000;+0530" {
		t.Fatal(out, err)
	}
}

func FuzzTemplateTimezone(f *testing.F) {
	for _, source := range []string{`{{ value|utc|date:"c" }}`, `{% timezone "UTC" %}{{ value }}{% endtimezone %}`, `{% localtime off %}{{ value }}{% endlocaltime %}`, `{% timezone "UTC" %}{% endtimezone wrong %}`} {
		f.Add(source, int64(0))
	}
	resolver, err := i18n.New(i18n.Config{Languages: []string{"en"}, TimeZones: []string{"UTC", "America/New_York"}})
	if err != nil {
		f.Fatal(err)
	}
	engine := New(Config{LocaleResolver: resolver, MaxDepth: 8, MaxIterations: 100})
	f.Fuzz(func(t *testing.T, source string, seconds int64) {
		if len(source) > 4096 {
			t.Skip()
		}
		_, _ = engine.RenderString(context.Background(), source, Context{"value": time.Unix(seconds%253402300800, 123456789).UTC()})
		out, err := engine.RenderString(context.Background(), `{% get_current_timezone as zone %}{{ zone }}`, nil)
		if err != nil || out != "UTC" {
			t.Fatal(fmt.Sprint(out, err))
		}
	})
}
