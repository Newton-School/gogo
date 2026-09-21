package integration_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

type adminSortedRecord struct {
	models.Base
	ID, Score    int64
	Tenant, Name string
}

func (*adminSortedRecord) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "SortedRecord", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.CharField("tenant", models.WithStructField("Tenant"), models.ReadOnly),
		models.CharField("name", models.WithStructField("Name"), models.WithColumn("stored_name")),
		models.BigIntegerField("score", models.WithStructField("Score")),
	}}
}

func TestAdminListOrderingUsesScopedSQLAndStablePages(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	schema := (&adminSortedRecord{}).Schema()
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	if err := backend.SchemaEditor().CreateModel(ctx, backend, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, registry)
	records := []*adminSortedRecord{
		{Tenant: "one", Name: "beta", Score: 2},
		{Tenant: "one", Name: "alpha", Score: 2},
		{Tenant: "one", Name: "alpha", Score: 1},
		{Tenant: "two", Name: "private-hidden", Score: 9},
	}
	for _, record := range records {
		if err := store.Save(ctx, record, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{
		"shop.SortedRecord": func() models.Model { return &adminSortedRecord{} },
	}, QueryScope: func(_ context.Context, principal auth.Principal, _ models.Schema) (admin.QueryScope, error) {
		return admin.QueryScope{Predicate: orm.Q("tenant", principal.ID), Identity: principal.ID}, nil
	}, ValidateWrite: func(context.Context, auth.Principal, models.Record) error { return auth.ErrPermissionDenied }})
	if err != nil {
		t.Fatal(err)
	}
	key, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "admin-list-ordering")
	if err != nil {
		t.Fatal(err)
	}
	build := func(allowed []string) *admin.Site {
		t.Helper()
		site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.ModelPolicy{AllowSuperuser: true}})
		if err != nil {
			t.Fatal(err)
		}
		options := admin.ModelAdmin{Schema: schema, Fields: []string{"name", "score"}, ListDisplay: []string{"caption", "score"},
			ListPerPage: 2, SearchFields: []string{"name"}, Ordering: []string{"-score"}, SortableBy: allowed,
			Columns: []admin.DisplayColumn{{Name: "caption", Label: "Caption", Ordering: "-name", Value: func(_ context.Context, object admin.Object) (any, error) {
				id, err := object.Record.Get("id")
				return fmt.Sprintf("Row %v", id), err
			}}}}
		if err := site.Register(options); err != nil {
			t.Fatal(err)
		}
		return site
	}
	perform := func(site *admin.Site, query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/admin/shop/sortedrecord/"+query, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{ID: "one", Authenticated: true, Active: true, Staff: true, Superuser: true}))
		w := httptest.NewRecorder()
		site.ServeHTTP(w, r)
		return w
	}
	rowPattern := regexp.MustCompile(`>Row ([0-9]+)</a>`)
	assertPage := func(site *admin.Site, query string, expected ...int64) string {
		t.Helper()
		page := perform(site, query)
		if page.Code != 200 {
			t.Fatal(query, page.Code, page.Body.String())
		}
		ids := []int64{}
		for _, match := range rowPattern.FindAllStringSubmatch(page.Body.String(), -1) {
			id, err := strconv.ParseInt(match[1], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if !reflect.DeepEqual(ids, expected) || strings.Contains(page.Body.String(), fmt.Sprintf(">Row %d</a>", records[3].ID)) {
			t.Fatal(query, ids, expected, page.Body.String())
		}
		return page.Body.String()
	}
	site := build([]string{"caption"})
	body := assertPage(site, "?o=caption", records[0].ID, records[1].ID)
	header := regexp.MustCompile(`(?s)<th\b[^>]*aria-sort="descending"[^>]*>(.*?)</th>`).FindStringSubmatch(body)
	if len(header) != 2 || !strings.Contains(header[1], `<a href="?o=-caption">Caption</a>`) {
		t.Fatal(body)
	}
	assertPage(site, "?o=caption&p=2", records[2].ID)
	assertPage(site, "?o=-caption", records[1].ID, records[2].ID)
	assertPage(site, "?o=-caption&p=2", records[0].ID)
	assertPage(site, "?o=caption&q=alpha", records[1].ID, records[2].ID)
	for _, query := range []string{"?o=score", "?o=name", "?o=stored_name", "?o=caption&o=-caption", "?o=caption%3BDROP+TABLE"} {
		if page := perform(site, query); page.Code != 400 {
			t.Fatal(query, page.Code, page.Body.String())
		}
	}
	disabled := build([]string{})
	body = assertPage(disabled, "", records[0].ID, records[1].ID)
	if strings.Contains(body, `href="?o=`) {
		t.Fatal("disabled sorting still rendered links", body)
	}
	assertPage(disabled, "?p=2", records[2].ID)
	if page := perform(disabled, "?o=caption"); page.Code != 400 {
		t.Fatal("disabled direct sort accepted", page.Code)
	}
}
