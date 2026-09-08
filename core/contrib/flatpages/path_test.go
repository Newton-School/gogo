package flatpages

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFlatpagePathCanonicalIdentity(t *testing.T) {
	for input, want := range map[string]string{
		"/": "/", "/About": "/About", "/About/": "/About/", "/a//b": "/a//b",
		"/%7eabout/%41": "/~about/A", "/caf%c3%a9": "/caf%C3%A9", "/café": "/caf%C3%A9",
		"/a%20b/%3f%23": "/a%20b/%3F%23", "/%25value": "/%25value", "/a:b@c!d$e&f'g(h)*i+j,k;l=m": "/a:b@c!d$e&f'g(h)*i+j,k;l=m",
	} {
		got, err := normalizePath(input)
		if err != nil || got != want {
			t.Fatalf("%q => %q, %v; want %q", input, got, err, want)
		}
		if again, err := normalizePath(got); err != nil || again != got {
			t.Fatalf("canonical path is unstable: %q => %q, %v", got, again, err)
		}
	}
}

func TestFlatpagePathRejectsStructureAndQuery(t *testing.T) {
	for _, input := range []string{
		"", "about", "https://example.test/about", "//example.test", "///about", "/about?", "/about?x=1", "/about#", "/about#name",
		"/a/./b", "/a/../b", "/%2e/", "/%2E%2E", "/a%2fb", "/a%5Cb", "/a\\b",
		"/%252f", "/%25252f", "/%252e%252e", "/%255c", "/%253f", "/%2523", "/%250d", "/%25C2%2580",
		"/a space", "/a\r", "/a\n", "/a\x00", "/%00", "/%0A", "/%C2%80", "/%ff", "/a\xff", "/[raw]", "/<raw>", "/{raw}", "/%", "/%0g",
	} {
		if got, err := normalizePath(input); err != ErrInvalid || got != "" {
			t.Fatalf("invalid path accepted: %q => %q, %v", input, got, err)
		}
	}
}

func TestFlatpagePathAndDraftBounds(t *testing.T) {
	exact := "/" + strings.Repeat("x", MaxURLBytes-1)
	if got, err := normalizePath(exact); err != nil || got != exact {
		t.Fatal("exact path bound rejected", len(got), err)
	}
	for _, input := range []string{exact + "x", "/" + strings.Repeat("é", MaxURLBytes/3), "/" + strings.Repeat("x", MaxURLBytes)} {
		if got, err := normalizePath(input); err != ErrInvalid || got != "" {
			t.Fatal("raw or expanded path overflow accepted", len(input), len(got), err)
		}
	}
	base := Draft{ID: "domain-validates-this", URL: "/%7eabout", Title: strings.Repeat("雪", MaxTitleRunes), Content: strings.Repeat("x", MaxContentBytes), TemplateName: "flatpages/custom.html"}
	got, err := validateDraft(base)
	if err != nil || got.ID != base.ID || got.Title != base.Title || got.Content != base.Content || got.URL != "/~about" {
		t.Fatal("valid draft lost scalar data or canonicalization", err)
	}
	for _, alter := range []func(*Draft){
		func(d *Draft) { d.Title = "" },
		func(d *Draft) { d.Title += "x" },
		func(d *Draft) { d.Title = "invalid\xff" },
		func(d *Draft) { d.Title = "nul\x00" },
		func(d *Draft) { d.Content += "x" },
		func(d *Draft) { d.Content = "invalid\xff" },
		func(d *Draft) { d.Content = "nul\x00" },
		func(d *Draft) { d.TemplateName = "../secret.html" },
		func(d *Draft) { d.TemplateName = "/secret.html" },
		func(d *Draft) { d.TemplateName = "page.html#partial" },
		func(d *Draft) { d.TemplateName = "page\\secret.html" },
		func(d *Draft) { d.TemplateName = "page\n.html" },
		func(d *Draft) { d.TemplateName = strings.Repeat("x", MaxTemplateBytes+1) },
	} {
		invalid := base
		alter(&invalid)
		if got, err := validateDraft(invalid); err != ErrInvalid || got != (Draft{}) {
			t.Fatal("invalid draft returned partial data", err)
		}
	}
	base.Content, base.TemplateName = "", ""
	if _, err := validateDraft(base); err != nil {
		t.Fatal("empty content/default template rejected", err)
	}
}

func FuzzFlatpagePath(f *testing.F) {
	for _, input := range []string{"/", "/about", "/about/", "/%7eabout", "/café", "/%252f", "/path?query", "/%25C2%2580"} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got, err := normalizePath(input)
		if err != nil {
			if got != "" {
				t.Fatal("partial path on failure")
			}
			return
		}
		if len(got) > MaxURLBytes || !utf8.ValidString(got) || !strings.HasPrefix(got, "/") || strings.ContainsAny(got, "?#\\") {
			t.Fatal("invalid successful canonical path")
		}
		for _, b := range []byte(got) {
			if b >= 128 {
				t.Fatal("canonical path is not ASCII")
			}
		}
		if again, err := normalizePath(got); err != nil || again != got {
			t.Fatal("canonicalization is not idempotent")
		}
	})
}
