package i18n_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/i18n"
)

func resolver(t *testing.T, preference func(*http.Request) (i18n.Preferences, error)) *i18n.Resolver {
	t.Helper()
	r, err := i18n.New(i18n.Config{Languages: []string{"en", "fr", "zh-Hant", "hi"}, TimeZones: []string{"UTC", "Asia/Kolkata", "America/New_York"}, UserPreferences: preference})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLocaleSelectionPrecedenceAndTimezoneIndependence(t *testing.T) {
	for _, test := range []struct {
		name, cookie, accept string
		profile, override    i18n.Preferences
		language, zone       string
	}{
		{"default", "", "", i18n.Preferences{}, i18n.Preferences{}, "en", "UTC"},
		{"weighted", "", "fr;q=0.5, hi;q=0.9, en;q=0", i18n.Preferences{}, i18n.Preferences{}, "hi", "UTC"},
		{"variant", "", "fr-CA", i18n.Preferences{}, i18n.Preferences{}, "fr", "UTC"},
		{"script", "", "zh-TW", i18n.Preferences{}, i18n.Preferences{}, "zh-Hant", "UTC"},
		{"extensions_remain_untrusted", "", "en-u-ca-hebrew", i18n.Preferences{}, i18n.Preferences{}, "en", "UTC"},
		{"cookie", "gogo_language=fr", "hi", i18n.Preferences{}, i18n.Preferences{}, "fr", "UTC"},
		{"canonical_cookie", "gogo_language=FR", "hi", i18n.Preferences{}, i18n.Preferences{}, "fr", "UTC"},
		{"profile", "gogo_language=fr", "hi", i18n.Preferences{Language: "en", TimeZone: "Asia/Kolkata"}, i18n.Preferences{}, "en", "Asia/Kolkata"},
		{"removed_profile_language", "gogo_language=fr", "hi", i18n.Preferences{Language: "de", TimeZone: "Asia/Kolkata"}, i18n.Preferences{}, "fr", "Asia/Kolkata"},
		{"bad_profile_zone", "", "hi", i18n.Preferences{TimeZone: "../../etc/passwd"}, i18n.Preferences{}, "hi", "UTC"},
		{"explicit_override", "gogo_language=fr", "hi", i18n.Preferences{Language: "hi"}, i18n.Preferences{Language: "zh-Hant", TimeZone: "America/New_York"}, "zh-Hant", "America/New_York"},
		{"duplicate_cookie", "gogo_language=fr; gogo_language=en", "hi", i18n.Preferences{}, i18n.Preferences{}, "hi", "UTC"},
		{"unknown_cookie", "gogo_language=de", "hi", i18n.Preferences{}, i18n.Preferences{}, "hi", "UTC"},
		{"bad_header", "", "fr;q=broken,en", i18n.Preferences{}, i18n.Preferences{}, "en", "UTC"},
		{"zero_weights_are_advisory", "", "en;q=0,fr;q=0,hi;q=0", i18n.Preferences{}, i18n.Preferences{}, "en", "UTC"},
		{"empty_header", "", " , ", i18n.Preferences{}, i18n.Preferences{}, "en", "UTC"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			r := resolver(t, func(request *http.Request) (i18n.Preferences, error) {
				calls++
				// Profile code cannot silently alter lower-priority selectors.
				request.Header.Set("Cookie", "gogo_language=zh-Hant")
				return test.profile, nil
			})
			request := httptest.NewRequest("GET", "/", nil)
			request.Header.Set("Cookie", test.cookie)
			request.Header.Set("Accept-Language", test.accept)
			request.Header.Set("Time-Zone", "Asia/Kolkata")
			if test.override != (i18n.Preferences{}) {
				ctx, err := r.WithLocale(request.Context(), test.override)
				if err != nil {
					t.Fatal(err)
				}
				request = request.WithContext(ctx)
			}
			selected, err := r.Resolve(request)
			if err != nil || selected.Language() != test.language || selected.TimeZone() != test.zone {
				t.Fatal(selected.Preferences(), err)
			}
			if test.override != (i18n.Preferences{}) && calls != 0 {
				t.Fatal("explicit context override queried profile")
			}
			if request.Header.Get("Cookie") != test.cookie {
				t.Fatal("profile changed original headers")
			}
			if _, present := i18n.FromContext(request.Context()); present != (test.override != (i18n.Preferences{})) {
				t.Fatal("resolution mutated request context")
			}
		})
	}
}

func TestLocaleConfigurationIsFrozenAndExplicitOverridesAreStrict(t *testing.T) {
	languages, zones := []string{"fr", "en"}, []string{"UTC", "Asia/Kolkata"}
	r, err := i18n.New(i18n.Config{Languages: languages, DefaultLanguage: "en", TimeZones: zones})
	if err != nil {
		t.Fatal(err)
	}
	languages[1], zones[0] = "de", "Local"
	if r.Default().Language() != "en" || r.Default().TimeZone() != "UTC" {
		t.Fatal("constructor retained mutable configuration")
	}
	ctx, err := r.WithLocale(context.Background(), i18n.Preferences{Language: "fr", TimeZone: "Asia/Kolkata"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = r.WithLocale(ctx, i18n.Preferences{})
	if err != nil {
		t.Fatal(err)
	}
	selected, ok := i18n.FromContext(ctx)
	if !ok || selected.Language() != "fr" || selected.TimeZone() != "Asia/Kolkata" {
		t.Fatal(selected.Preferences())
	}
	for _, preferences := range []i18n.Preferences{{Language: "de"}, {Language: "fr-CA"}, {Language: "bad\r\nheader"}, {TimeZone: "Local"}, {TimeZone: "Europe/Paris"}} {
		if changed, err := r.WithLocale(ctx, preferences); !errors.Is(err, i18n.ErrInvalidLocale) || changed != nil {
			t.Fatal("explicit unsupported intent silently fell back", preferences, err)
		}
	}
	other, _ := i18n.New(i18n.Config{Languages: []string{"en"}})
	if _, err := other.Resolve(httptest.NewRequest("GET", "/", nil).WithContext(ctx)); !errors.Is(err, i18n.ErrInvalidLocale) {
		t.Fatal("cross-resolver allowlist widened", err)
	}
	if _, ok := i18n.FromContext(nil); ok {
		t.Fatal("nil context resolved")
	}
	for _, config := range []i18n.Config{
		{}, {Languages: []string{"en", "EN"}}, {Languages: []string{"und"}}, {Languages: []string{"bad!!"}},
		{Languages: []string{"en"}, DefaultLanguage: "fr"}, {Languages: []string{"en"}, DefaultTimeZone: "Local"},
		{Languages: []string{"en"}, TimeZones: []string{"Asia/Kolkata"}}, {Languages: []string{"en"}, TimeZones: []string{"UTC", "UTC"}},
		{Languages: []string{"en"}, TimeZones: []string{"../private"}}, {Languages: []string{"en"}, LanguageCookie: "bad cookie"},
		{Languages: []string{"en"}, LanguageCookie: "x\r\ny"}, {Languages: make([]string, 257)},
		{Languages: []string{"en"}, TimeZones: make([]string, 257)},
	} {
		if _, err := i18n.New(config); !errors.Is(err, i18n.ErrInvalidLocale) {
			t.Fatal("invalid configuration accepted", err)
		}
	}
}

func TestLocaleHeaderBudgetsAndDisabledCookie(t *testing.T) {
	r := resolver(t, nil)
	for _, headers := range []http.Header{
		{"Accept-Language": {strings.Repeat("fr,", 33)}},
		{"Accept-Language": {"fr," + strings.Repeat(" ", 4096)}},
		{"Accept-Language": slices.Repeat([]string{"fr"}, 17)},
		{"Cookie": {"gogo_language=fr; x=" + strings.Repeat("a", 16<<10)}},
		{"Cookie": slices.Repeat([]string{"gogo_language=fr"}, 33)},
	} {
		request := httptest.NewRequest("GET", "/", nil)
		request.Header = headers
		locale, err := r.Resolve(request)
		if err != nil || locale.Language() != "en" {
			t.Fatal("oversized selector was partially accepted", locale.Preferences(), err)
		}
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Add("Accept-Language", "fr;q=0.4")
	request.Header.Add("Accept-Language", "hi;q=0.9")
	if locale, err := r.Resolve(request); err != nil || locale.Language() != "hi" {
		t.Fatal(locale.Preferences(), err)
	}
	disabled, _ := i18n.New(i18n.Config{Languages: []string{"en", "fr"}, DisableCookie: true})
	request.Header = http.Header{"Cookie": {"gogo_language=fr"}}
	response := httptest.NewRecorder()
	disabled.Middleware(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		locale, _ := i18n.FromContext(request.Context())
		if locale.Language() != "en" {
			t.Fatal("disabled cookie selected language")
		}
		w.WriteHeader(204)
	})).ServeHTTP(response, request)
	if strings.Contains(strings.Join(response.Header().Values("Vary"), ","), "Cookie") || len(response.Header().Values("Set-Cookie")) != 0 {
		t.Fatal(response.Header())
	}
}

func TestLocaleMiddlewareFailsClosedAndSetsCacheDimensions(t *testing.T) {
	for _, mode := range []string{"error", "panic", "cancel", "deadline", "success"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := resolver(t, func(*http.Request) (i18n.Preferences, error) {
				switch mode {
				case "error":
					return i18n.Preferences{}, errors.New("private database detail")
				case "panic":
					panic("private profile detail")
				case "cancel":
					cancel()
				}
				return i18n.Preferences{Language: "fr", TimeZone: "Asia/Kolkata"}, nil
			})
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer stop()
			}
			handled := false
			w := httptest.NewRecorder()
			w.Header().Set("Vary", "accept-language, X-Project")
			r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				handled = true
				locale, ok := i18n.FromContext(request.Context())
				if !ok || locale.Language() != "fr" || locale.TimeZone() != "Asia/Kolkata" {
					t.Fatal(locale.Preferences())
				}
				w.WriteHeader(204)
			})).ServeHTTP(w, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
			if strings.Contains(w.Body.String(), "private") || w.Header().Get("Cache-Control") != "private, no-store" || len(w.Header().Values("Set-Cookie")) != 0 {
				t.Fatal(w.Code, w.Header(), w.Body.String())
			}
			vary := strings.Join(w.Header().Values("Vary"), ",")
			for _, value := range []string{"accept-language", "X-Project", "Cookie", "Authorization"} {
				if !strings.Contains(vary, value) {
					t.Fatal(vary)
				}
			}
			if strings.Count(strings.ToLower(vary), "accept-language") != 1 {
				t.Fatal("duplicate Vary", vary)
			}
			if mode == "success" {
				if !handled || w.Code != 204 || w.Header().Get("Content-Language") != "fr" {
					t.Fatal(w.Code, handled, w.Header())
				}
			} else if handled || w.Code < 500 {
				t.Fatal("failed preference reached application", w.Code, handled)
			}
		})
	}
}

func TestLocaleConcurrentRequestsAndCalendarConversionDoNotMutateGlobals(t *testing.T) {
	r := resolver(t, nil)
	global := time.Local
	var workers sync.WaitGroup
	errorsFound := make(chan error, 200)
	for n := 0; n < 200; n++ {
		workers.Go(func() {
			language := []string{"en", "fr", "hi"}[n%3]
			ctx, err := r.WithLocale(context.Background(), i18n.Preferences{Language: language, TimeZone: "Asia/Kolkata"})
			if err != nil {
				errorsFound <- err
				return
			}
			selected, err := r.Resolve(httptest.NewRequest("GET", "/", nil).WithContext(ctx))
			if err != nil || selected.Language() != language || selected.TimeZone() != "Asia/Kolkata" {
				errorsFound <- fmt.Errorf("request locale leaked across callers")
				return
			}
		})
	}
	workers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	if time.Local != global || r.Default().Language() != "en" || r.Default().TimeZone() != "UTC" {
		t.Fatal("global/default state changed")
	}
	ctx, _ := r.WithLocale(context.Background(), i18n.Preferences{TimeZone: "America/New_York"})
	locale, _ := i18n.FromContext(ctx)
	// Distinct UTC instants in the DST fold retain their instant identity even
	// though their local wall-clock representation has the same hour/minute.
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	a, b := locale.LocalTime(first), locale.LocalTime(second)
	_, offsetA := a.Zone()
	_, offsetB := b.Zone()
	if !a.Equal(first) || !b.Equal(second) || a.Hour() != b.Hour() || offsetA == offsetB || first.Location() != time.UTC {
		t.Fatal("display conversion changed stored instant or collapsed fold")
	}
}

func TestLocaleLocationPointersCannotChangeResolverState(t *testing.T) {
	r := resolver(t, nil)
	ctx, err := r.WithLocale(context.Background(), i18n.Preferences{TimeZone: "Asia/Kolkata"})
	if err != nil {
		t.Fatal(err)
	}
	locale, _ := i18n.FromContext(ctx)
	location := locale.Location()
	*location = *time.UTC
	instant := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	converted := locale.LocalTime(instant)
	*converted.Location() = *time.UTC
	*locale.Location() = *time.FixedZone("changed", 3600)
	zero := i18n.Locale{}
	*zero.Location() = *time.FixedZone("changed", 3600)
	for _, selected := range []i18n.Locale{locale, r.Default()} {
		got := selected.LocalTime(instant)
		_, offset := got.Zone()
		want := 0
		if selected.TimeZone() == "Asia/Kolkata" {
			want = 19800
		}
		if offset != want || !got.Equal(instant) {
			t.Fatal("exposed location changed a shared selection", selected.Preferences(), got)
		}
	}
	if locale.TimeZone() != "Asia/Kolkata" || r.Default().TimeZone() != "UTC" || time.UTC.String() != "UTC" {
		t.Fatal("resolver or global timezone mutated")
	}
	resolved, err := r.Resolve(httptest.NewRequest("GET", "/", nil).WithContext(ctx))
	if err != nil || resolved.TimeZone() != "Asia/Kolkata" {
		t.Fatal(resolved.Preferences(), err)
	}
}

func FuzzLocaleUntrustedHeadersStayWithinAllowlist(f *testing.F) {
	r, err := i18n.New(i18n.Config{Languages: []string{"en", "fr", "hi"}})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{"fr", "hi;q=0.8,en;q=0", "*", "bad\x00tag", "fr;q=NaN", "en-u-ca-hebrew"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 20000 {
			t.Skip()
		}
		request := httptest.NewRequest("GET", "/", nil)
		request.Header.Set("Accept-Language", input)
		request.Header.Set("Cookie", "gogo_language="+input)
		selected, err := r.Resolve(request)
		if err != nil || !slices.Contains([]string{"en", "fr", "hi"}, selected.Language()) || selected.TimeZone() != "UTC" {
			t.Fatal("untrusted locale escaped allowlist", selected.Preferences(), err)
		}
	})
}
