package static

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticCSSTokenizerPreservesNonReferencesAndDecodesEscapes(t *testing.T) {
	input := `/* url(fake.png) */ a{content:"url(fake.png)";background:URL( 'image.png?q=1#x' );mask:u\72l(im\61ge.svg)} @\69mport /* comment */ 'base.css' layer(main) supports(display:grid) screen;`
	var found []string
	output, err := rewriteCSS(context.Background(), []byte(input), 4096, func(value string) (string, error) {
		found = append(found, value)
		return "hashed/" + value, nil
	})
	if err != nil || !reflect.DeepEqual(found, []string{"image.png?q=1#x", "image.svg", "base.css"}) {
		t.Fatal(found, err)
	}
	for _, preserved := range []string{`/* url(fake.png) */`, `content:"url(fake.png)"`, `layer(main) supports(display:grid) screen;`, `@\69mport /* comment */`} {
		if !strings.Contains(string(output), preserved) {
			t.Fatalf("lost %q in %s", preserved, output)
		}
	}
	for _, input := range []string{`x{a:url(a\)b.png)}`, "@import 'a\\\nb.css';", `x{a:url(a\20 b.png)}`} {
		var seen string
		out, err := rewriteCSS(context.Background(), []byte(input), 4096, func(value string) (string, error) { seen = value; return value, nil })
		if err != nil || seen == "" || string(out) != input {
			t.Fatal(input, seen, err)
		}
	}
}

func TestStaticCSSReferenceClassificationAndConfiguredCDN(t *testing.T) {
	c, err := New(Config{Sources: []Source{{Owner: "project", FS: fstest.MapFS{}}}, BaseURL: "https://cdn.example.test/static/"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		input, name, suffix string
		local, invalid      bool
	}{
		{"../img/a.svg?x=1&y=2#icon", "img/a.svg", "?x=1&y=2#icon", true, false},
		{"/static/img/a.svg?", "img/a.svg", "?", true, false},
		{"https://cdn.example.test/static/img/a.svg#x", "img/a.svg", "#x", true, false},
		{"https://CDN.EXAMPLE.TEST/static/img/a.svg", "img/a.svg", "", true, false},
		{"https://other.example.test/static/img/a.svg", "", "", false, false},
		{"http://cdn.example.test/static/img/a.svg", "", "", false, false},
		{"https://cdn.example.test/login", "", "", false, false},
		{"https://cdn.example.test:443/static/img/a.svg", "", "", false, false},
		{"//cdn.example.test/static/img/a.svg", "", "", false, false},
		{"data:image/png;base64,YQ==", "", "", false, false},
		{"#icon", "", "", false, false},
		{"/login", "", "", false, false},
		{"?revision=1", "css/app.css", "?revision=1", true, false},
		{"../../outside.png", "", "", false, true},
		{"../%2e%2e/private.png", "", "", false, true},
		{"../.env", "", "", false, true},
		{"javascript:alert(1)", "", "", false, true},
	} {
		t.Run(test.input, func(t *testing.T) {
			name, suffix, local, err := c.cssReference("css/app.css", test.input)
			if (err != nil) != test.invalid || name != test.name || suffix != test.suffix || local != test.local {
				t.Fatal(name, suffix, local, err)
			}
		})
	}
	files := fstest.MapFS{"css/app.css": {Data: []byte(`a{background:url(https://cdn.example.test/static/img/a.svg?v=1#i)}`)}, "img/a.svg": {Data: []byte("image")}}
	c.config.Sources[0].FS = files
	set, err := c.prepare(context.Background(), map[string][]byte{"css/app.css": files["css/app.css"].Data, "img/a.svg": files["img/a.svg"].Data})
	if err != nil {
		t.Fatal(err)
	}
	asset := set.manifest.assets["css/app.css"]
	if !strings.Contains(string(set.bytes[asset.Versioned]), "../img/"+digest([]byte("image"))+".svg?v=1#i") {
		t.Fatal("configured CDN reference was not versioned", string(set.bytes[asset.Versioned]))
	}
}

func TestStaticCSSFailuresReturnNoPartialOutput(t *testing.T) {
	for _, input := range []string{`url(a`, `url('a)`, `url(a b)`, `url(a(b))`, `@import variable;`, `u\0rl(a)`, `url(a\)`, `/* unterminated`, `image-set("a" 1x)`, `-webkit-image-set(url(a) 1x)`, `/*# sourceMappingURL=a.map */`, "a\x00b", "\xff"} {
		t.Run(input, func(t *testing.T) {
			out, err := rewriteCSS(context.Background(), []byte(input), 4096, func(s string) (string, error) { return s, nil })
			if out != nil || !errors.Is(err, ErrDependency) {
				t.Fatal(string(out), err)
			}
		})
	}
	out, err := rewriteCSS(context.Background(), []byte("url(a)"), 16, func(string) (string, error) { return strings.Repeat("x", 20), nil })
	if out != nil || err != ErrLimit {
		t.Fatal(out, err)
	}
	failure := errors.New("failure")
	out, err = rewriteCSS(context.Background(), []byte("url(a)"), 128, func(string) (string, error) { return "", failure })
	if out != nil || err != failure {
		t.Fatal(out, err)
	}
}
