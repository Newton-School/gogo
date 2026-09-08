package redirects

import (
	"errors"
	"strings"
	"testing"
)

func TestRedirectOriginCanonicalAuthority(t *testing.T) {
	valid := map[string]string{
		"HTTPS://Example.TEST.:443/":      "https://example.test",
		"http://example.test:80":          "http://example.test",
		"https://example.test:8443":       "https://example.test:8443",
		"https://123.example":             "https://123.example",
		"https://xn--bcher-kva.example":   "https://xn--bcher-kva.example",
		"http://127.0.0.1":                "http://127.0.0.1",
		"http://127.0.0.1.:80":            "http://127.0.0.1",
		"https://[2001:0DB8:0:0:0:0:0:1]": "https://[2001:db8::1]",
		"https://[::1]:8443":              "https://[::1]:8443",
	}
	for raw, want := range valid {
		got, err := normalizeOrigin(raw)
		if err != nil || got != want {
			t.Fatalf("origin %q=%q error=%v; want %q", raw, got, err, want)
		}
		if again, err := normalizeOrigin(got); err != nil || again != got {
			t.Fatalf("origin is not idempotent: %q %q %v", got, again, err)
		}
	}
	invalid := []string{
		"", "/", "//example.test", "javascript:alert(1)", "https:example.test", "ftp://example.test",
		"https://user:secret@example.test", "https://example.test?", "https://example.test#", "https://example.test/path",
		"https://example.test/%2f", "https://example.test..", "https://example.test:", "https://example.test:0", "https://example.test:65536",
		"https://example.test:0443", "https://example.test:+443", "https://example.test:4430000000000",
		"https://bücher.example", "https://exa%6dple.test", "https://example.test\\@evil.test", "https://example.test\n",
		"http://127.1", "http://2130706433", "http://0177.0.0.1", "http://0x7f000001", "http://127.0x0.0.1",
		"http://example.123", "http://example.0x10", "http://0X", "http://127.0.0.01", "http://256.0.0.1",
		"https://[example.test]", "https://[fe80::1%25eth0]", "https://2001:db8::1", "https://[::1",
	}
	for _, raw := range invalid {
		if got, err := normalizeOrigin(raw); !errors.Is(err, ErrInvalid) || got != "" {
			t.Fatalf("invalid origin accepted: %q => %q %v", raw, got, err)
		}
	}
}

func TestRedirectOldPathExactQueryAndSlash(t *testing.T) {
	valid := map[string]string{
		"/":                                 "/",
		"/old?":                             "/old",
		"/%7eold?q=a+b&x=%2f&x=%2F":         "/%7eold?q=a+b&x=%2f&x=%2F",
		"/old?b=2&a=1&a=3&":                 "/old?b=2&a=1&a=3&",
		"/café?q=雪":                         "/caf%C3%A9?q=%E9%9B%AA",
		"/a//b?next=https%3a%2f%2fx.test":   "/a//b?next=https%3a%2f%2fx.test",
		"/a%20b?q=%252f%252e%252e&x=%5B%5D": "/a%20b?q=%252f%252e%252e&x=%5B%5D",
	}
	for raw, want := range valid {
		got, err := parseOldPath(raw)
		if err != nil || got != want {
			t.Fatalf("old path %q=%q error=%v; want %q", raw, got, err, want)
		}
		if again, err := parseOldPath(got); err != nil || again != got {
			t.Fatalf("old path is not idempotent: %q %q %v", got, again, err)
		}
	}
	for _, test := range []struct {
		raw, want string
		changed   bool
	}{
		{"/old?x=1&x=2", "/old/?x=1&x=2", true},
		{"/old?", "/old/", true},
		{"/old/?x=1", "/old/?x=1", false},
		{"/", "/", false},
	} {
		got, changed, err := appendSlashKey(test.raw)
		if err != nil || got != test.want || changed != test.changed {
			t.Fatalf("slash %q=%q,%v,%v", test.raw, got, changed, err)
		}
	}
}

func TestRedirectTargetsSeparateLocationAndRequestKey(t *testing.T) {
	const source, origin = "/old?b=2&a=1&a=3", "https://example.test"
	for _, test := range []struct {
		raw      string
		preserve bool
		want     targetURI
	}{
		{"/new", false, targetURI{Location: "/new", Key: "/new", Origin: origin}},
		{"/new", true, targetURI{Location: "/new?b=2&a=1&a=3", Key: "/new?b=2&a=1&a=3", Origin: origin}},
		{"/new?", true, targetURI{Location: "/new?", Key: "/new", Origin: origin}},
		{"/new?#", true, targetURI{Location: "/new?#", Key: "/new", Origin: origin}},
		{"/new#", true, targetURI{Location: "/new?b=2&a=1&a=3#", Key: "/new?b=2&a=1&a=3", Origin: origin}},
		{"/new?own=1#résumé", true, targetURI{Location: "/new?own=1#r%C3%A9sum%C3%A9", Key: "/new?own=1", Origin: origin}},
		{"HTTPS://EXAMPLE.TEST.:443/new#anchor", false, targetURI{Location: "https://example.test/new#anchor", Key: "/new", Origin: origin}},
		{"https://other.test/new", true, targetURI{Location: "https://other.test/new?b=2&a=1&a=3", Origin: "https://other.test", External: true}},
		{"http://example.test/new", false, targetURI{Location: "http://example.test/new", Origin: "http://example.test", External: true}},
		{"https://example.test:8443/new", false, targetURI{Location: "https://example.test:8443/new", Origin: "https://example.test:8443", External: true}},
		{"https://example.test", true, targetURI{Location: "https://example.test/?b=2&a=1&a=3", Key: "/?b=2&a=1&a=3", Origin: origin}},
	} {
		got, err := parseTarget(test.raw, source, origin, test.preserve)
		if err != nil || got != test.want {
			t.Fatalf("target %q preserve=%v => %#v %v; want %#v", test.raw, test.preserve, got, err, test.want)
		}
	}
	if got, err := validateStoredTarget(""); got != "" || err != nil {
		t.Fatal("empty stored target must mean Gone", got, err)
	}
	if got, err := parseTarget("", source, origin, false); got != (targetURI{}) || !errors.Is(err, ErrInvalid) {
		t.Fatal("caller must handle Gone before target parsing", got, err)
	}
	if got, err := validateStoredTarget("/new?#"); err != nil || got != "/new?#" {
		t.Fatal("explicit empty components were discarded", got, err)
	}
}

func TestRedirectURIRejectsStructuralAmbiguity(t *testing.T) {
	invalid := []string{
		"", "next", "?q=1", "#fragment", "//evil.test", "///evil.test", "/\\evil.test",
		"/./new", "/a/../new", "/%2e/new", "/a/%2E%2e", "/a%2fb", "/a%5Cb",
		"/%252f%252fevil.test", "/%25252f", "/%252e%252e", "/%255c", "/%250a", "/%253f",
		"/a\n", "/a%0a", "/a%00", "/a%C2%80", "/a?x=%0D", "/a?x=%5c", "/a?x=%ff",
		"/%", "/%x0", "/a?x=%", "/a?x=%0g", "/a space", "/a?x=raw space", "/a?x=[raw]",
		"/a\x7f", "/a\xff", "/a#fragment",
	}
	for _, raw := range invalid {
		if got, err := parseOldPath(raw); !errors.Is(err, ErrInvalid) || got != "" {
			t.Fatalf("invalid old path accepted: %q => %q %v", raw, got, err)
		}
		if strings.Contains(raw, "#") {
			continue // A rooted target may have a fragment, unlike an old path.
		}
		if got, err := parseTarget(raw, "/old", "https://example.test", true); !errors.Is(err, ErrInvalid) || got != (targetURI{}) {
			t.Fatalf("invalid target accepted: %q => %#v %v", raw, got, err)
		}
	}
	for _, raw := range []string{"/a#%0a", "/a#%ff", "/a#bad\\value", "https://evil.test@other.test/", "javascript:alert(1)"} {
		if got, err := validateStoredTarget(raw); !errors.Is(err, ErrInvalid) || got != "" {
			t.Fatalf("invalid stored target accepted: %q => %q %v", raw, got, err)
		}
	}
}

func TestRedirectCycleIdentityUsesEffectiveQuery(t *testing.T) {
	a, err := cycleKey("/%7eold?q=%41&x=%2f&x=+", "HTTPS://EXAMPLE.TEST.:443")
	if err != nil {
		t.Fatal(err)
	}
	b, err := cycleKey("/~old?q=A&x=%2F&x=+", "https://example.test/")
	if err != nil || a != b {
		t.Fatal("equivalent encodings have different cycle identities", a, b, err)
	}
	for _, different := range []string{"/~old?q=A&x=+&x=%2F", "/~old?q=A&x=%2F&x=%20", "/~old?q=A&x=/&x=+"} {
		key, err := cycleKey(different, "https://example.test")
		if err != nil || key == a {
			t.Fatal("query semantics were collapsed", different, key, err)
		}
	}
	target, err := parseTarget("/old#new", "/old?q=1", "https://example.test", true)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := cycleKey("/old?q=1", target.Origin)
	after, err := cycleKey(target.Key, target.Origin)
	if err != nil || before != after {
		t.Fatal("query preservation hid self-cycle", before, after, err)
	}
	if key, err := cycleKey("/old#ignored", "https://example.test"); err == nil || key != "" {
		t.Fatal("cycle key accepted a non-request fragment", key, err)
	}
}

func TestRedirectURIBoundsBeforeAndAfterEncoding(t *testing.T) {
	limit := "/" + strings.Repeat("x", MaxURIBytes-1)
	if got, err := parseOldPath(limit); err != nil || got != limit {
		t.Fatal("exact bound rejected", len(got), err)
	}
	for _, raw := range []string{limit + "x", "/" + strings.Repeat("é", MaxURIBytes/2), "/" + strings.Repeat("é", MaxURIBytes/3)} {
		if got, err := parseOldPath(raw); err == nil || got != "" {
			t.Fatal("oversized raw/encoded key accepted", len(raw), len(got), err)
		}
	}
	if got, changed, err := appendSlashKey(limit); err == nil || changed || got != "" {
		t.Fatal("slash overflow returned a key", len(got), changed, err)
	}
	if got, err := parseTarget(limit, "/old?q=1", "https://example.test", true); err == nil || got != (targetURI{}) {
		t.Fatal("query-preservation overflow returned a partial target", got, err)
	}
}

func FuzzRedirectURI(f *testing.F) {
	for _, raw := range []string{"/", "/old?x=1&x=2", "/new?#", "/café?q=雪", "https://example.test/new", "/%252f", "http://127.1"} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		key, err := parseOldPath(raw)
		if err == nil {
			if len(key) > MaxURIBytes || strings.Contains(key, "#") {
				t.Fatal("invalid successful key")
			}
			if again, err := parseOldPath(key); err != nil || again != key {
				t.Fatal("unstable successful key")
			}
			if _, err := cycleKey(key, "https://example.test"); err != nil {
				t.Fatal("validated key failed comparison")
			}
		} else if key != "" {
			t.Fatal("partial old key on failure")
		}
		got, err := parseTarget(raw, "/old?x=1", "https://example.test", true)
		if err == nil {
			if len(got.Location) > MaxURIBytes || got.External && got.Key != "" {
				t.Fatal("invalid successful target")
			}
			if again, err := validateStoredTarget(got.Location); err != nil || again != got.Location {
				t.Fatal("unstable successful location")
			}
		} else if got != (targetURI{}) {
			t.Fatal("partial target on failure")
		}
	})
}
