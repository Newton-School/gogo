package syndication_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"time"

	"github.com/Newton-School/gogo/core/contrib/syndication"
	"github.com/Newton-School/gogo/core/urls"
)

func ExampleRenderer_Handler() {
	// This example publishes one fixed, public announcement. A database-backed
	// application must apply publication/scope filters before projecting items,
	// then recheck its trusted publication authority in Policy.Item.
	renderer, err := syndication.New(syndication.Config{
		Origin: "https://example.test", Format: syndication.Atom1{},
		Policy: syndication.Policy{Item: func(_ context.Context, item syndication.Item) error {
			if item.ID != "urn:announcement:launch" {
				return syndication.ErrNotPublic
			}
			return nil
		}},
	})
	if err != nil {
		panic(err)
	}
	handler, err := renderer.Handler(func(_ context.Context, limit int) (syndication.Feed, error) {
		updated := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
		items := []syndication.Item{{ID: "urn:announcement:launch", Title: "Launch", Link: "/news/launch", Updated: updated}}
		if len(items) > limit {
			return syndication.Feed{}, syndication.ErrLimit
		}
		return syndication.Feed{
			Title: "Public news", Link: "/news", FeedURL: "/news/feed.xml",
			Author: &syndication.Author{Name: "News desk"}, Updated: updated, Items: items,
		}, nil
	}, syndication.HandlerOptions{
		// This feed's metadata is deliberately public; no session is required.
		Authorize: func(context.Context) error { return nil },
	})
	if err != nil {
		panic(err)
	}
	router, err := urls.New(urls.Include("/news", "news", urls.Path("/feed.xml", handler, "feed")))
	if err != nil {
		panic(err)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "https://example.test/news/feed.xml", nil))
	fmt.Println(response.Code)
	fmt.Println(response.Header().Get("Content-Type"))
	fmt.Println(response.Header().Get("Cache-Control"))
	// Output:
	// 200
	// application/atom+xml; charset=utf-8
	// private, no-cache
}
