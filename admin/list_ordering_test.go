package admin

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func sortingOptions() ModelAdmin {
	return ModelAdmin{Schema: (&testRecord{}).Schema(), Fields: []string{"Name"}, ListDisplay: []string{"ID", "Name", "label"}, Columns: []DisplayColumn{{Name: "label", Ordering: "-Name", Value: func(_ context.Context, object Object) (any, error) { return object.Record.Get("Name") }}}}
}

func TestListOrderingValidatesSortableDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*ModelAdmin)
	}{
		{"missing", func(o *ModelAdmin) { o.SortableBy = []string{"missing"} }},
		{"not-displayed", func(o *ModelAdmin) { o.SortableBy = []string{"Secret"} }},
		{"duplicate", func(o *ModelAdmin) { o.SortableBy = []string{"Name", "Name"} }},
		{"signed-column", func(o *ModelAdmin) { o.SortableBy = []string{"-Name"} }},
		{"unmapped-column", func(o *ModelAdmin) { o.Columns[0].Ordering = ""; o.SortableBy = []string{"label"} }},
		{"mapped-name", func(o *ModelAdmin) { o.Columns[0].Name = "-label" }},
		{"missing-target", func(o *ModelAdmin) { o.Columns[0].Ordering = "missing" }},
		{"double-sign", func(o *ModelAdmin) { o.Columns[0].Ordering = "--Name" }},
		{"relation-path", func(o *ModelAdmin) { o.Columns[0].Ordering = "parent__Name" }},
		{"sql", func(o *ModelAdmin) { o.Columns[0].Ordering = "Name DESC" }},
		{"column-not-field", func(o *ModelAdmin) { o.Schema.Fields[2].Column = "stored_name"; o.Columns[0].Ordering = "stored_name" }},
		{"structured", func(o *ModelAdmin) { o.Schema.Fields[2].Kind = models.JSON }},
		{"custom", func(o *ModelAdmin) { o.Schema.Fields[2].Kind = models.Custom }},
		{"relation", func(o *ModelAdmin) { o.Schema.Fields[2].Relation = &models.Relation{Target: "shop.Target"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := sortingOptions()
			tc.alter(&options)
			if validateListOrdering(options) == nil {
				t.Fatal("invalid ordering declaration accepted")
			}
		})
	}
	for _, allowed := range [][]string{nil, {}, {"Name"}, {"label", "ID"}} {
		options := sortingOptions()
		options.SortableBy = allowed
		if err := validateListOrdering(options); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListOrderingMapsSignedColumnsAndRetainsDefaultOrdering(t *testing.T) {
	for _, tc := range []struct {
		requested string
		allow     []string
		want      []string
		bad       bool
	}{
		{"Name", nil, []string{"Name", "ID"}, false},
		{"-Name", nil, []string{"-Name", "ID"}, false},
		{"label", nil, []string{"-Name", "ID"}, false},
		{"-label", []string{"label"}, []string{"Name", "ID"}, false},
		{"-ID", nil, []string{"-ID"}, false},
		{"", []string{}, []string{"-Secret", "ID"}, false},
		{"Name", []string{}, nil, true},
		{"Name", []string{"label"}, nil, true},
		{"Secret", nil, nil, true},
		{"--label", nil, nil, true},
		{"Name DESC", nil, nil, true},
	} {
		options := sortingOptions()
		options.SortableBy, options.Ordering = tc.allow, []string{"-Secret"}
		got, err := listOrdering(options, url.Values{"o": {tc.requested}})
		if (err != nil) != tc.bad || !reflect.DeepEqual(got, tc.want) {
			t.Fatal(tc.requested, got, err)
		}
		if options.Ordering[0] != "-Secret" {
			t.Fatal("request mutated configured ordering")
		}
	}
	options := sortingOptions()
	if got, err := listOrdering(options, url.Values{"o": {"Name", "-Name"}}); err == nil || got != nil {
		t.Fatal("ambiguous request accepted", got, err)
	}
	options.Schema.PrimaryKey = []string{"ID", "Tenant"}
	if got, err := listOrdering(options, url.Values{"o": {"-ID"}}); err != nil || !reflect.DeepEqual(got, []string{"-ID", "Tenant"}) {
		t.Fatal("composite ties not preserved", got, err)
	}
}

func TestListSortLinksPreserveQueryAndDescribeMappedDirection(t *testing.T) {
	options := sortingOptions()
	query := url.Values{"q": {"a & b"}, "p": {"2"}, "Name": {"before"}, "o": {"label"}}
	url, direction := listSortLink(options, "label", query, []string{"-Name", "ID"})
	if url != "?Name=before&o=-label&q=a+%26+b" || direction != "descending" || query.Get("p") != "2" || query.Get("o") != "label" {
		t.Fatal(url, direction, query)
	}
	url, direction = listSortLink(options, "label", query, []string{"Name", "ID"})
	if !strings.Contains(url, "o=label") || direction != "ascending" {
		t.Fatal(url, direction)
	}
	options.SortableBy = []string{}
	if url, direction := listSortLink(options, "Name", query, []string{"Name"}); url != "" || direction != "none" {
		t.Fatal("disabled header exposed sorting", url, direction)
	}
}

func TestListOrderingDeclarationCopyAndRequestEnforcement(t *testing.T) {
	base, database := newTestSite(t)
	options := sortingOptions()
	allowed := []string{"label"}
	options.SortableBy = allowed
	site, err := NewSite(base.config)
	if err != nil {
		t.Fatal(err)
	}
	if err = site.Register(options); err != nil {
		t.Fatal(err)
	}
	allowed[0], options.Columns[0].Ordering = "Name", "Secret"
	page := perform(site, "GET", "/admin/shop/product/?o=label", principal(), nil, nil)
	if page.Code != 200 || !reflect.DeepEqual(database.lastQuery.Ordering, []string{"-Name", "ID"}) {
		t.Fatal(page.Code, database.lastQuery)
	}
	if !strings.Contains(page.Body.String(), `o=-label`) || strings.Contains(page.Body.String(), `o=Name`) || strings.Contains(page.Body.String(), `o=ID`) {
		t.Fatal("header allowlist not enforced", page.Body.String())
	}
	for _, raw := range []string{"o=Name", "o=-ID", "o=Secret", "o=label&o=-label", "o=label&o="} {
		database.lastQuery = ListQuery{}
		page := perform(site, "GET", "/admin/shop/product/?"+raw, principal(), nil, nil)
		if page.Code != 400 || database.lastQuery.Limit != 0 {
			t.Fatal("denied sort reached store", raw, page.Code, database.lastQuery)
		}
	}
}

func TestListEditableUsesTheSameSortingPolicy(t *testing.T) {
	site, database := listTestSite(t)
	options := site.models["shop.Product"]
	options.SortableBy = []string{"ID"}
	site.models["shop.Product"] = options
	page := perform(site, "GET", "/admin/shop/product/?o=ID", principal(), nil, nil)
	if page.Code != 200 {
		t.Fatal(page.Code, page.Body.String())
	}
	values := listTestPost(t, page.Body.String())
	post := perform(site, "POST", "/admin/shop/product/?o=Name", principal(), values, page.Result().Cookies())
	if post.Code != 400 || database.records["1"].Name != "Public record" || len(database.logs) != 0 {
		t.Fatal("direct list-edit sorting bypassed policy", post.Code)
	}
	post = perform(site, "POST", "/admin/shop/product/?o=ID", principal(), values, page.Result().Cookies())
	if post.Code != 303 || database.records["1"].Name != "Updated record" {
		t.Fatal("allowed sorted edit failed", post.Code, post.Body.String())
	}
}

func TestListOrderingAnnouncesOnlyTheSelectedDisplayHeader(t *testing.T) {
	for _, tc := range []struct {
		query             string
		allowed           []string
		column, direction string
	}{
		{"?o=label", nil, "label", "descending"},
		{"?o=-label", nil, "label", "ascending"},
		{"?o=Name", nil, "Name", "ascending"},
		{"", nil, "Name", "descending"},
		{"", []string{"label"}, "label", "descending"},
		{"", []string{}, "", ""},
	} {
		base, _ := newTestSite(t)
		site, err := NewSite(base.config)
		if err != nil {
			t.Fatal(err)
		}
		options := sortingOptions()
		options.Ordering, options.SortableBy = []string{"-Name"}, tc.allowed
		if err := site.Register(options); err != nil {
			t.Fatal(err)
		}
		page := perform(site, "GET", "/admin/shop/product/"+tc.query, principal(), nil, nil)
		body := page.Body.String()
		count := strings.Count(body, "aria-sort=")
		wantCount := 1
		if tc.column == "" {
			wantCount = 0
		}
		if page.Code != 200 || count != wantCount {
			t.Fatal(tc, page.Code, body)
		}
		if tc.column != "" && !strings.Contains(body, `aria-sort="`+tc.direction+`"><a href="?o=`) {
			t.Fatal(tc, body)
		}
		query := mustOrderingQuery(t, tc.query)
		ordering, err := listOrdering(options, query)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range options.ListDisplay {
			link, direction := listSortLink(options, name, query, ordering)
			if (direction != "none") != (name == tc.column) {
				t.Fatal(tc, name, direction)
			}
			if name == tc.column && !strings.Contains(body, `aria-sort="`+direction+`"><a href="`+link+`">`+name+`</a>`) {
				t.Fatal("wrong active header identity", tc, body)
			}
		}
	}
}

func mustOrderingQuery(t *testing.T, text string) url.Values {
	t.Helper()
	query, err := url.ParseQuery(strings.TrimPrefix(text, "?"))
	if err != nil {
		t.Fatal(err)
	}
	return query
}

func TestListOrderingRepeatedDisplayNameAnnouncesOnce(t *testing.T) {
	base, _ := newTestSite(t)
	site, err := NewSite(base.config)
	if err != nil {
		t.Fatal(err)
	}
	options := sortingOptions()
	options.ListDisplay = []string{"label", "label", "Name"}
	if err := site.Register(options); err != nil {
		t.Fatal(err)
	}
	page := perform(site, "GET", "/admin/shop/product/?o=label", principal(), nil, nil)
	if page.Code != 200 || strings.Count(page.Body.String(), "aria-sort=") != 1 {
		t.Fatal("repeated display name repeated sort announcement", page.Code, page.Body.String())
	}
}
