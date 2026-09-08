package sitemaps

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testURLPolicy(t *testing.T) *urlPolicy {
	t.Helper()
	p, err := newURLPolicy(Config{Origin: "https://EXAMPLE.test.:443/", Directory: "/public", AdditionalOrigins: []string{"https://fr.example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSitemapURLPolicyCanonicalizationAndOriginScope(t *testing.T) {
	p := testURLPolicy(t)
	if p.origin.String() != "https://example.test" || p.directory != "/public/" {
		t.Fatal(p.origin, p.directory)
	}
	for raw, want := range map[string]string{
		"/public/article":                          "https://example.test/public/article",
		"https://EXAMPLE.test.:443/public/article": "https://example.test/public/article",
		"/public/日本語?q=français&ids[]=1":           "https://example.test/public/%E6%97%A5%E6%9C%AC%E8%AA%9E?q=fran%C3%A7ais&ids%5B%5D=1",
		"/public/a%20b?q=a+b&value=%26":            "https://example.test/public/a%20b?q=a+b&value=%26",
	} {
		entry, err := p.normalizeEntry(Entry{Loc: raw})
		if err != nil || entry.Loc != want {
			t.Fatalf("%q => %q, %v; want %q", raw, entry.Loc, err, want)
		}
	}
	for _, raw := range []string{
		"/publicity/a", "/private/a", "/public", "/public/../private", "/public/%2e%2e/private", "/public/a%2fb", "/public/%252e%252e/private", "/public/%25252fprivate", "/public//a", "/public/./a",
		"https://fr.example.test/public/a", "http://example.test/public/a", "https://example.test:444/public/a", "//example.test/public/a", "public/a", "javascript:alert(1)",
		"https://user:secret@example.test/public/a", "/public/a#section", "/public/a#", "/public/a\\b", "/public/a%5cb", "/public/a b", "/public/a%00", "/public/a?value=%0d", "/public/a?value=%FF", "/public/a?value=%5C", "/public/a?value=%",
	} {
		if entry, err := p.normalizeEntry(Entry{Loc: raw}); err == nil || !reflect.DeepEqual(entry, Entry{}) {
			t.Errorf("invalid %q returned metadata: %+v %v", raw, entry, err)
		}
		if entry, err := p.normalizeIndex(IndexEntry{Loc: raw}); err == nil || !reflect.DeepEqual(entry, IndexEntry{}) {
			t.Errorf("invalid index %q returned metadata: %+v %v", raw, entry, err)
		}
	}
	for _, origin := range []string{"https://[2001:db8::1]:8443", "http://192.0.2.1"} {
		p, err := newURLPolicy(Config{Origin: origin})
		if err != nil {
			t.Fatal(origin, err)
		}
		if entry, err := p.normalizeEntry(Entry{Loc: "/"}); err != nil || entry.Loc != origin+"/" {
			t.Fatal(entry, err)
		}
	}
}

func TestSitemapURLPolicyRejectsInvalidConfigurationAndFreezesOrigins(t *testing.T) {
	for _, origin := range []string{"", "https://例え.テスト", "https://[example.test]", "https://2001:db8::1", "https://[fe80::1%25zone]", "ftp://example.test", "https://example.test/path", "https://example.test?", "https://example.test#", "https://example.test:", "https://example.test:0443", "https://example.test:65536", "https://u:p@example.test", "https://bad..test"} {
		if p, err := newURLPolicy(Config{Origin: origin}); err == nil || p != nil {
			t.Errorf("invalid origin %q accepted", origin)
		}
	}
	for _, directory := range []string{"relative", "//private", "/a//b/", "/a/../", "/a/./", "/a/%2f", "/a?q=x", "/a#x", "/<slug:x>/", "/a\\b", "/日本語/"} {
		if _, err := newURLPolicy(Config{Origin: "https://example.test", Directory: directory}); err == nil {
			t.Errorf("invalid directory %q accepted", directory)
		}
	}
	config := Config{Origin: "https://example.test", AdditionalOrigins: []string{"https://fr.example.test"}}
	p, err := newURLPolicy(config)
	if err != nil {
		t.Fatal(err)
	}
	config.AdditionalOrigins[0] = "https://other.test"
	if _, err := p.location("https://fr.example.test/fr", true); err != nil {
		t.Fatal("configuration replacement changed existing policy", err)
	}
	if _, err := p.location("https://other.test/fr", true); err == nil {
		t.Fatal("replacement granted a new alternate origin")
	}
	for _, extras := range [][]string{{"https://example.test/"}, {"https://fr.test", "https://FR.test.:443"}} {
		if _, err := newURLPolicy(Config{Origin: "https://example.test", AdditionalOrigins: extras}); err == nil {
			t.Fatal("duplicate canonical origins accepted")
		}
	}
}

func TestSitemapAlternateLanguageSetsAreExplicitDetachedAndScoped(t *testing.T) {
	p := testURLPolicy(t)
	input := Entry{Loc: "/public/a", Alternates: []Alternate{{Language: "en-us", Loc: "/public/a"}, {Language: "fr", Loc: "https://fr.example.test/fr/a"}, {Language: "x-default", Loc: "/public/a"}, {Language: "en", Loc: "/public/a"}}}
	got, err := p.normalizeEntry(input)
	if err != nil || got.Alternates[0].Language != "en-US" || got.Alternates[2].Language != "x-default" {
		t.Fatal(got, err)
	}
	input.Alternates[0].Loc = "/private"
	if got.Alternates[0].Loc != "https://example.test/public/a" {
		t.Fatal("input aliases returned alternates")
	}
	for name, values := range map[string][]Alternate{
		"missing self":       {{Language: "fr", Loc: "https://fr.example.test/fr/a"}},
		"duplicate locale":   {{Language: "en-us", Loc: "/public/a"}, {Language: "en-US", Loc: "/public/other"}},
		"canonical alias":    {{Language: "iw", Loc: "/public/a"}, {Language: "he", Loc: "/public/other"}},
		"duplicate fallback": {{Language: "x-default", Loc: "/public/a"}, {Language: "X-DEFAULT", Loc: "/public/a"}},
		"unlisted origin":    {{Language: "en", Loc: "/public/a"}, {Language: "fr", Loc: "https://other.test/a"}},
		"invalid locale":     {{Language: "en US", Loc: "/public/a"}},
		"empty locale":       {{Loc: "/public/a"}},
		"root locale":        {{Language: "und", Loc: "/public/a"}},
	} {
		if entry, err := p.normalizeEntry(Entry{Loc: "/public/a", Alternates: values}); err == nil || !reflect.DeepEqual(entry, Entry{}) {
			t.Errorf("%s accepted: %+v %v", name, entry, err)
		}
	}
}

func TestSitemapMetadataPrecisionValidationAndDetachment(t *testing.T) {
	p := testURLPolicy(t)
	priority := 0.0
	zone := time.FixedZone("source", 3600)
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 123456789, zone)
	input := Entry{Loc: "/public/a", Priority: &priority, LastMod: LastModified{Time: stamp}, ChangeFreq: "monthly"}
	got, err := p.normalizeEntry(input)
	if err != nil || got.Priority == nil || *got.Priority != 0 {
		t.Fatal(got, err)
	}
	if value, err := lastModValue(got.LastMod); err != nil || value != "2026-01-02T02:04:05.123456789Z" {
		t.Fatal(value, err)
	}
	priority = 0.9
	*zone = *time.FixedZone("replaced", -7200)
	if *got.Priority != 0 || got.LastMod.Time.Hour() != 2 {
		t.Fatal("input mutable metadata reached normalized entry")
	}
	for _, date := range []string{"0001-01-01", "2024-02-29", "9999-12-31"} {
		if value, err := lastModValue(LastModified{Date: date}); err != nil || value != date {
			t.Fatal(date, value, err)
		}
	}
	for _, value := range []LastModified{{Date: "2023-02-29"}, {Date: "0000-01-01"}, {Date: "2026-1-01"}, {Date: "2026-01-01", Time: stamp}, {Time: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}, {Time: time.Date(1, 1, 1, 0, 0, 0, 0, time.FixedZone("east", 3600))}, {Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("invalid", 86400))}} {
		if entry, err := p.normalizeEntry(Entry{Loc: "/public/a", LastMod: value}); err == nil || !reflect.DeepEqual(entry, Entry{}) {
			t.Fatal("invalid lastmod accepted", value, err)
		}
	}
	for _, value := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := p.normalizeEntry(Entry{Loc: "/public/a", Priority: &value}); err == nil {
			t.Fatal("invalid priority accepted", value)
		}
	}
	if _, err := p.normalizeEntry(Entry{Loc: "/public/a", ChangeFreq: "sometimes"}); err == nil {
		t.Fatal("invalid change frequency accepted")
	}
}

func TestSitemapZeroTimestampLocationAndSeparateViewsNeverAlias(t *testing.T) {
	input := Entry{Loc: "/", LastMod: LastModified{}}
	one, two := cloneEntry(input), cloneEntry(input)
	if one.LastMod.Time.Location() == time.UTC || one.LastMod.Time.Location() == input.LastMod.Time.Location() || one.LastMod.Time.Location() == two.LastMod.Time.Location() {
		t.Fatal("unset timestamps share a mutable location")
	}
	*one.LastMod.Time.Location() = *time.FixedZone("changed", 3600)
	if _, offset := two.LastMod.Time.Zone(); offset != 0 {
		t.Fatal("view changed another view's location")
	}
	if value, err := lastModValue(two.LastMod); err != nil || value != "" || !two.LastMod.Time.IsZero() {
		t.Fatal("cloning invented an optional timestamp", value, err)
	}
	index := cloneIndexEntry(IndexEntry{})
	if index.LastMod.Time.Location() == time.UTC {
		t.Fatal("index unset timestamp leaked UTC location")
	}
}

func TestSitemapPreflightAndNormalizedURIBudgets(t *testing.T) {
	p := testURLPolicy(t)
	for _, entry := range []Entry{
		{Loc: strings.Repeat("a", 2048)},
		{Loc: "/public/a", LastMod: LastModified{Date: strings.Repeat("x", 11)}},
		{Loc: "/public/a", ChangeFreq: strings.Repeat("a", 17)},
		{Loc: "/public/a", Alternates: make([]Alternate, 65)},
		{Loc: "/public/a", Alternates: []Alternate{{Language: strings.Repeat("a", 129)}}},
		{Loc: "/public/a", Alternates: []Alternate{{Language: "en", Loc: strings.Repeat("a", 2048)}}},
	} {
		if _, err := entryInputSize(entry); !errors.Is(err, ErrLimit) {
			t.Fatal("preflight did not reject bounded shape", err)
		}
	}
	base := "https://example.test/public/"
	for _, n := range []int{maxLocationBytes, maxLocationBytes + 1} {
		entry, err := p.normalizeEntry(Entry{Loc: base + strings.Repeat("a", n-len(base))})
		if n == maxLocationBytes && (err != nil || len(entry.Loc) != n) || n > maxLocationBytes && !errors.Is(err, ErrLimit) {
			t.Fatal("wire URL boundary failed", n, err)
		}
	}
	for _, raw := range []string{"/public/" + strings.Repeat("é", 400), "/public/a?q=" + strings.Repeat("é", 400)} {
		if _, err := p.normalizeEntry(Entry{Loc: raw}); !errors.Is(err, ErrLimit) {
			t.Fatal("URI expansion bypassed output length bound", err)
		}
	}
}

func FuzzSitemapLocationScope(f *testing.F) {
	for _, raw := range []string{"/public/a", "/public/%2e%2e/private", "/public/%252fprivate", "/public/é?q=été", "https://user@example.test/public/a", "/public/a?x=%0a"} {
		f.Add(raw)
	}
	p, _ := newURLPolicy(Config{Origin: "https://example.test", Directory: "/public/"})
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			t.Skip()
		}
		got, err := p.normalizeEntry(Entry{Loc: raw})
		if err != nil {
			if !reflect.DeepEqual(got, Entry{}) {
				t.Fatal("error returned partial entry")
			}
			return
		}
		if !strings.HasPrefix(got.Loc, "https://example.test/public/") || len(got.Loc) > maxLocationBytes || strings.Contains(got.Loc, "#") {
			t.Fatal("normalization escaped scope", got.Loc)
		}
		for _, b := range []byte(got.Loc) {
			if b >= 128 {
				t.Fatal("output URI is not ASCII")
			}
		}
		if again, err := p.normalizeEntry(got); err != nil || again.Loc != got.Loc {
			t.Fatal("normalization is not stable", err)
		}
	})
}
