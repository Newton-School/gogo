package integration_test

import (
	"context"
	"errors"
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

func TestAdminListEditablePostgresBatchFencesAndOutcomes(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx := context.Background()
	for _, schema := range []models.Schema{(&adminProduct{}).Schema(), admin.LogSchema()} {
		if err := backend.SchemaEditor().CreateModel(ctx, backend, schema); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"success", "unchanged", "validation", "second audit failure", "later saved sibling", "untouched sibling", "final policy sibling", "later scope", "proposed sibling", "late readonly", "unknown commit", "committed callback", "foreign committed", "foreign unknown", "precommit panic", "nested transaction"} {
		t.Run(mode, func(t *testing.T) {
			commitBackend := &adminCredentialCommitBackend{Backend: backend}
			store := orm.New(commitBackend, nil)
			p := auth.Principal{ID: mode, Authenticated: true, Active: true, Staff: true, Superuser: true}
			products := []*adminProduct{{Tenant: mode, Name: "First", Secret: "server first"}, {Tenant: mode, Name: "Second", Secret: "server second"}, {Tenant: mode, Name: "Untouched", Secret: "server untouched"}, {Tenant: mode + " hidden", Name: "Hidden", Secret: "server hidden"}}
			for _, product := range products {
				if err := store.Save(ctx, product, orm.SaveOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			var excluded int64
			adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{"shop.Product": func() models.Model { return &adminProduct{} }}, QueryScope: func(_ context.Context, p auth.Principal, _ models.Schema) (admin.QueryScope, error) {
				predicate := orm.Q("tenant", p.ID)
				if excluded != 0 {
					predicate = orm.And(predicate, orm.Not(orm.Q("id", excluded)))
				}
				return admin.QueryScope{Predicate: predicate, Identity: p.ID}, nil
			}, ValidateWrite: func(_ context.Context, p auth.Principal, r models.Record) error {
				tenant, err := r.Get("tenant")
				if err != nil || tenant != p.ID {
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
			signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "list-batch")
			if err != nil {
				t.Fatal(err)
			}
			site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.ModelPolicy{AllowSuperuser: true}})
			if err != nil {
				t.Fatal(err)
			}
			saved, audits := 0, 0
			var secondProposal models.Record
			update := func(ctx context.Context, id int64) error {
				_, err := db.ExecutorFor(ctx, backend).Exec(ctx, "UPDATE shop_product SET name=$1 WHERE id=$2", "callback replacement", id)
				return err
			}
			options := admin.ModelAdmin{Schema: products[0].Schema(), Fields: []string{"name"}, ListDisplay: []string{"id", "name"}, ListEditable: []string{"name"}, ConstraintChecker: store,
				SaveModel: func(ctx context.Context, scoped admin.ScopedStore, object admin.Object) (admin.Object, error) {
					if mode == "foreign committed" && saved == 1 {
						return admin.Object{}, &db.CommittedCallbackError{Errors: []error{errors.New("private foreign transaction")}}
					}
					return scoped.Save(ctx, object)
				},
				GetReadonlyFields: func(ctx context.Context, object admin.Object) []string {
					if db.InTransaction(ctx, backend.Alias()) {
						id, _ := object.Record.Get("id")
						if id == products[1].ID && saved == 0 {
							secondProposal = object.Record
						}
					}
					if mode == "late readonly" && saved == 2 {
						return []string{"name"}
					}
					return nil
				}, SaveRelated: func(ctx context.Context, _ admin.ScopedStore, _ admin.Object, _ *http.Request) error {
					saved++
					if mode == "proposed sibling" && saved == 1 {
						return secondProposal.Set("name", "callback replacement")
					}
					if saved == 2 {
						switch mode {
						case "precommit panic":
							panic("private precommit callback panic")
						case "later saved sibling":
							return update(ctx, products[0].ID)
						case "untouched sibling":
							return update(ctx, products[2].ID)
						case "later scope":
							excluded = products[0].ID
						case "committed callback":
							return db.OnCommit(ctx, backend.Alias(), func(context.Context) error { return errors.New("private post-commit callback") }, false)
						}
					}
					return nil
				}, Authorize: func(ctx context.Context, _ auth.Principal, action string, object admin.Object) error {
					if mode == "final policy sibling" && db.InTransaction(ctx, backend.Alias()) && saved == 2 && action == "change" && object.Record != nil {
						id, _ := object.Record.Get("id")
						if id == products[1].ID {
							return update(ctx, products[0].ID)
						}
					}
					return nil
				}}
			if err := site.Register(options); err != nil {
				t.Fatal(err)
			}
			store.AfterSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
				if event.Record.Schema().Key() == admin.LogSchema().Key() {
					audits++
					if mode == "second audit failure" && audits == 2 {
						return errors.New("private second audit failure")
					}
					if mode == "foreign unknown" && audits == 2 {
						return &db.Error{Code: db.UnknownCommit, Message: "private foreign transaction"}
					}
				}
				return nil
			}}
			requestContext := ctx
			request := func(method string, data url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, "http://example.test/admin/shop/product/", strings.NewReader(data.Encode()))
				r = r.WithContext(auth.WithPrincipal(requestContext, p))
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
			get := request("GET", nil, nil)
			if get.Code != 200 {
				t.Fatal(get.Code, get.Body.String())
			}
			hidden := func(name string) string {
				t.Helper()
				match := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(get.Body.String())
				if len(match) != 2 {
					t.Fatal("missing management field", name)
				}
				return html.UnescapeString(match[1])
			}
			data := url.Values{"csrfmiddlewaretoken": {hidden("csrfmiddlewaretoken")}, "_list_token": {hidden("_list_token")}, "_save_list": {"1"}, "form-TOTAL_FORMS": {"3"}, "form-INITIAL_FORMS": {"3"}}
			for index, name := range []string{"Changed first", "Changed second", "Untouched"} {
				prefix := "form-" + strconv.Itoa(index)
				data.Set(prefix+"-_id", hidden(prefix+"-_id"))
				data.Set(prefix+"-name", name)
				data.Set(prefix+"-secret", "forged")
			}
			if mode == "validation" {
				data.Set("form-1-name", "")
			}
			if mode == "unchanged" {
				data.Set("form-0-name", "First")
				data.Set("form-1-name", "Second")
			}
			commitBackend.unknown = mode == "unknown commit"
			var post *httptest.ResponseRecorder
			if mode == "nested transaction" {
				if err := db.Atomic(ctx, backend, db.AtomicOptions{}, func(tx context.Context) error {
					requestContext = tx
					post = request("POST", data, get.Result().Cookies())
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				post = request("POST", data, get.Result().Cookies())
			}
			status, outcome, wantSaved := 303, "changed", true
			switch mode {
			case "unchanged":
				outcome, wantSaved = "unchanged", false
			case "validation":
				status, outcome, wantSaved = 400, "unchanged", false
			case "second audit failure", "foreign committed", "foreign unknown", "precommit panic", "nested transaction":
				status, outcome, wantSaved = 503, "unchanged", false
			case "later saved sibling", "untouched sibling", "final policy sibling", "proposed sibling":
				status, outcome, wantSaved = 409, "unchanged", false
			case "later scope":
				status, outcome, wantSaved = 404, "unchanged", false
			case "late readonly":
				status, outcome, wantSaved = 403, "unchanged", false
			case "unknown commit":
				status, outcome = 503, "unknown"
			case "committed callback":
				status = 503
			}
			if post.Code != status || post.Header().Get("X-Gogo-List-Change") != outcome || strings.Contains(post.Body.String(), "private ") {
				t.Fatal(post.Code, post.Header(), post.Body.String())
			}
			for index, original := range products {
				current, err := orm.For(store, func() *adminProduct { return &adminProduct{} }).Filter(orm.Q("id", original.ID)).Get(ctx)
				if err != nil {
					t.Fatal(err)
				}
				name := original.Name
				if wantSaved && index < 2 {
					name = []string{"Changed first", "Changed second"}[index]
				}
				if current.Name != name || current.Secret != original.Secret || current.Tenant != original.Tenant {
					t.Fatal("batch did not preserve expected row", index, current.Name, current.Tenant)
				}
			}
			var count int64
			if err := db.QueryRow(ctx, backend, "SELECT count(*) FROM gogo_admin_log WHERE actor_id=$1", []any{p.ID}, &count); err != nil {
				t.Fatal(err)
			}
			wantCount := int64(0)
			if wantSaved {
				wantCount = 2
			}
			if count != wantCount {
				t.Fatal("partial/duplicate audit survived", count, wantCount)
			}
			if mode == "validation" && saved != 0 {
				t.Fatal("invalid batch reached save callbacks")
			}
		})
	}
}
