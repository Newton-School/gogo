package sitemaps

import (
	"context"
	"errors"
	"testing"
)

type replacementContext struct {
	context.Context
	once func()
}

func (c *replacementContext) Err() error {
	if c.once != nil {
		callback := c.once
		c.once = nil
		callback()
	}
	return c.Context.Err()
}

func TestSitemapRendererCapturedBeforeContextCallbacks(t *testing.T) {
	for _, operation := range []string{"page", "index", "build"} {
		t.Run(operation, func(t *testing.T) {
			config := sitemapRenderConfig()
			config.Policy.Entry = func(context.Context, Entry) error { return ErrNotPublic }
			config.Policy.Index = func(context.Context, IndexEntry) error { return ErrNotPublic }
			renderer := newSitemapRenderTest(t, config)
			replacement := newSitemapRenderTest(t, sitemapRenderConfig())
			ctx := &replacementContext{Context: context.Background(), once: func() { *renderer = *replacement }}
			switch operation {
			case "page":
				document, err := renderer.RenderPage(ctx, []Entry{{Loc: "/article"}})
				requireSitemapError(t, document, err, ErrNotPublic)
			case "index":
				document, err := renderer.RenderIndex(ctx, []IndexEntry{{Loc: "/page.xml"}})
				requireSitemapError(t, document, err, ErrNotPublic)
			case "build":
				documents, err := renderer.BuildPages(ctx, []Entry{{Loc: "/article"}})
				if !errors.Is(err, ErrNotPublic) || documents != nil {
					t.Fatalf("context callback replaced entry-time policy: documents=%d err=%v", len(documents), err)
				}
			}
			// The replacement is intentionally visible to the next operation.
			if _, err := renderer.RenderPage(context.Background(), []Entry{{Loc: "/next"}}); err != nil {
				t.Fatal("subsequent call did not observe replacement", err)
			}
		})
	}
}
