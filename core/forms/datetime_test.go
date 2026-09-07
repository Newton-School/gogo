package forms

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/i18n"
)

func dateTimeContext(t *testing.T, zone string) context.Context {
	t.Helper()
	resolver, err := i18n.New(i18n.Config{Languages: []string{"en"}, DefaultTimeZone: zone})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := resolver.WithLocale(context.Background(), i18n.Preferences{})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestDateTimeWidgetRoundTripAndChangedDataUseCurrentZone(t *testing.T) {
	ctx := dateTimeContext(t, "Asia/Kolkata")
	initial := time.Date(2026, time.September, 5, 9, 0, 5, 123456789, time.UTC)
	for _, kind := range []Kind{DateTime, SplitDateTime} {
		field := NewField("when", kind)
		form, err := New([]Field{field}, WithContext(ctx), WithInitial(map[string]any{"when": initial}), WithPrefix("event"))
		if err != nil {
			t.Fatal(err)
		}
		out, err := form.Render("div")
		if err != nil {
			t.Fatal(err)
		}
		data := url.Values{"event-when": {"2026-09-05T14:30:05.123456789"}}
		if kind == SplitDateTime {
			data = url.Values{"event-when_0": {"2026-09-05"}, "event-when_1": {"14:30:05.123456789"}}
		}
		for _, values := range data {
			if !strings.Contains(string(out), `value="`+values[0]+`"`) {
				t.Fatal("initial value did not render in current timezone", kind, out)
			}
		}
		bound, err := New([]Field{field}, WithContext(ctx), WithInitial(map[string]any{"when": initial}), WithData(data), WithPrefix("event"))
		if err != nil || !bound.IsValid() || bound.HasChanged() {
			t.Fatal("widget did not roundtrip", kind, err, bound.Errors(), bound.ChangedData())
		}
		got := bound.CleanedData()["when"].(time.Time)
		if !got.Equal(initial) || got.Location() != time.UTC || initial.Location() != time.UTC {
			t.Fatal("cleaning changed instant", kind, got, initial)
		}
	}
}

func TestDateTimeAwareInputsDisambiguateFoldsWhileNaiveValuesFail(t *testing.T) {
	ctx := dateTimeContext(t, "America/New_York")
	for _, input := range []string{"2026-03-08T02:30", "2026-11-01T01:30:00"} {
		form, _ := New([]Field{NewField("when", DateTime)}, WithContext(ctx), WithData(url.Values{"when": {input}}))
		if form.IsValid() || !form.HasError("when", "ambiguous_timezone") {
			t.Fatal(input, form.Errors())
		}
		out, err := form.Render("div")
		if err != nil || !strings.Contains(string(out), `value="`+input+`"`) || !strings.Contains(string(out), `aria-invalid="true"`) {
			t.Fatal("invalid input was not preserved accessibly", out, err)
		}
	}
	for _, input := range []string{"2026-11-01T01:30:00-04:00", "2026-11-01T01:30:00-05:00"} {
		form, _ := New([]Field{NewField("when", DateTime)}, WithContext(ctx), WithData(url.Values{"when": {input}}))
		want, _ := time.Parse(time.RFC3339, input)
		if !form.IsValid() || !form.CleanedData()["when"].(time.Time).Equal(want) {
			t.Fatal(input, form.Errors())
		}
	}
	for _, values := range []url.Values{{"when_0": {"2026-11-01"}, "when_1": {"01:30"}}, {"when_0": {"2026-03-08"}, "when_1": {"02:30"}}} {
		form, _ := New([]Field{NewField("when", SplitDateTime)}, WithContext(ctx), WithData(values))
		if form.IsValid() || !form.HasError("when", "ambiguous_timezone") {
			t.Fatal(form.Errors())
		}
	}
}

func TestDisabledTypedDateTimesRetainExactFoldInstantAndComponentValidation(t *testing.T) {
	ctx := dateTimeContext(t, "America/New_York")
	initial := time.Date(2026, time.November, 1, 6, 30, 0, 0, time.UTC)
	for _, kind := range []Kind{DateTime, SplitDateTime} {
		field := NewField("when", kind)
		field.Disabled = true
		form, _ := New([]Field{field}, WithContext(ctx), WithInitial(map[string]any{"when": initial}), WithData(url.Values{"when": {"forged"}, "when_0": {"bad"}, "when_1": {"bad"}}))
		if !form.IsValid() || form.HasChanged() || !form.CleanedData()["when"].(time.Time).Equal(initial) {
			t.Fatal(kind, form.Errors())
		}
	}
	field := NewField("when", SplitDateTime)
	field.Disabled = true
	day := NewField("date", Date)
	day.Validators = []Validator{func(context.Context, any) error { return Error{Code: "not_allowed", Message: "Date unavailable."} }}
	field.Fields = []Field{day, NewField("time", Time)}
	form, _ := New([]Field{field}, WithContext(ctx), WithInitial(map[string]any{"when": initial}), WithData(url.Values{}))
	if form.IsValid() || !form.HasError("when", "not_allowed") {
		t.Fatal("typed initial bypassed component validators", form.Errors())
	}
}

func TestDateTimeDefaultCustomFormatsAndCalendarOnlyValues(t *testing.T) {
	ctx := dateTimeContext(t, "Asia/Kolkata")
	for _, input := range []string{"2026-09-05T14:30", "2026-09-05T14:30:00", "2026-09-05 14:30", "2026-09-05 14:30:00"} {
		form, _ := New([]Field{NewField("when", DateTime)}, WithContext(ctx), WithData(url.Values{"when": {input}}))
		if !form.IsValid() || form.CleanedData()["when"].(time.Time).Format(time.RFC3339) != "2026-09-05T09:00:00Z" {
			t.Fatal(input, form.Errors())
		}
	}
	custom := NewField("when", DateTime)
	custom.InputFormats = []string{"02/01/2006 15:04"}
	form, _ := New([]Field{custom}, WithContext(ctx), WithData(url.Values{"when": {"05/09/2026 14:30"}}))
	if !form.IsValid() || form.CleanedData()["when"].(time.Time).Hour() != 9 {
		t.Fatal(form.Errors())
	}
	custom.InputFormats = []string{"2006-01-02 15:04 MST"}
	form, _ = New([]Field{custom}, WithContext(ctx), WithData(url.Values{"when": {"2026-09-05 14:30 XYZ"}}))
	if form.IsValid() {
		t.Fatal("fabricated abbreviation selected an instant")
	}
	form, _ = New([]Field{NewField("date", Date), NewField("time", Time)}, WithContext(ctx), WithData(url.Values{"date": {"2026-09-05"}, "time": {"14:30"}}))
	if !form.IsValid() || form.CleanedData()["date"].(time.Time).Day() != 5 || form.CleanedData()["time"].(time.Time).Hour() != 14 {
		t.Fatal("calendar-only fields shifted timezone", form.Errors())
	}
	form, _ = New([]Field{NewField("when", DateTime)}, WithData(url.Values{"when": {"2026-09-05T14:30:00"}}))
	if !form.IsValid() || form.CleanedData()["when"].(time.Time).Hour() != 14 {
		t.Fatal("missing locale must use UTC", form.Errors())
	}
}
