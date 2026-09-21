package integration_test

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

func TestAdminListMaxShowAllPostgresScopedNavigationAndEdits(t *testing.T) {
	for _, mode := range []string{"save", "changed-query", "invalid", "grew-past-cap", "shrunk"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			backend := testservice.Postgres(t)
			schema := (&adminSortedRecord{}).Schema()
			for _, item := range []models.Schema{schema, admin.LogSchema()} {
				if err := backend.SchemaEditor().CreateModel(ctx, backend, item); err != nil {
					t.Fatal(err)
				}
			}
			store := orm.New(backend, nil)
			rows := []*adminSortedRecord{{Tenant: "one", Name: "alpha first", Score: 1}, {Tenant: "one", Name: "alpha second", Score: 1}, {Tenant: "one", Name: "beta", Score: 2}, {Tenant: "two", Name: "alpha private", Score: 1}}
			for _, row := range rows {
				if err := store.Save(ctx, row, orm.SaveOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{schema.Key(): func() models.Model { return &adminSortedRecord{} }}, QueryScope: func(_ context.Context, p auth.Principal, _ models.Schema) (admin.QueryScope, error) {
				return admin.QueryScope{Predicate: orm.Q("tenant", p.ID), Identity: p.ID}, nil
			}, ValidateWrite: func(_ context.Context, p auth.Principal, r models.Record) error {
				value, err := r.Get("tenant")
				if err != nil || value != p.ID {
					return auth.ErrPermissionDenied
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			key, err := security.RandomToken(32)
			if err != nil {
				t.Fatal(err)
			}
			signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "show-all")
			if err != nil {
				t.Fatal(err)
			}
			site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.ModelPolicy{AllowSuperuser: true}})
			if err != nil {
				t.Fatal(err)
			}
			if err := site.Register(admin.ModelAdmin{Schema: schema, Fields: []string{"name"}, ListDisplay: []string{"id", "name", "score"}, ListEditable: []string{"name"}, SearchFields: []string{"name"}, ListFilter: []string{"score"}, ListPerPage: 1, ListMaxShowAll: 2}); err != nil {
				t.Fatal(err)
			}
			actor := auth.Principal{ID: "one", Authenticated: true, Active: true, Staff: true, Superuser: true}
			request := func(method, query string, values url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, "http://example.test/admin/shop/sortedrecord/"+query, strings.NewReader(values.Encode()))
				r = r.WithContext(auth.WithPrincipal(ctx, actor))
				if method == "POST" {
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.Header.Set("Origin", "http://example.test")
				}
				for _, cookie := range cookies {
					r.AddCookie(cookie)
				}
				w := httptest.NewRecorder()
				site.ServeHTTP(w, r)
				return w
			}
			page := request("GET", "", nil, nil)
			if page.Code != 200 || strings.Contains(page.Body.String(), ">Show all</a>") || !strings.Contains(page.Body.String(), "<span>3 records</span>") {
				t.Fatal(page.Code, page.Body.String())
			}
			if page := request("GET", "?all=", nil, nil); page.Code != 400 || strings.Contains(page.Body.String(), "alpha") {
				t.Fatal("over-cap request was rendered", page.Code, page.Body.String())
			}
			page = request("GET", "?q=alpha&o=score&score=1", nil, nil)
			if page.Code != 200 || !strings.Contains(page.Body.String(), `href="?all=&amp;o=score&amp;q=alpha&amp;score=1">Show all</a>`) {
				t.Fatal(page.Code, page.Body.String())
			}
			query := "?all=&o=score&q=alpha&score=1"
			page = request("GET", query, nil, nil)
			body := page.Body.String()
			if page.Code != 200 || strings.Contains(body, "alpha private") || !strings.Contains(body, "<span>2 records</span>") || !strings.Contains(body, `href="?o=score&amp;q=alpha&amp;score=1">Return to pagination</a>`) || !strings.Contains(body, `name="form-TOTAL_FORMS" value="2"`) {
				t.Fatal(page.Code, body)
			}
			hidden := func(name string) string {
				t.Helper()
				match := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(body)
				if len(match) != 2 {
					t.Fatal("missing management field", name)
				}
				return html.UnescapeString(match[1])
			}
			values := url.Values{"csrfmiddlewaretoken": {hidden("csrfmiddlewaretoken")}, "_list_token": {hidden("_list_token")}, "_save_list": {"1"}, "form-TOTAL_FORMS": {"2"}, "form-INITIAL_FORMS": {"2"}}
			for i := 0; i < 2; i++ {
				prefix := "form-" + strconv.Itoa(i)
				values.Set(prefix+"-_id", hidden(prefix+"-_id"))
				values.Set(prefix+"-name", "Updated alpha "+strconv.Itoa(i))
			}
			switch mode {
			case "changed-query":
				query = "?o=score&q=alpha&score=1"
			case "invalid":
				values.Set("form-1-name", "")
			case "grew-past-cap":
				if err := store.Save(ctx, &adminSortedRecord{Tenant: "one", Name: "alpha late", Score: 1}, orm.SaveOptions{}); err != nil {
					t.Fatal(err)
				}
			case "shrunk":
				if _, err := backend.Exec(ctx, "DELETE FROM "+schema.DBTable()+" WHERE id=$1", rows[1].ID); err != nil {
					t.Fatal(err)
				}
			}
			post := request("POST", query, values, page.Result().Cookies())
			wantStatus, wantOutcome, wantAudits := 400, "unchanged", int64(0)
			if mode == "save" {
				wantStatus, wantOutcome, wantAudits = 303, "changed", 2
			}
			if mode == "shrunk" || mode == "changed-query" {
				wantStatus = 409
			}
			if post.Code != wantStatus || post.Header().Get("X-Gogo-List-Change") != wantOutcome {
				t.Fatal(mode, post.Code, post.Body.String(), post.Header())
			}
			if mode == "save" && post.Header().Get("Location") != "/admin/shop/sortedrecord/"+query {
				t.Fatal("show-all query not preserved after edit", post.Header())
			}
			var audits int64
			if err := db.QueryRow(ctx, backend, "SELECT count(*) FROM "+admin.LogSchema().DBTable(), nil, &audits); err != nil {
				t.Fatal(err)
			}
			if audits != wantAudits {
				t.Fatal("partial/unexpected audit", audits, wantAudits)
			}
			first, err := orm.For(store, func() *adminSortedRecord { return &adminSortedRecord{} }).Filter(orm.Q("id", rows[0].ID)).Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantName := "alpha first"
			if mode == "save" {
				wantName = "Updated alpha 0"
			}
			if first.Name != wantName {
				t.Fatal("partial list edit persisted", first.Name)
			}
		})
	}
}
