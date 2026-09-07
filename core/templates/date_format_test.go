package templates

import (
	"context"
	"html"
	"strings"
	"testing"
	"time"
)

func TestDateFormatCharacters(t *testing.T) {
	// The full character catalog uses one fixed instant. Boundary cases below
	// separately exercise calendar, offsets and fractional values.
	at := time.Date(2024, time.February, 29, 13, 5, 7, 123456789, time.FixedZone("IST", 19800))
	wants := map[string]string{
		"a": "p.m.", "A": "PM", "b": "feb", "c": "2024-02-29T13:05:07.123456789+05:30",
		"d": "29", "D": "Thu", "e": "IST", "E": "February", "f": "1:05", "F": "February",
		"g": "1", "G": "13", "h": "01", "H": "13", "i": "05", "I": "0", "j": "29",
		"l": "Thursday", "L": "True", "m": "02", "M": "Feb", "n": "2", "N": "Feb.",
		"o": "2024", "O": "+0530", "P": "1:05 p.m.", "r": "Thu, 29 Feb 2024 13:05:07 +0530",
		"s": "07", "S": "th", "t": "29", "T": "IST", "u": "123456", "U": "1709192107",
		"w": "4", "W": "9", "y": "24", "Y": "2024", "z": "60", "Z": "19800",
	}
	for format, want := range wants {
		t.Run(format, func(t *testing.T) {
			out, err := formatTemporal(at, format, false)
			if err != nil || out != want {
				t.Fatalf("%s: %q %v want %q", format, out, err, want)
			}
		})
	}
}

func TestDateFormatCalendarAndFractionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		at           time.Time
		format, want string
	}{
		{time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), "Y o W z", "2021 2020 53 1"},
		{time.Date(2019, 12, 30, 0, 0, 0, 0, time.UTC), "Y o W", "2019 2020 1"},
		{time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), "Y y t L", "0001 01 31 False"},
		{time.Date(1900, 2, 1, 0, 0, 0, 0, time.UTC), "t L", "28 False"},
		{time.Date(2000, 2, 1, 0, 0, 0, 0, time.UTC), "t L", "29 True"},
		{time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC), "z", "366"},
		{time.Date(9999, 12, 1, 0, 0, 0, 0, time.UTC), "Y t", "9999 31"},
		{time.Unix(-1, 500000000).UTC(), "U", "0"},
		{time.Unix(-1, 0).UTC(), "U", "-1"},
		{time.Unix(-2, 123).UTC(), "U", "-1"},
		{time.Unix(0, 123).UTC(), "u c", "000000 1970-01-01T00:00:00.000000123+00:00"},
		{time.Unix(0, 0).UTC(), "c", "1970-01-01T00:00:00+00:00"},
		{time.Date(2024, 1, 1, 0, 0, 45, 0, time.UTC), "G g H h a A f P", "0 12 00 12 a.m. AM 12 midnight"},
		{time.Date(2024, 1, 1, 12, 0, 45, 0, time.UTC), "P", "noon"},
		{time.Date(2024, 1, 1, 0, 30, 0, 0, time.UTC), "P", "12:30 a.m."},
		{time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC), "G f P", "9 9 9 a.m."},
		{time.Date(2024, 1, 1, 23, 59, 0, 0, time.UTC), "P", "11:59 p.m."},
	} {
		out, err := formatTemporal(tc.at, tc.format, false)
		if err != nil || out != tc.want {
			t.Errorf("%v %q: %q %v want %q", tc.at, tc.format, out, err, tc.want)
		}
	}
	for day := 1; day <= 31; day++ {
		want := "th"
		if day != 11 && day != 12 && day != 13 {
			want = map[int]string{1: "st", 2: "nd", 3: "rd"}[day%10]
			if want == "" {
				want = "th"
			}
		}
		out, err := formatTemporal(time.Date(2024, 1, day, 0, 0, 0, 0, time.UTC), "S", false)
		if err != nil || out != want {
			t.Fatal(day, out, err)
		}
	}
}

func TestDateFormatEscapesBoundsAndTimeOnly(t *testing.T) {
	at := time.Date(2024, 2, 29, 13, 5, 7, 0, time.UTC)
	for _, tc := range []struct{ format, want string }{{`Y\Y`, "2024Y"}, {`H\h i\m`, "13h 05m"}, {`\\Y`, `\Y`}, {`\\\Y`, `\Y`}, {`\\\\Y`, `\\Y`}, {`Y\`, `2024\`}, {"Q ☃", "Q ☃"}, {"", ""}, {"\\\nY", "\\\n2024"}} {
		out, err := formatTemporal(at, tc.format, false)
		if err != nil || out != tc.want {
			t.Fatal(tc, out, err)
		}
	}
	for _, bad := range []string{strings.Repeat("Y", 4097), "Y\x00", string([]byte{255})} {
		if _, err := formatTemporal(at, bad, false); err == nil {
			t.Fatal("invalid format accepted")
		}
	}
	for _, char := range "bcdDEFIjlLmMnNortSUwWyYz" {
		if _, err := formatTemporal(at, string(char), true); err == nil {
			t.Fatal("date token in time-only format", string(char))
		}
		if out, err := formatTemporal(at, `\`+string(char), true); err != nil || out != string(char) {
			t.Fatal("escaped literal", out, err)
		}
	}
	if out, err := formatTemporal(at, "H:i e O T Z", true); err != nil || out != "13:05 UTC +0000 UTC 0" {
		t.Fatal(out, err)
	}
}

func TestDateFormatTimezonesAndExactInstants(t *testing.T) {
	for _, tc := range []struct {
		offset int
		want   string
	}{
		{19800, "+0530 19800 2024-01-02T03:04:05.500000+05:30"},
		{-12600, "-0330 -12600 2024-01-02T03:04:05.500000-03:30"},
		{561, "+0009 561 2024-01-02T03:04:05.500000+00:09:21"},
		{-45, "-0000 -45 2024-01-02T03:04:05.500000-00:00:45"},
	} {
		at := time.Date(2024, 1, 2, 3, 4, 5, 500000000, time.FixedZone("historical", tc.offset))
		out, err := formatTemporal(at, "O Z c", false)
		if err != nil || out != tc.want {
			t.Fatal(tc, out, err)
		}
	}
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		hour int
		want string
	}{{5, "1 EDT -0400"}, {6, "0 EST -0500"}} {
		at := time.Date(2024, 11, 3, tc.hour, 30, 0, 0, time.UTC).In(zone)
		out, err := formatTemporal(at, "I T O", false)
		if err != nil || out != tc.want {
			t.Fatal(tc, out, err)
		}
	}
	for _, at := range []time.Time{time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if _, err := formatTemporal(at, "c", false); err == nil {
			t.Fatal("unsupported year accepted")
		}
	}
	for _, offset := range []int{-86400, 86400, int(^uint(0) >> 1)} {
		if _, err := formatTemporal(time.Unix(0, 0).In(time.FixedZone("unsupported", offset)), "c", false); err == nil {
			t.Fatal("unsupported offset")
		}
	}
	at := time.Now().In(time.FixedZone(strings.Repeat("Z", 1024), 0))
	if out, err := formatTemporal(at, strings.Repeat("T", 1024), false); err == nil || out != "" {
		t.Fatal("unbounded timezone-name expansion")
	}
}

func TestDateFilterDefaultsAndNamedFormats(t *testing.T) {
	at := time.Date(2024, 2, 29, 13, 5, 7, 0, time.UTC)
	engine := New(Config{})
	for _, tc := range []struct{ source, want string }{
		{`{{ at|date }}`, "Feb. 29, 2024"}, {`{{ at|time }}`, "1:05 p.m."},
		{`{{ at|date:"" }}`, "Feb. 29, 2024"}, {`{{ at|time:"" }}`, "1:05 p.m."},
		{`{{ at|date:False }}`, "Feb. 29, 2024"}, {`{{ at|time:False }}`, "1:05 p.m."},
		{`{{ at|date:0 }}`, "Feb. 29, 2024"}, {`{{ at|time:0 }}`, "1:05 p.m."},
		{`{{ at|date:missing }}`, "Feb. 29, 2024"}, {`{{ at|time:missing }}`, "1:05 p.m."},
		{`{{ at|date:"DATE_FORMAT" }}`, "Feb. 29, 2024"},
		{`{{ at|date:"DATETIME_FORMAT" }}`, "Feb. 29, 2024, 1:05 p.m."},
		{`{{ at|date:"SHORT_DATE_FORMAT" }}`, "02/29/2024"},
		{`{{ at|date:"SHORT_DATETIME_FORMAT" }}`, "02/29/2024 1:05 p.m."},
		{`{{ at|date:"YEAR_MONTH_FORMAT" }}`, "February 2024"},
		{`{{ at|date:"MONTH_DAY_FORMAT" }}`, "February 29"},
		{`{{ at|time:"TIME_FORMAT" }}`, "1:05 p.m."},
	} {
		out, err := engine.RenderString(context.Background(), tc.source, Context{"at": at})
		if err != nil || html.UnescapeString(out) != tc.want {
			t.Fatal(tc, out, err)
		}
	}
	if out, err := engine.RenderString(context.Background(), `{{ at|time:"Y" }}`, Context{"at": at}); err != nil || out != "" {
		t.Fatal("time/date confusion", out, err)
	}
	if out, err := engine.RenderString(context.Background(), `{% now "" %}`, nil); err != nil || out != "" {
		t.Fatal("now must retain its explicit empty format", out, err)
	}
}

func FuzzDateFormat(f *testing.F) {
	for _, format := range []string{"c", "Y-m-d H:i O", "DATE_FORMAT", `\\Y`, `\\\Y`, "P U u", "\xff"} {
		f.Add(format, int64(0), uint32(0), false)
	}
	f.Fuzz(func(t *testing.T, format string, seconds int64, ns uint32, timeOnly bool) {
		if len(format) > 8192 {
			t.Skip()
		}
		at := time.Unix(seconds%253402300800, int64(ns%1000000000)).UTC()
		out, err := formatTemporal(at, format, timeOnly)
		if err == nil && len(out) > maxTemporalOutput {
			t.Fatal("unbounded date expansion")
		}
		if err != nil && out != "" {
			t.Fatal("partial failed date output")
		}
	})
}
