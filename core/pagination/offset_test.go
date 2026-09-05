package pagination

import (
	"net/url"
	"testing"
)

func TestBoundedPaginationAndRelativeLinks(t *testing.T) {
	for _, mode := range []Mode{PageNumber, LimitOffset} {
		p, err := New(Config{Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		page, err := p.Parse(url.Values{})
		if err != nil || page != (Page{Size: 50}) {
			t.Fatal(page, err)
		}
		bad := []url.Values{{"cursor": {"x"}}, {"page": {"1"}, "offset": {"0"}}, {"page_size": {"201"}}, {"limit": {"201"}}, {"page": {"9999999999999999999999999"}}, {"offset": {"1000001"}}, {"page": {"0"}}, {"limit": {"0"}}, {"offset": {"-1"}}, {"page": {"1", "2"}}, {"offset": {"+1"}}}
		for _, values := range bad {
			if _, err := p.Parse(values); err == nil {
				t.Fatalf("accepted %v in %s", values, mode)
			}
		}
		values := url.Values{"search": {"a&b"}}
		if mode == PageNumber {
			values.Set("page", "2")
			values.Set("page_size", "3")
		} else {
			values.Set("offset", "3")
			values.Set("limit", "3")
		}
		page, err = p.Parse(values)
		if err != nil || page != (Page{Size: 3, Offset: 3}) {
			t.Fatal(page, err)
		}
		before := values.Encode()
		next, previous := p.Links(values, page, true)
		for _, link := range []string{next, previous} {
			parsed, err := url.Parse(link)
			if err != nil || parsed.Host != "" || parsed.Query().Get("search") != "a&b" {
				t.Fatal(link, err)
			}
		}
		if values.Encode() != before || next == "" || previous == "" {
			t.Fatal("navigation mutated input or lost links")
		}
		page, err = p.Parse(url.Values{})
		next, previous = p.Links(values, page, false)
		if err != nil || next != "" || previous != "" {
			t.Fatal("empty first page navigation")
		}
	}
}

func TestPaginationConfigurationBounds(t *testing.T) {
	for _, config := range []Config{{Mode: "other"}, {MaxSize: 201}, {DefaultSize: 201}, {DefaultSize: -1}, {MaxOffset: -1}, {MaxOffset: int(^uint(0) >> 1)}} {
		if _, err := New(config); err == nil {
			t.Fatal("invalid configuration accepted", config)
		}
	}
}

func TestCursorPageSizeParsingDoesNotEnableOffsetNavigation(t *testing.T) {
	p, err := New(Config{Mode: CursorMode})
	if err != nil {
		t.Fatal(err)
	}
	page, err := p.Parse(url.Values{"cursor": {"opaque-token"}, "page_size": {"7"}})
	if err != nil || page.Size != 7 || page.Offset != 0 {
		t.Fatal(page, err)
	}
	for _, values := range []url.Values{{"page": {"1"}}, {"offset": {"0"}}, {"limit": {"2"}}, {"cursor": {""}}, {"cursor": {"one", "two"}}, {"page_size": {"201"}}} {
		if _, err := p.Parse(values); err != ErrInvalid {
			t.Fatal("invalid cursor pagination accepted", values, err)
		}
	}
	if next, previous := p.Links(url.Values{}, page, true); next != "" || previous != "" {
		t.Fatal("unsigned cursor navigation emitted")
	}
}
