package templates

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/i18n"
)

func calendarContext(t testing.TB, zone string) context.Context {
	t.Helper()
	r, err := i18n.New(i18n.Config{Languages: []string{"en"}, DefaultTimeZone: zone})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := r.WithLocale(context.Background(), i18n.Preferences{})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestCalendarDatePreservesSelectedFieldsWithoutClockOrZone(t *testing.T) {
	ctx := calendarContext(t, "America/New_York")
	at := time.Date(2024, 2, 29, 0, 5, 7, 123456789, time.FixedZone("source", 14*3600))
	before := at
	for _, tc := range []struct{ format, want string }{
		{"c", "2024-02-29"}, {"I", ""}, {"Y-m-d I", "2024-02-29 "},
		{"b d D E F j l L m M n N o S t w W y Y z", "feb 29 Thu February February 29 Thursday True 02 Feb 2 Feb. 2024 th 29 4 9 24 2024 60"},
		{"DATE_FORMAT", "Feb. 29, 2024"}, {"SHORT_DATE_FORMAT", "02/29/2024"},
		{"YEAR_MONTH_FORMAT", "February 2024"}, {"MONTH_DAY_FORMAT", "February 29"},
		{`Y\Y`, "2024Y"}, {`\\H`, `\H`}, {`\\\H`, `\H`}, {`\\\\H`, `\\H`},
		{"", ""}, {"Q ☃", "Q ☃"}, {"\\\nY", "\\\n2024"},
	} {
		got, err := FormatCalendarDate(ctx, at, tc.format)
		if err != nil || got != tc.want {
			t.Fatal(tc, got, err)
		}
	}
	if at != before {
		t.Fatal("calendar display changed its input")
	}
	for _, year := range []int{1, 9999} {
		at := time.Date(year, 1, 1, 23, 59, 59, 0, time.UTC)
		if got, err := FormatCalendarDate(ctx, at, "c"); err != nil || got != at.Format("2006-01-02") {
			t.Fatal(year, got, err)
		}
	}
}

func TestCalendarDateRejectsTimeTokensButPreservesEscapedLiterals(t *testing.T) {
	at := time.Date(2024, 2, 29, 13, 5, 7, 0, time.UTC)
	for _, token := range "aAefgGhHiOPsTuZ" {
		if got, err := FormatCalendarDate(context.Background(), at, "Y "+string(token)); got != "" || !errors.Is(err, ErrRender) {
			t.Fatal("time token exposed source clock fields", string(token), got, err)
		}
		if got, err := FormatCalendarDate(context.Background(), at, `\`+string(token)); err != nil || got != string(token) {
			t.Fatal("escaped time token was evaluated", string(token), got, err)
		}
	}
	for _, name := range []string{"DATETIME_FORMAT", "SHORT_DATETIME_FORMAT", "TIME_FORMAT"} {
		if got, err := FormatCalendarDate(context.Background(), at, name); got != "" || !errors.Is(err, ErrRender) {
			t.Fatal("time-bearing named format accepted", name, got, err)
		}
	}
	// The existing datetime and time-only formatters keep their old behavior.
	if got, err := formatTemporal(at, "c H:i I", false); err != nil || got != "2024-02-29T13:05:07+00:00 13:05 0" {
		t.Fatal("datetime formatter changed", got, err)
	}
	if got, err := formatTemporal(at, "H:i:s", true); err != nil || got != "13:05:07" {
		t.Fatal("time formatter changed", got, err)
	}
}

func TestCalendarDateTimestampAnchorsUseContextMidnightNotSourceTime(t *testing.T) {
	at := time.Date(2024, 2, 29, 23, 5, 7, 123456789, time.FixedZone("source", -12*3600))
	for _, tc := range []struct{ zone, r, utc string }{
		{"UTC", "Thu, 29 Feb 2024 00:00:00 +0000", "2024-02-29T00:00:00Z"},
		{"Asia/Kolkata", "Thu, 29 Feb 2024 00:00:00 +0530", "2024-02-28T18:30:00Z"},
		{"America/New_York", "Thu, 29 Feb 2024 00:00:00 -0500", "2024-02-29T05:00:00Z"},
	} {
		ctx := calendarContext(t, tc.zone)
		instant, err := time.Parse(time.RFC3339, tc.utc)
		if err != nil {
			t.Fatal(err)
		}
		want := tc.r + "|" + strconv.FormatInt(instant.Unix(), 10) + "|" + tc.r
		if got, err := FormatCalendarDate(ctx, at, "r|U|r"); err != nil || got != want {
			t.Fatal(tc.zone, got, err, want)
		}
		locale, _ := i18n.FromContext(ctx)
		if locale.TimeZone() != tc.zone {
			t.Fatal("formatting mutated the locale")
		}
	}
	// An absent locale has an explicit UTC default, not the source/host zone.
	if got, err := FormatCalendarDate(context.Background(), at, "r"); err != nil || got != "Thu, 29 Feb 2024 00:00:00 +0000" {
		t.Fatal(got, err)
	}
	at = time.Date(1969, 12, 31, 23, 59, 59, 999999999, time.UTC)
	if got, err := FormatCalendarDate(context.Background(), at, "U"); err != nil || got != "-86400" {
		t.Fatal("source fractional time affected the midnight epoch", got, err)
	}
}

func TestCalendarDateMidnightResolutionIsLazyAndNeverGuesses(t *testing.T) {
	for _, tc := range []struct {
		zone  string
		year  int
		month time.Month
		day   int
		want  error
	}{
		{"America/Sao_Paulo", 2018, 11, 4, i18n.ErrNonexistentTime},
		{"America/Havana", 2020, 11, 1, i18n.ErrAmbiguousTime},
		{"Pacific/Apia", 2011, 12, 30, i18n.ErrNonexistentTime},
		{"Asia/Kolkata", 1, 1, 1, i18n.ErrInvalidTime},
	} {
		ctx := calendarContext(t, tc.zone)
		at := time.Date(tc.year, tc.month, tc.day, 12, 0, 0, 0, time.UTC)
		for _, format := range []string{"r", "U", "Y-m-d r", "c U"} {
			if got, err := FormatCalendarDate(ctx, at, format); got != "" || !errors.Is(err, tc.want) {
				t.Fatal("midnight was guessed or partial output escaped", tc.zone, format, got, err)
			}
		}
		for _, format := range []string{"c", "Y-m-d", `\r\U`, "I"} {
			if _, err := FormatCalendarDate(ctx, at, format); err != nil {
				t.Fatal("date-only display unnecessarily resolved midnight", tc.zone, format, err)
			}
		}
	}
}

func TestCalendarDateBoundsAndCancellationReturnNoPartialOutput(t *testing.T) {
	at := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, format := range []string{strings.Repeat("Y", 4097), "Y\x00", string([]byte{255})} {
		if got, err := FormatCalendarDate(context.Background(), at, format); got != "" || !errors.Is(err, ErrRender) {
			t.Fatal("invalid format accepted", got, err)
		}
	}
	for _, year := range []int{0, 10000} {
		if got, err := FormatCalendarDate(context.Background(), time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC), "c"); got != "" || !errors.Is(err, ErrRender) {
			t.Fatal("invalid year accepted", got, err)
		}
	}
	if got, err := FormatCalendarDate(nil, at, "c"); got != "" || !errors.Is(err, ErrRender) {
		t.Fatal("nil context accepted", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, format := range []string{"c", "U"} {
		if got, err := FormatCalendarDate(ctx, at, format); got != "" || !errors.Is(err, context.Canceled) {
			t.Fatal("canceled display returned text", got, err)
		}
	}
}
