package sitemaps_test

import (
	"context"
	"fmt"
	"net/http/httptest"

	"github.com/Newton-School/gogo/core/contrib/sitemaps"
)

// announcementPages represents one immutable public publication. Database-backed
// providers instead bind every reference to a coherent stored revision and
// reject requests for pages that no longer match that revision.
type announcementPages struct{}

func (announcementPages) Manifest(context.Context, int) ([]sitemaps.PageRef, error) {
	return []sitemaps.PageRef{{Key: "launch", Revision: "publication-1"}}, nil
}

func (announcementPages) LoadPage(_ context.Context, ref sitemaps.PageRef, limits sitemaps.Limits) (sitemaps.Page, error) {
	if ref.Key != "launch" || ref.Revision != "publication-1" {
		return sitemaps.Page{}, sitemaps.ErrStale
	}
	if limits.MaxItems < 1 {
		return sitemaps.Page{}, sitemaps.ErrLimit
	}
	return sitemaps.Page{Ref: ref, Entries: []sitemaps.Entry{{Loc: "/news/launch", LastMod: sitemaps.LastModified{Date: "2026-01-01"}}}}, nil
}

func Example_sitemapsRenderer_Handler() {
	renderer, err := sitemaps.New(sitemaps.Config{
		Origin: "https://example.test",
		Policy: sitemaps.Policy{
			Entry: func(_ context.Context, entry sitemaps.Entry) error {
				if entry.Loc != "https://example.test/news/launch" {
					return sitemaps.ErrNotPublic
				}
				return nil
			},
			Index: func(_ context.Context, entry sitemaps.IndexEntry) error {
				if entry.Loc != "https://example.test/sitemap-news.launch.xml" {
					return sitemaps.ErrNotPublic
				}
				return nil
			},
		},
	})
	if err != nil {
		panic(err)
	}
	handler, err := renderer.Handler([]sitemaps.Section{{Name: "news", Provider: announcementPages{}}}, sitemaps.HandlerOptions{
		Authorize: func(_ context.Context, section string) error {
			if section != "news" {
				return sitemaps.ErrNotPublic
			}
			return nil
		},
	})
	if err != nil {
		panic(err)
	}
	for _, path := range []string{"/sitemap.xml", "/sitemap-news.launch.xml"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "https://example.test"+path, nil))
		fmt.Println(path, response.Code, response.Header().Get("ETag") != "")
	}
	// Output:
	// /sitemap.xml 200 true
	// /sitemap-news.launch.xml 200 true
}

func Example_sitemapsRenderer_BuildPages() {
	renderer, err := sitemaps.New(sitemaps.Config{
		Origin: "https://example.test", MaxItems: 2,
		Policy: sitemaps.Policy{Entry: func(_ context.Context, entry sitemaps.Entry) error {
			switch entry.Loc {
			case "https://example.test/public/one", "https://example.test/public/two", "https://example.test/public/three":
				return nil
			}
			return sitemaps.ErrNotPublic
		}},
	})
	if err != nil {
		panic(err)
	}
	documents, err := renderer.BuildPages(context.Background(), []sitemaps.Entry{{Loc: "/public/one"}, {Loc: "/public/two"}, {Loc: "/public/three"}})
	if err != nil {
		panic(err)
	}
	fmt.Println(len(documents))
	// Output: 2
}
