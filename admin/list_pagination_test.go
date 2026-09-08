package admin

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
)

func TestListMaxShowAllUsesTheScopedResultLimit(t *testing.T) {
	site, database := newTestSite(t)
	options := site.models["shop.Product"]
	options.ListPerPage, options.ListMaxShowAll = 1, 2
	site.models["shop.Product"] = options
	database.records["3"] = testRecord{ID: 3, Tenant: "one", Name: "Second public", Secret: "private field"}
	page := perform(site, "GET", "/admin/shop/product/?all=", principal(), nil, nil)
	if page.Code != 200 || database.lastQuery.Limit != 3 || database.lastQuery.Offset != 0 || !strings.Contains(page.Body.String(), "Public record") || !strings.Contains(page.Body.String(), "Second public") || strings.Contains(page.Body.String(), "Other tenant") {
		t.Fatal("show-all did not use the bounded scoped result", page.Code, database.lastQuery, page.Body.String())
	}
}

func TestListMaxShowAllModeAndNavigation(t *testing.T) {
	options := ModelAdmin{ListPerPage: 2, ListMaxShowAll: 4}
	for _, test := range []struct {
		query string
		want  listPage
	}{
		{"", listPage{number: 1, limit: 2}},
		{"p=3", listPage{number: 3, limit: 2, offset: 4}},
		{"all=", listPage{number: 1, limit: 5, all: true}},
	} {
		values, _ := url.ParseQuery(test.query)
		actual, err := listPagination(options, values)
		if err != nil || actual != test.want {
			t.Fatal(test.query, actual, err)
		}
	}
	for _, raw := range []string{"all=1", "all=true", "all=&all=", "all=&p=1", "all=&p=", "p=2&p=3", "p=-1", "p=1000001"} {
		values, _ := url.ParseQuery(raw)
		if _, err := listPagination(options, values); err == nil {
			t.Fatal("ambiguous mode accepted", raw)
		}
	}
	query := url.Values{"p": {"3"}, "q": {"a & b"}, "Name": {"<match>"}, "o": {"-Name"}}
	before := query.Encode()
	if got := listModeURL(query, true); got != "?Name=%3Cmatch%3E&all=&o=-Name&q=a+%26+b" {
		t.Fatal(got)
	}
	query.Set("all", "")
	if got := listModeURL(query, false); got != "?Name=%3Cmatch%3E&o=-Name&q=a+%26+b" {
		t.Fatal(got)
	}
	query.Del("all")
	if query.Encode() != before {
		t.Fatal("navigation mutated caller query")
	}
}

func TestListMaxShowAllRegistrationReservesOnlyThePaginationFilter(t *testing.T) {
	base, _ := newTestSite(t)
	site, err := NewSite(base.config)
	if err != nil {
		t.Fatal(err)
	}
	options := base.models["shop.Product"]
	options.Schema.Fields = append(options.Schema.Fields, models.CharField("all"))
	options.ListFilter = []string{"all"}
	if err := site.Register(options); err == nil {
		t.Fatal("show-all parameter also registered as a filter")
	}
	options.ListFilter = nil
	options.ListPerPage, options.ListMaxShowAll = 1, 2
	if err := site.Register(options); err != nil {
		t.Fatal(err)
	}
	options.ListPerPage, options.ListMaxShowAll = 20, 40
	if registered := site.models["shop.Product"]; registered.ListPerPage != 1 || registered.ListMaxShowAll != 2 {
		t.Fatal("registration limits not retained", registered.ListPerPage, registered.ListMaxShowAll)
	}
}

func TestListMaxShowAllLinksAndQueryValidation(t *testing.T) {
	site, database := newTestSite(t)
	options := site.models["shop.Product"]
	options.ListPerPage, options.ListMaxShowAll = 1, 2
	options.ListFilter = []string{"Name"}
	site.models["shop.Product"] = options
	database.records["3"] = testRecord{ID: 3, Tenant: "one", Name: "Public second"}
	page := perform(site, "GET", "/admin/shop/product/?p=2&q=Public&o=Name&Name=Public", principal(), nil, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `href="?Name=Public&amp;all=&amp;o=Name&amp;q=Public">Show all</a>`) {
		t.Fatal(page.Code, page.Body.String())
	}
	page = perform(site, "GET", "/admin/shop/product/?all=&q=Public&o=Name&Name=Public", principal(), nil, nil)
	body := page.Body.String()
	if page.Code != 200 || !strings.Contains(body, `href="?Name=Public&amp;o=Name&amp;q=Public">Return to pagination</a>`) || !strings.Contains(body, `name="all" value=""`) || strings.Contains(body, "Next →") || strings.Contains(body, "← Previous") || !strings.Contains(body, "All matching records") {
		t.Fatal(page.Code, body)
	}
	for _, raw := range []string{"all=1", "all=&all=", "all=&p=1", "all=&unknown=x", "all=&Name=one&Name=two", "all=&Name=one;hidden=1", "all=%ZZ"} {
		database.lastQuery = ListQuery{}
		page := perform(site, "GET", "/admin/shop/product/?"+raw, principal(), nil, nil)
		if page.Code != 400 || database.lastQuery.Limit != 0 {
			t.Fatal("invalid mode reached list provider", raw, page.Code)
		}
	}
	for _, count := range []int{0, 1, 3} {
		database.records = map[string]testRecord{}
		for i := 1; i <= count; i++ {
			database.records[strconv.Itoa(i)] = testRecord{ID: int64(i), Tenant: "one", Name: "Public"}
		}
		page := perform(site, "GET", "/admin/shop/product/", principal(), nil, nil)
		if page.Code != 200 || strings.Contains(page.Body.String(), ">Show all</a>") {
			t.Fatal("ineligible navigation shown", count, page.Code)
		}
		if count == 0 {
			page = perform(site, "GET", "/admin/shop/product/?all=", principal(), nil, nil)
			if page.Code != 200 || !strings.Contains(page.Body.String(), "No matching records") {
				t.Fatal(page.Code, page.Body.String())
			}
		}
	}
}

type showAllStore struct {
	Store
	page Page
	err  error
}

func (s showAllStore) Scope(ctx context.Context, p auth.Principal, site string, schema models.Schema) (ScopedStore, error) {
	base, err := s.Store.Scope(ctx, p, site, schema)
	return showAllScope{ScopedStore: base, page: s.page, err: s.err}, err
}

type showAllScope struct {
	ScopedStore
	page Page
	err  error
}

func (s showAllScope) List(context.Context, ListQuery) (Page, error) { return s.page, s.err }

func TestListMaxShowAllRefusesOverflowInconsistentAndPartialPages(t *testing.T) {
	for _, kind := range []string{"count-overflow", "row-overflow", "count-high", "count-low", "negative", "duplicate", "empty-id", "nil-record", "provider"} {
		t.Run(kind, func(t *testing.T) {
			site, database := newTestSite(t)
			options := site.models["shop.Product"]
			options.ListPerPage, options.ListMaxShowAll = 1, 2
			calls := 0
			options.Columns = []DisplayColumn{{Name: "Name", Value: func(context.Context, Object) (any, error) { calls++; return "must not render", nil }}}
			site.models["shop.Product"] = options
			object := (&testScope{db: database}).object(database.records["1"])
			page := Page{Objects: []Object{object}, Count: 1}
			var failure error
			switch kind {
			case "count-overflow":
				page.Count = 3
			case "row-overflow":
				page.Objects = []Object{object, object, object}
			case "count-high":
				page.Count = 2
			case "count-low":
				page.Count = 0
			case "negative":
				page.Count = -1
			case "duplicate":
				page.Objects = []Object{object, object}
				page.Count = 2
			case "empty-id":
				page.Objects[0].ID = ""
			case "nil-record":
				page.Objects[0].Record = nil
			case "provider":
				failure = errors.New("synthetic private failure")
			}
			site.config.Store = showAllStore{Store: database, page: page, err: failure}
			response := perform(site, "GET", "/admin/shop/product/?all=", principal(), nil, nil)
			if response.Code < 400 || calls != 0 || strings.Contains(response.Body.String(), "Public record") || strings.Contains(response.Body.String(), "private") {
				t.Fatal(kind, response.Code, response.Body.String(), calls)
			}
		})
	}
}

func TestListMaxShowAllEditableManifestCannotChangeModesOrSize(t *testing.T) {
	site, database := listTestSite(t)
	options := site.models["shop.Product"]
	options.ListPerPage, options.ListMaxShowAll = 1, 2
	site.models["shop.Product"] = options
	database.records["3"] = testRecord{ID: 3, Tenant: "one", Name: "Second public"}
	scope := &testScope{db: database, tenant: "one"}
	rows := []Object{scope.object(database.records["1"]), scope.object(database.records["3"])}
	all := url.Values{"all": {""}}
	if _, err := site.listToken(principal(), options, url.Values{}, rows); err == nil {
		t.Fatal("normal page widened")
	}
	token, err := site.listToken(principal(), options, all, rows)
	if err != nil {
		t.Fatal(err)
	}
	posted := url.Values{"_list_token": {token}, "_save_list": {"1"}, "form-TOTAL_FORMS": {"2"}, "form-INITIAL_FORMS": {"2"}, "form-0-_id": {"1"}, "form-1-_id": {"3"}}
	if _, err := site.checkListToken(context.Background(), principal(), options, all, posted, rows); err != nil {
		t.Fatal(err)
	}
	for _, query := range []url.Values{{}, {"all": {"1"}}, {"all": {""}, "p": {"1"}}} {
		if _, err := site.checkListToken(context.Background(), principal(), options, query, posted, rows); err == nil {
			t.Fatal("manifest mode changed", query)
		}
	}
	if _, err := site.listToken(principal(), options, all, append(rows, rows[0])); err == nil {
		t.Fatal("show-all cap widened")
	}
	if _, err := site.checkListToken(context.Background(), principal(), options, all, posted, append(rows, rows[0])); err == nil {
		t.Fatal("POST cap widened")
	}
	page := perform(site, "GET", "/admin/shop/product/?all=", principal(), nil, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `name="form-TOTAL_FORMS" value="2"`) {
		t.Fatal(page.Code, page.Body.String())
	}
	if limit, err := listEditableLimit(options, all); err != nil || limit != 2 {
		t.Fatal(limit, err)
	}
	if !reflect.DeepEqual(all, url.Values{"all": {""}}) {
		t.Fatal("mode mutated")
	}
}
