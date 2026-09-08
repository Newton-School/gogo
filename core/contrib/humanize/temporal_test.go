package humanize

import (
	"context"
	"errors"
	"html"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/templates"
)

func humanizeTime(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func temporalFormatter(t *testing.T, now time.Time) (*Formatter, *i18n.Resolver) {
	t.Helper()
	r, err := i18n.New(i18n.Config{Languages: []string{"en", "de"}, TimeZones: []string{"UTC", "America/New_York", "Asia/Kolkata"}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := New(Config{Resolver: r, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return f, r
}

func TestNaturalTimeElapsedUnitsAndNanosecondBoundaries(t *testing.T) {
	now := humanizeTime(t, "2026-07-10T12:00:00.1Z")
	f, _ := temporalFormatter(t, now)
	for _, tc := range []struct {
		delta time.Duration
		want  string
	}{{0, "now"}, {time.Second - time.Nanosecond, "now"}, {time.Second, "a second"}, {2 * time.Second, "2\u00a0seconds"}, {59 * time.Second, "59\u00a0seconds"}, {time.Minute, "a minute"}, {119 * time.Second, "a minute"}, {2 * time.Minute, "2\u00a0minutes"}, {time.Hour - time.Nanosecond, "59\u00a0minutes"}, {time.Hour, "an hour"}, {2 * time.Hour, "2\u00a0hours"}, {24*time.Hour - time.Nanosecond, "23\u00a0hours"}, {24 * time.Hour, "1\u00a0day"}, {25 * time.Hour, "1\u00a0day, 1\u00a0hour"}, {14*24*time.Hour + 3*time.Hour, "2\u00a0weeks"}} {
		for _, sign := range []time.Duration{-1, 1} {
			want := tc.want
			if want != "now" {
				if sign < 0 {
					want += " ago"
				} else {
					want += " from now"
				}
			}
			got, err := f.NaturalTime(context.Background(), now.Add(sign*tc.delta))
			if err != nil || got != want {
				t.Fatal(tc.delta, sign, got, err, want)
			}
		}
	}
}

func TestNaturalTimeCalendarUnitsLongSpansAndLeapPivot(t *testing.T) {
	for _, tc := range []struct{ first, last, want string }{
		{"2013-02-10T12:00:00Z", "2014-03-10T12:00:00Z", "1\u00a0year, 1\u00a0month"},
		{"2007-08-10T12:00:00Z", "2008-09-10T12:00:00Z", "1\u00a0year, 1\u00a0month"},
		{"2025-01-01T00:00:00Z", "2026-01-06T00:00:00Z", "1\u00a0year"},
		{"2026-01-31T00:00:00Z", "2026-02-28T00:00:00Z", "4\u00a0weeks"},
		{"2024-01-29T00:00:00Z", "2024-02-29T00:00:00Z", "1\u00a0month"},
		{"2024-01-29T00:00:00Z", "2024-03-07T00:00:00Z", "1\u00a0month, 1\u00a0week"},
		{"0001-01-01T00:00:00Z", "9999-12-31T23:59:59Z", "9998\u00a0years, 11\u00a0months"},
		{"2026-01-01T12:00:00.1Z", "2026-02-01T12:00:00Z", "4\u00a0weeks, 2\u00a0days"},
	} {
		first, last := humanizeTime(t, tc.first), humanizeTime(t, tc.last)
		f, _ := temporalFormatter(t, last)
		if got, err := f.NaturalTime(context.Background(), first); err != nil || got != tc.want+" ago" {
			t.Fatal(tc, got, err)
		}
		f, _ = temporalFormatter(t, first)
		if got, err := f.NaturalTime(context.Background(), last); err != nil || got != tc.want+" from now" {
			t.Fatal(tc, got, err)
		}
	}
}

func TestNaturalDayRequestZonesDSTAndFormatting(t *testing.T) {
	now := humanizeTime(t, "2024-03-11T04:30:00Z")
	f, r := temporalFormatter(t, now)
	ctx, _ := r.WithLocale(context.Background(), i18n.Preferences{TimeZone: "America/New_York"})
	for _, tc := range []struct{ at, format, want string }{
		{"2024-03-10T05:30:00Z", "", "yesterday"}, // 23 elapsed hours, one calendar day.
		{"2024-03-11T05:30:00Z", "", "today"},
		{"2024-03-12T05:30:00Z", "", "tomorrow"},
		{"2024-03-13T05:30:00Z", "Y-m-d", "2024-03-13"},
		{"2024-03-13T05:30:00Z", "", "March 13, 2024"},
	} {
		if got, err := f.NaturalDay(ctx, humanizeTime(t, tc.at), tc.format); err != nil || got != tc.want {
			t.Fatal(tc, got, err)
		}
	}
	if got, err := f.NaturalTime(ctx, humanizeTime(t, "2024-03-10T05:30:00Z")); err != nil || got != "23\u00a0hours ago" {
		t.Fatal(got, err)
	}
	f, r = temporalFormatter(t, humanizeTime(t, "2024-11-04T05:30:00Z"))
	ctx, _ = r.WithLocale(context.Background(), i18n.Preferences{TimeZone: "America/New_York"})
	if got, err := f.NaturalTime(ctx, humanizeTime(t, "2024-11-03T04:30:00Z")); err != nil || got != "1\u00a0day ago" {
		t.Fatal("calendar fallback counted repeated DST hour twice", got, err)
	}
}

func TestTemporalFiltersExplicitZonesNullsAndIsolation(t *testing.T) {
	now := humanizeTime(t, "2026-01-02T00:30:00Z")
	at := humanizeTime(t, "2026-01-01T23:30:00Z")
	f, r := temporalFormatter(t, now)
	ctx, _ := r.WithLocale(context.Background(), i18n.Preferences{TimeZone: "Asia/Kolkata"})
	engine := templates.New(templates.Config{Filters: f.Filters(), Libraries: []string{"humanize"}, LocaleResolver: r})
	for _, tc := range []struct{ source, want string }{
		{`{% load humanize %}{{ at|naturalday }}|{{ at|naturaltime }}`, "today|an hour ago"},
		{`{{ at|utc|naturalday }}|{{ at|naturalday }}`, "yesterday|today"},
		{`{% localtime off %}{{ at|naturalday }}{% endlocaltime %}|{{ at|naturalday }}`, "yesterday|today"},
		{`{% timezone "UTC" %}{{ at|naturalday }}{% endtimezone %}|{{ at|naturalday }}`, "yesterday|today"},
		{`{{ empty|naturalday }}|{{ empty|naturaltime }}`, "|"},
	} {
		got, err := engine.RenderString(ctx, tc.source, templates.Context{"at": &at, "empty": (*time.Time)(nil)})
		if err != nil || html.UnescapeString(got) != tc.want {
			t.Fatal(tc, got, err)
		}
	}
	for _, name := range []string{"naturalday", "naturaltime"} {
		for _, value := range []any{nil, (*time.Time)(nil)} {
			if got, err := f.Filters()[name](ctx, value, nil); err != nil || got != "" {
				t.Fatal(name, got, err)
			}
		}
		for _, value := range []any{"not a time", 42, struct{}{}} {
			if _, err := f.Filters()[name](ctx, value, nil); !errors.Is(err, ErrInvalidValue) {
				t.Fatal(name, value, err)
			}
		}
		if _, err := f.Filters()[name](ctx, nil, true); !errors.Is(err, ErrInvalidValue) {
			t.Fatal("invalid argument ignored for null", name, err)
		}
	}
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			zone, want := "UTC", "yesterday"
			if i%2 == 0 {
				zone, want = "Asia/Kolkata", "today"
			}
			local, err := r.WithLocale(ctx, i18n.Preferences{TimeZone: zone})
			if err != nil {
				t.Error(err)
				return
			}
			if got, err := f.NaturalDay(local, at, ""); err != nil || got != want {
				t.Error(zone, got, err)
			}
		}()
	}
	wg.Wait()
	locale, _ := i18n.FromContext(ctx)
	if locale.TimeZone() != "Asia/Kolkata" || at.Location() != time.UTC {
		t.Fatal("request or value mutated")
	}
}

func TestTemporalHumanizeTranslationAndStableFailures(t *testing.T) {
	now := humanizeTime(t, "2026-07-10T12:00:00Z")
	f, r := temporalFormatter(t, now)
	tr, err := i18n.NewTranslator(r, i18n.TranslatorConfig{Catalogs: []i18n.Catalog{{Language: "de", Domain: Domain, Messages: []i18n.Translation{
		{ID: "today", Text: "<b>heute</b>"},
		{ID: "a minute ago", Context: "naturaltime-past", Forms: map[i18n.PluralForm]string{i18n.One: "vor einer Minute", i18n.Other: "vor {value} Minuten"}},
		{ID: "{value} day", Context: "naturaltime-future", Forms: map[i18n.PluralForm]string{i18n.One: "{value} Tag", i18n.Other: "{value} Tage"}},
		{ID: "{value} from now", Context: "naturaltime-future", Text: "in {value}"},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	f, err = New(Config{Resolver: r, Translator: tr, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := r.WithLocale(context.Background(), i18n.Preferences{Language: "de"})
	if got, err := f.NaturalTime(ctx, now.Add(-2*time.Minute)); err != nil || got != "vor 2 Minuten" {
		t.Fatal(got, err)
	}
	if got, err := f.NaturalTime(ctx, now.Add(48*time.Hour)); err != nil || got != "in 2\u00a0Tage" {
		t.Fatal(got, err)
	}
	engine := templates.New(templates.Config{Filters: f.Filters(), LocaleResolver: r})
	if got, err := engine.RenderString(ctx, `{{ at|naturalday }}`, templates.Context{"at": now}); err != nil || got != "&lt;b&gt;heute&lt;/b&gt;" {
		t.Fatal(got, err)
	}
	for _, clock := range []func() time.Time{func() time.Time { panic("private clock error") }, func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }} {
		broken, _ := New(Config{Clock: clock})
		if got, err := broken.NaturalTime(context.Background(), now); got != "" || err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal(got, err)
		}
	}
	for _, at := range []time.Time{time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), now.In(time.FixedZone("invalid", 86400))} {
		if got, err := f.naturalTime(ctx, at); got != "" || !errors.Is(err, ErrInvalidValue) {
			t.Fatal(got, err)
		}
	}
	if got, err := f.NaturalDay(ctx, now.AddDate(0, 0, 3), strings.Repeat("Y", 4097)); got != "" || err == nil {
		t.Fatal("unbounded format", got, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	late, _ := New(Config{Resolver: r, Clock: func() time.Time { cancel(); return now }})
	if got, err := late.NaturalTime(canceled, now); got != "" || !errors.Is(err, context.Canceled) {
		t.Fatal("late clock cancellation", got, err)
	}
	if got, err := f.NaturalDay(canceled, now, ""); got != "" || !errors.Is(err, context.Canceled) {
		t.Fatal(got, err)
	}
	if got, err := f.NaturalTime(nil, now); got != "" || err == nil {
		t.Fatal(got, err)
	}
}

func TestNaturalDayDateOnlyFallbackUsesProjectMidnight(t *testing.T) {
	now := humanizeTime(t, "2026-07-10T00:00:00Z")
	at := humanizeTime(t, "2026-07-12T23:30:00Z")
	f, r := temporalFormatter(t, now)
	ctx, _ := r.WithLocale(context.Background(), i18n.Preferences{TimeZone: "Asia/Kolkata"})
	for _, tc := range []struct{ format, want string }{
		{"c", "2026-07-13"},
		{"Y-m-d I", "2026-07-13 "},
		{`Y-m-d \H\:\i`, "2026-07-13 H:i"},
		{"r", "Mon, 13 Jul 2026 00:00:00 +0000"},
		{"U", "1783900800"},
	} {
		if got, err := f.NaturalDay(ctx, at, tc.format); err != nil || got != tc.want {
			t.Fatal("date fallback retained a clock or shifted calendar fields", tc, got, err)
		}
	}
	for _, format := range []string{"H:i", "c O", "DATETIME_FORMAT", "TIME_FORMAT"} {
		if got, err := f.NaturalDay(ctx, at, format); got != "" || err == nil {
			t.Fatal("time tokens accepted for date-only value", format, got, err)
		}
	}
}

func FuzzTemporalHumanizeBoundedCalendar(f *testing.F) {
	for _, seconds := range []int64{-62135596800, 0, 253402300799, 1000000000, 1709251199} {
		f.Add(seconds)
	}
	h, err := New(Config{Clock: func() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, seconds int64) {
		at := time.Unix(seconds, 123456789).UTC()
		for _, fn := range []func() (string, error){func() (string, error) { return h.NaturalTime(context.Background(), at) }, func() (string, error) { return h.NaturalDay(context.Background(), at, "c") }} {
			out, err := fn()
			if err != nil && out != "" || len(out) > 64<<10 {
				t.Fatal("partial or unbounded output", out, err)
			}
		}
	})
}
