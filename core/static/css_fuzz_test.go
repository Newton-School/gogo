package static

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// Keep this tokenizer check independent of the collector's filesystem tests:
// a successful identity rewrite must preserve every byte, and replacing all
// recognized references must produce CSS whose references parse identically.
func FuzzCSSReferenceRoundTrip(f *testing.F) {
	for _, seed := range []string{
		`body { background: url(../img/logo.svg?v=1#icon) }`,
		`@import "theme.css" layer(base) screen;`,
		`@import url('theme.css'); a { mask: URL("x.svg") }`,
		`a { background: u\72l(fo\6f.png) }`,
		`@\69mport /* keep */ "theme.css";`,
		`a { content: "url(ignore.png)" } /* url(ignore.css) */`,
		`a { background: url(data:image/png;base64,YQ==) }`,
		`a { background: image-set("x.png" 1x) }`,
		"/*# sourceMappingURL=app.css.map */",
		"a { background: url(\"a\\\nb.png\") }",
		"a { background: url(\"unterminated) }",
		"\x00\xff",
		"",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, source []byte) {
		if len(source) > 4096 {
			t.Skip()
		}
		const maximum int64 = 16 << 10
		ctx := context.Background()
		original := bytes.Clone(source)
		identity, err := rewriteCSS(ctx, source, maximum, func(s string) (string, error) { return s, nil })
		if !bytes.Equal(source, original) {
			t.Fatal("rewriter mutated source bytes")
		}
		if err != nil {
			if identity != nil {
				t.Fatal("rejected CSS returned partial output")
			}
			return
		}
		if !bytes.Equal(identity, source) {
			t.Fatal("identity rewrite changed source")
		}
		const replacement = `images/0123456789.svg?label="quoted"&v=2#icon`
		calls := 0
		output, err := rewriteCSS(ctx, source, maximum, func(string) (string, error) {
			calls++
			return replacement, nil
		})
		if err != nil {
			if !errors.Is(err, ErrLimit) || output != nil {
				t.Fatalf("valid source failed non-limit rewrite: %v", err)
			}
			return
		}
		if int64(len(output)) > maximum {
			t.Fatal("rewrite exceeded output bound")
		}
		reparsed := 0
		again, err := rewriteCSS(ctx, output, maximum, func(value string) (string, error) {
			reparsed++
			if value != replacement {
				t.Fatalf("rewritten reference changed after parsing: %q", value)
			}
			return value, nil
		})
		if err != nil || !bytes.Equal(again, output) || reparsed != calls {
			t.Fatalf("rewritten CSS did not round trip: references %d/%d, error %v", reparsed, calls, err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		denied, err := rewriteCSS(canceled, source, maximum, func(string) (string, error) {
			t.Fatal("canceled rewrite invoked callback")
			return "", nil
		})
		if !errors.Is(err, context.Canceled) || denied != nil {
			t.Fatalf("canceled rewrite returned output: %v", err)
		}
	})
}
