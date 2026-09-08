package admin

import (
	"context"
	"html"
	"net/url"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/templates"
)

func TestSearchHelpTemplateDescribesTheSearchInput(t *testing.T) {
	site, _ := newTestSite(t)
	body, err := site.engine.Render(context.Background(), "list.html", templates.Context{"has_search": true, "search_help_text": "Search product names."})
	if err != nil || !strings.Contains(body, `aria-describedby="search-help"`) || !strings.Contains(body, `<p id="search-help" class="helptext">Search product names.</p>`) {
		t.Fatal("configured search guidance is not attached to the search input", body, err)
	}
}

func newSearchHelpSite(t *testing.T, help string, searchable, editable bool) (*Site, *testDB) {
	t.Helper()
	base, database := newTestSite(t)
	site, err := NewSite(base.config)
	if err != nil {
		t.Fatal(err)
	}
	options := base.models["shop.Product"]
	options.SearchHelpText = help
	options.ListFilter = []string{"Name"}
	options.ListPerPage, options.ListMaxShowAll = 1, 2
	if !searchable {
		options.SearchFields = nil
	}
	if editable {
		options.ListEditable = []string{"Name"}
	}
	if err := site.Register(options); err != nil {
		t.Fatal(err)
	}
	options.SearchHelpText = "caller changed its descriptor"
	if site.models["shop.Product"].SearchHelpText != help {
		t.Fatal("search guidance was not snapshotted")
	}
	return site, database
}

func TestSearchHelpRegistrationBoundsAndPlainText(t *testing.T) {
	base, _ := newTestSite(t)
	for _, help := range []string{"", "Search names.", strings.Repeat("x", 4096), strings.Repeat("é", 2048), "First line.\nSecond line.", `<b>not trusted HTML</b>`} {
		site, err := NewSite(base.config)
		if err != nil {
			t.Fatal(err)
		}
		options := base.models["shop.Product"]
		options.SearchHelpText = help
		if err := site.Register(options); err != nil {
			t.Fatal("valid bounded help rejected", len(help), err)
		}
	}
	for _, help := range []string{strings.Repeat("x", 4097), strings.Repeat("é", 2049), "private\x00help", string([]byte{255})} {
		for _, searchable := range []bool{false, true} {
			site, err := NewSite(base.config)
			if err != nil {
				t.Fatal(err)
			}
			options := base.models["shop.Product"]
			options.SearchHelpText = help
			if !searchable {
				options.SearchFields = nil
			}
			if err := site.Register(options); err == nil || err.Error() != "admin: invalid search help text" || site.IsRegistered(options.Schema.Key()) {
				t.Fatal("invalid help was registered or echoed", searchable, err)
			}
		}
	}
}

func assertSearchHelp(t *testing.T, body, text string, visible bool) {
	t.Helper()
	want := 0
	if visible {
		want = 1
	}
	if strings.Count(body, `id="search-help"`) != want || strings.Count(body, `aria-describedby="search-help"`) != want {
		t.Fatal("missing, duplicate or dangling search description", body)
	}
	if visible && !strings.Contains(body, `<p id="search-help" class="helptext">`+html.EscapeString(text)+`</p>`) {
		t.Fatal("search text not escaped or changed", body)
	}
	if visible {
		start := strings.Index(body, `<input id="search"`)
		if start < 0 || !strings.Contains(body[start:start+strings.Index(body[start:], ">")], `aria-describedby="search-help"`) {
			t.Fatal("description linked to another control", body)
		}
	}
}

func TestSearchHelpVisibilityEscapingAndExistingQuery(t *testing.T) {
	help := `Search names & prefixes; <img src=x onerror="bad()"> is plain text.`
	for _, tc := range []struct {
		help, query string
		searchable  bool
	}{
		{help, "?q=Public&Name=Public&o=-Name&all=", true},
		{help, "?Name=Public", false},
		{"", "?q=Public", true},
	} {
		site, database := newSearchHelpSite(t, tc.help, tc.searchable, false)
		page := perform(site, "GET", "/admin/shop/product/"+tc.query, principal(), nil, nil)
		body := page.Body.String()
		if page.Code != 200 || strings.Contains(body, `<img`) || strings.Contains(body, "Other tenant") || strings.Contains(body, "caller changed its descriptor") {
			t.Fatal("help or row scope escaped its boundary", page.Code, body)
		}
		assertSearchHelp(t, body, tc.help, tc.help != "" && tc.searchable)
		if !tc.searchable && (strings.Contains(body, `id="search"`) || strings.Contains(body, html.EscapeString(tc.help))) {
			t.Fatal("help enabled an otherwise disabled search input", body)
		}
		if tc.searchable && tc.help != "" {
			if database.lastQuery.Search != "Public" || len(database.lastQuery.SearchFields) != 1 || database.lastQuery.SearchFields[0] != "Name" || database.lastQuery.Filters["Name"] != "Public" || database.lastQuery.Ordering[0] != "-Name" || database.lastQuery.Limit != 3 || database.lastQuery.Offset != 0 || !strings.Contains(body, `name="all" value=""`) || !strings.Contains(body, `name="o" value="-Name"`) {
				t.Fatal("guidance changed query or preserved mode", database.lastQuery, body)
			}
		}
	}
}

func TestSearchHelpDoesNotGrantSearchOrExposeDeniedPages(t *testing.T) {
	for _, searchable := range []bool{false, true} {
		site, database := newSearchHelpSite(t, "Visible guidance", searchable, false)
		for _, query := range []string{"?SearchHelpText=forged", "?q=" + url.QueryEscape(strings.Repeat("x", 257))} {
			database.lastQuery = ListQuery{}
			page := perform(site, "GET", "/admin/shop/product/"+query, principal(), nil, nil)
			if page.Code != 400 || database.lastQuery.Limit != 0 {
				t.Fatal("help broadened accepted search query", searchable, query, page.Code)
			}
		}
		if !searchable {
			page := perform(site, "GET", "/admin/shop/product/?q=Public", principal(), nil, nil)
			if page.Code != 400 {
				t.Fatal("help granted search capability", page.Code)
			}
		}
		page := perform(site, "GET", "/admin/shop/product/", auth.Principal{}, nil, nil)
		if page.Code != 401 || strings.Contains(page.Body.String(), "Visible guidance") {
			t.Fatal("unauthorized page exposed guidance", page.Code, page.Body.String())
		}
	}
}

func TestSearchHelpSurvivesScopedListEditValidation(t *testing.T) {
	const help = "Search product names."
	site, database := newSearchHelpSite(t, help, true, true)
	path := "/admin/shop/product/?Name=Public&all=&o=Name&q=Public"
	get := perform(site, "GET", path, principal(), nil, nil)
	if get.Code != 200 {
		t.Fatal(get.Code, get.Body.String())
	}
	data := listTestPost(t, get.Body.String())
	data.Set("form-0-Name", "")
	invalid := perform(site, "POST", path, principal(), data, get.Result().Cookies())
	if invalid.Code != 400 || database.records["1"].Name != "Public record" || len(database.logs) != 0 || !strings.Contains(invalid.Body.String(), "This field is required.") {
		t.Fatal("help changed failed list-edit behavior", invalid.Code, invalid.Body.String())
	}
	assertSearchHelp(t, invalid.Body.String(), help, true)
	if strings.Count(invalid.Body.String(), `name="_list_token"`) != 1 || strings.Count(invalid.Body.String(), `name="csrfmiddlewaretoken"`) != 1 {
		t.Fatal("description repeated management data")
	}
	get = perform(site, "GET", path, principal(), nil, nil)
	data = listTestPost(t, get.Body.String())
	post := perform(site, "POST", path, principal(), data, get.Result().Cookies())
	if post.Code != 303 || post.Header().Get("Location") != path || database.records["1"].Name != "Updated record" || len(database.logs) != 1 {
		t.Fatal("help changed successful list edit", post.Code, post.Header(), database.records, database.logs)
	}
}
