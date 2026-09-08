package http

import (
	"net/url"
	"strings"
	"testing"
)

func TestGenericRedirectTargetLiteralQueryAndFragment(t *testing.T) {
	for _, test := range []struct {
		raw, query, want string
		absolute         bool
	}{
		{"/", "", "/", false},
		{"/literal/%41/%25?q=%2f&x=+", "", "/literal/%41/%25?q=%2f&x=+", false},
		{"/new?", "", "/new?", false},
		{"/new?#", "", "/new?#", false},
		{"/new#", "x=1&x=2", "/new?x=1&x=2#", false},
		{"/new?#part", "x=1&x=2", "/new?x=1&x=2#part", false},
		{"/new?own=1#part", "b=2&a=1&a=3", "/new?own=1&b=2&a=1&a=3#part", false},
		{"/new?own=1&", "x=1", "/new?own=1&&x=1", false},
		{"/café?q=雪#résumé", "u=é", "/caf%C3%A9?q=%E9%9B%AA&u=%C3%A9#r%C3%A9sum%C3%A9", false},
		{"/a//b?next=https%3a%2f%2fx.test", "next=%252f%252e%252e", "/a//b?next=https%3a%2f%2fx.test&next=%252f%252e%252e", false},
		{"HTTPS://EXAMPLE.TEST.:443/new#part", "x=1", "https://example.test/new?x=1#part", true},
		{"https://example.test", "", "https://example.test/", true},
		{"http://example.test:80?", "", "http://example.test/?", true},
		{"https://example.test:8443/", "", "https://example.test:8443/", true},
		{"https://123.example/", "", "https://123.example/", true},
		{"https://xn--bcher-kva.example/", "", "https://xn--bcher-kva.example/", true},
		{"http://127.0.0.1.:80/", "", "http://127.0.0.1/", true},
		{"https://[2001:0DB8:0:0:0:0:0:1]:443/", "", "https://[2001:db8::1]/", true},
		{"https://[::1]:8443/", "", "https://[::1]:8443/", true},
	} {
		got, absolute, err := genericRedirectTarget(test.raw, test.query)
		if err != nil || got != test.want || absolute != test.absolute {
			t.Fatalf("target %q query %q: got %q, %v, %v; want %q, %v", test.raw, test.query, got, absolute, err, test.want, test.absolute)
		}
		if again, abs, err := genericRedirectTarget(got, ""); err != nil || again != got || abs != absolute {
			t.Fatalf("normalized target is unstable: %q => %q, %v, %v", got, again, abs, err)
		}
	}
}

func TestGenericRedirectTargetRejectsAmbiguousURI(t *testing.T) {
	for _, raw := range []string{
		"", "next", "?q=1", "#fragment", "//other.test", "///other.test", "/\\other.test",
		"javascript:alert(1)", "ftp://example.test/", "https:example.test/", "https:///missing",
		"https://user:secret@example.test/", "https://example.test@other.test/", "https://example.test\\@other.test/",
		"https://example.test..", "https://example.test:", "https://example.test:0", "https://example.test:65536",
		"https://example.test:0443", "https://example.test:+443", "https://example.test:4430000000000",
		"https://bücher.example/", "https://exa%6dple.test/", "https://-example.test/", "https://example_.test/",
		"http://127.1", "http://2130706433", "http://0177.0.0.1", "http://0x7f000001", "http://127.0x0.0.1",
		"http://example.123", "http://example.0x10", "http://0X", "http://127.0.0.01", "http://256.0.0.1",
		"https://[example.test]", "https://[fe80::1%25eth0]", "https://2001:db8::1", "https://[::1",
		"/./new", "/a/../new", "/%2e/new", "/a/%2E%2e", "/a%2fb", "/a%5Cb",
		"/%252f%252fother.test", "/%25252f", "/%252e%252e", "/%255c", "/%250a", "/%253f",
		"/a\n", "/a%0a", "/a%00", "/a%C2%80", "/a?x=%0D", "/a?x=%5c", "/a?x=%ff",
		"/%", "/%x0", "/a?x=%", "/a?x=%0g", "/a space", "/a?x=raw space", "/a?x=[raw]",
		"/a\x7f", "/a\xff", "/a#%0a", "/a#%ff", "/a#bad\\value", "/a#one#two",
	} {
		if got, absolute, err := genericRedirectTarget(raw, ""); err != ErrUnavailable || got != "" || absolute {
			t.Fatalf("ambiguous target accepted: %q => %q, %v, %v", raw, got, absolute, err)
		}
	}
	for _, query := range []string{"x=raw space", "x=%0a", "x=%00", "x=%ff", "x=%", "x=#fragment", "x=\\", "x=[raw]"} {
		if got, absolute, err := genericRedirectTarget("https://example.test/new", query); err != ErrUnavailable || got != "" || absolute {
			t.Fatalf("unsafe forwarded query accepted: %q => %q, %v, %v", query, got, absolute, err)
		}
	}
}

func TestGenericRedirectTargetBoundsIncludeForwardedQueryAndFragment(t *testing.T) {
	limit := "/" + strings.Repeat("x", maxGenericRedirectURI-1)
	if got, _, err := genericRedirectTarget(limit, ""); err != nil || got != limit {
		t.Fatal("exact URI limit rejected", len(got), err)
	}
	for _, test := range []struct{ raw, query string }{
		{limit + "x", ""},
		{limit, "x=1"},
		{limit + "#", ""},
		{"/", strings.Repeat("x", maxGenericRedirectURI)},
		{"/", strings.Repeat("x", maxGenericRedirectURI+1)},
		{"/" + strings.Repeat("é", maxGenericRedirectURI/2), ""},
		{"/" + strings.Repeat("é", maxGenericRedirectURI/3), ""},
		{"/x#" + strings.Repeat("é", maxGenericRedirectURI/3), ""},
		{"/x", "q=" + strings.Repeat("é", maxGenericRedirectURI/3)},
	} {
		if got, absolute, err := genericRedirectTarget(test.raw, test.query); err != ErrUnavailable || got != "" || absolute {
			t.Fatal("URI overflow returned partial target", len(test.raw), len(test.query), len(got), absolute, err)
		}
	}
	query := strings.Repeat("x", maxGenericRedirectURI-3)
	if got, _, err := genericRedirectTarget("/#", query); err != nil || len(got) != maxGenericRedirectURI {
		t.Fatal("exact combined bound rejected", len(got), err)
	}
}

func FuzzGenericRedirectTarget(f *testing.F) {
	for _, seed := range []struct{ raw, query string }{
		{"/", ""}, {"/old?x=1&x=2#", "q=a+b"}, {"/café", "q=雪"},
		{"https://example.test/new", "x=1"}, {"/%252f", ""}, {"http://127.1", ""},
	} {
		f.Add(seed.raw, seed.query)
	}
	f.Fuzz(func(t *testing.T, raw, query string) {
		got, absolute, err := genericRedirectTarget(raw, query)
		if err != nil {
			if got != "" || absolute {
				t.Fatal("partial target returned on failure")
			}
			return
		}
		if len(got) == 0 || len(got) > maxGenericRedirectURI {
			t.Fatal("unbounded successful target")
		}
		for _, c := range got {
			if c <= ' ' || c >= 127 || c == '\\' {
				t.Fatal("non-ASCII or unsafe successful target")
			}
		}
		parsed, err := url.Parse(got)
		if err != nil || parsed.User != nil || parsed.IsAbs() != absolute || absolute && parsed.Scheme != "http" && parsed.Scheme != "https" {
			t.Fatal("successful target has unsafe URL interpretation")
		}
		if again, nextAbsolute, err := genericRedirectTarget(got, ""); err != nil || again != got || nextAbsolute != absolute {
			t.Fatal("normalized target is not stable")
		}
	})
}
