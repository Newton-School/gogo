package integration_test

import (
	"context"
	"errors"
	"fmt"
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

type ordinarySelection struct {
	models.Base
	ID           int64
	Tenant, Name string
	Label, Only  *int64
	Code         *string
}

func (*ordinarySelection) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "Selection", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.CharField("tenant", models.WithStructField("Tenant"), models.ReadOnly),
		models.CharField("name", models.WithStructField("Name")),
		models.ForeignKeyField("label", models.Relation{Target: "shop.SelectionLabel"}, models.WithStructField("Label"), models.Nullable, models.Optional),
		models.OneToOneField("only", models.Relation{Target: "shop.SelectionLabel"}, models.WithStructField("Only"), models.Nullable, models.Optional),
		models.ForeignKeyField("code", models.Relation{Target: "shop.SelectionLabel", TargetFields: []string{"code"}}, models.WithStructField("Code"), models.Nullable, models.Optional),
	}}
}

type ordinarySelectionLabel struct {
	models.Base
	ID                 int64
	Tenant, Name, Code string
}

func (*ordinarySelectionLabel) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "SelectionLabel", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.CharField("tenant", models.WithStructField("Tenant"), models.ReadOnly),
		models.CharField("name", models.WithStructField("Name")),
		models.CharField("code", models.WithStructField("Code"), models.WithMaxLength(40), models.UniqueValue),
	}}
}

type ordinaryAuditStore struct {
	admin.Store
	afterAudit func(context.Context) error
}

func (store *ordinaryAuditStore) Scope(ctx context.Context, p auth.Principal, site string, schema models.Schema) (admin.ScopedStore, error) {
	scope, err := store.Store.Scope(ctx, p, site, schema)
	if err != nil {
		return nil, err
	}
	return ordinaryAuditScope{ScopedStore: scope, owner: store}, nil
}

type ordinaryAuditScope struct {
	admin.ScopedStore
	owner *ordinaryAuditStore
}

func (scope ordinaryAuditScope) Audit(ctx context.Context, entry admin.LogEntry) error {
	if err := scope.ScopedStore.Audit(ctx, entry); err != nil {
		return err
	}
	if scope.owner.afterAudit != nil {
		return scope.owner.afterAudit(ctx)
	}
	return nil
}

func TestAdminOrdinaryRelationSelectScopedPersistenceAndRollback(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx := context.Background()
	registry := &models.Registry{}
	schemas := []models.Schema{(&ordinarySelectionLabel{}).Schema(), (&ordinarySelection{}).Schema(), admin.LogSchema()}
	for _, schema := range schemas {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	editor, err := backend.SchemaEditor().(db.SchemaResolverEditor).WithSchemas(registry.All())
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range schemas {
		if err := editor.CreateModel(ctx, backend, schema); err != nil {
			t.Fatal(err)
		}
	}
	store := orm.New(backend, registry)
	labels := []*ordinarySelectionLabel{
		{Tenant: "one", Name: "First visible", Code: "visible-one"},
		{Tenant: "one", Name: "Second visible", Code: "visible-two"},
		{Tenant: "two", Name: "Tenant-hidden label", Code: "tenant-hidden"},
		{Tenant: "one", Name: "Policy-hidden label", Code: "policy-hidden"},
		{Tenant: "one", Name: "Resolver-hidden label", Code: "resolver-hidden"},
	}
	for _, label := range labels {
		if err := store.Save(ctx, label, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	parent := &ordinarySelection{Tenant: "one", Name: "Original", Label: &labels[0].ID}
	if err := store.Save(ctx, parent, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{
		"shop.Selection":      func() models.Model { return &ordinarySelection{} },
		"shop.SelectionLabel": func() models.Model { return &ordinarySelectionLabel{} },
	}, QueryScope: func(_ context.Context, p auth.Principal, _ models.Schema) (admin.QueryScope, error) {
		return admin.QueryScope{Predicate: orm.Q("tenant", p.ID), Identity: p.ID}, nil
	}, ValidateWrite: func(_ context.Context, p auth.Principal, record models.Record) error {
		tenant, _ := record.Get("tenant")
		if tenant != p.ID {
			return auth.ErrPermissionDenied
		}
		return nil
	}, Initialize: func(_ context.Context, p auth.Principal, record models.Record) error {
		return record.Set("tenant", p.ID)
	}})
	if err != nil {
		t.Fatal(err)
	}
	audited := &ordinaryAuditStore{Store: adapter}
	denyTarget, resolverCalls, failResolverAt := false, 0, 0
	var policyEffect func(context.Context) error
	var resolverEffect func(context.Context, models.Field) error
	policy := auth.PolicyFunc(func(ctx context.Context, p auth.Principal, action string, resource auth.Resource) error {
		if resource.Model == "SelectionLabel" && action == "view" {
			if denyTarget {
				return auth.ErrPermissionDenied
			}
			if record, ok := resource.Object.(models.Record); ok && record != nil {
				if policyEffect != nil {
					if err := policyEffect(ctx); err != nil {
						return err
					}
				}
				id, _ := record.Get("id")
				if id == labels[3].ID {
					return auth.ErrPermissionDenied
				}
			}
		}
		return (auth.ModelPolicy{AllowSuperuser: true}).Authorize(ctx, p, action, resource)
	})
	key, _ := security.RandomToken(32)
	signer, _ := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "admin-ordinary-relations")
	site, err := admin.NewSite(admin.Config{Store: audited, Signer: signer, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	var saveRelated func(context.Context, admin.Object) error
	options := admin.ModelAdmin{Schema: parent.Schema(), Fields: []string{"name", "label", "only", "code"}, ListDisplay: []string{"name"}, ConstraintChecker: store,
		ResolveRelation: func(ctx context.Context, field models.Field, ids []string) ([]any, error) {
			resolverCalls++
			if failResolverAt > 0 && resolverCalls >= failResolverAt {
				return nil, errors.New("private selection provider unavailable")
			}
			if len(ids) != 1 {
				return nil, auth.ErrPermissionDenied
			}
			lookup := "id"
			if field.Name == "code" {
				lookup = "code"
			}
			label, err := orm.For(store, func() *ordinarySelectionLabel { return &ordinarySelectionLabel{} }).Filter(orm.Q(lookup, ids[0]), orm.Q("tenant", auth.FromContext(ctx).ID)).Get(ctx)
			if errors.Is(err, orm.ErrNotFound) || err == nil && label.ID == labels[4].ID {
				return nil, auth.ErrPermissionDenied
			}
			if err != nil {
				return nil, err
			}
			if resolverEffect != nil {
				if err := resolverEffect(ctx, field); err != nil {
					return nil, err
				}
			}
			if lookup == "code" {
				return []any{label.Code}, nil
			}
			return []any{label.ID}, nil
		}, SaveRelated: func(ctx context.Context, _ admin.ScopedStore, object admin.Object, _ *http.Request) error {
			if saveRelated != nil {
				return saveRelated(ctx, object)
			}
			return nil
		}}
	if err := site.Register(options); err != nil {
		t.Fatal(err)
	}
	if err := site.Register(admin.ModelAdmin{Schema: labels[0].Schema(), Fields: []string{"name", "code"}, ListDisplay: []string{"name"}}); err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{ID: "one", Authenticated: true, Active: true, Staff: true, Superuser: true}
	request := func(method, path string, values url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(values.Encode()))
		r = r.WithContext(auth.WithPrincipal(r.Context(), principal))
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
	hidden := func(body, name string) string {
		t.Helper()
		match := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(body)
		if len(match) != 2 {
			t.Fatalf("missing %s", name)
		}
		return match[1]
	}
	list := request("GET", "/admin/shop/selection/", nil, nil)
	link := regexp.MustCompile(`href="(/admin/shop/selection/[^"]+/change/)"`).FindStringSubmatch(list.Body.String())
	if len(link) != 2 {
		t.Fatal(list.Code, list.Body.String())
	}
	postValues := func(page *httptest.ResponseRecorder, name, label, only, code string) url.Values {
		return url.Values{"name": {name}, "label": {label}, "only": {only}, "code": {code}, "_edit_token": {hidden(page.Body.String(), "_edit_token")}, "csrfmiddlewaretoken": {hidden(page.Body.String(), "csrfmiddlewaretoken")}}
	}
	read := func() *ordinarySelection {
		t.Helper()
		value, err := orm.For(store, func() *ordinarySelection { return &ordinarySelection{} }).Filter(orm.Q("id", parent.ID)).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	auditCount := func() int64 {
		t.Helper()
		var count int64
		if err := db.QueryRow(ctx, backend, "SELECT count(*) FROM gogo_admin_log", nil, &count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	page := request("GET", link[1], nil, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `<option value=""`) || !strings.Contains(page.Body.String(), `value="visible-two"`) {
		t.Fatal(page.Code, page.Body.String())
	}
	for _, label := range labels[2:] {
		if strings.Contains(page.Body.String(), label.Name) || strings.Contains(page.Body.String(), label.Code) {
			t.Fatal("hidden relationship disclosed in ordinary select")
		}
	}
	post := request("POST", link[1], postValues(page, "Updated", "2", "2", "visible-two"), page.Result().Cookies())
	if post.Code != 303 {
		t.Fatal(post.Code, post.Body.String())
	}
	current := read()
	if current.Label == nil || *current.Label != labels[1].ID || current.Only == nil || *current.Only != labels[1].ID || current.Code == nil || *current.Code != labels[1].Code || auditCount() != 1 {
		t.Fatal("FK, one-to-one or non-primary target did not persist", current)
	}
	for _, id := range []string{"3", "4", "5", "999"} {
		page = request("GET", link[1], nil, nil)
		post = request("POST", link[1], postValues(page, "Forged", id, "2", "visible-two"), page.Result().Cookies())
		if post.Code != 400 || read().Name != "Updated" || auditCount() != 1 {
			t.Fatal("hidden/unknown selection accepted", id, post.Code)
		}
	}
	page = request("GET", link[1], nil, nil)
	if _, err := backend.Exec(ctx, "UPDATE shop_selectionlabel SET tenant='two' WHERE id=$1", labels[0].ID); err != nil {
		t.Fatal(err)
	}
	post = request("POST", link[1], postValues(page, "Stale target", "1", "2", "visible-two"), page.Result().Cookies())
	if post.Code != 400 || read().Name != "Updated" || auditCount() != 1 {
		t.Fatal("target scope was not reloaded on POST", post.Code)
	}
	if _, err := backend.Exec(ctx, "UPDATE shop_selectionlabel SET tenant='one' WHERE id=$1", labels[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"before-save-retarget", "related-retarget", "related-target-scope", "audit-retarget", "audit-target-scope", "late-policy-target-scope", "late-resolver-target-scope", "final-resolver-retarget", "final-resolver-earlier-target-scope", "final-resolver-earlier-target-data", "audit-failure"} {
		t.Run(mode, func(t *testing.T) {
			mutate := func(ctx context.Context) error {
				if mode == "final-resolver-earlier-target-data" {
					_, err := db.ExecutorFor(ctx, backend).Exec(ctx, "UPDATE shop_selectionlabel SET name='Authorization data changed' WHERE id=$1", labels[1].ID)
					return err
				}
				if strings.Contains(mode, "target-scope") {
					_, err := db.ExecutorFor(ctx, backend).Exec(ctx, "UPDATE shop_selectionlabel SET tenant='two' WHERE id=$1", labels[1].ID)
					return err
				}
				_, err := db.ExecutorFor(ctx, backend).Exec(ctx, "UPDATE shop_selection SET label=$1 WHERE id=$2", labels[0].ID, parent.ID)
				return err
			}
			switch mode {
			case "before-save-retarget":
				store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
					if record, ok := event.Model.(*ordinarySelection); ok {
						record.Label = &labels[0].ID
					}
					return nil
				}}
			case "related-retarget", "related-target-scope":
				saveRelated = func(ctx context.Context, _ admin.Object) error { return mutate(ctx) }
			case "late-policy-target-scope", "late-resolver-target-scope", "final-resolver-retarget", "final-resolver-earlier-target-scope", "final-resolver-earlier-target-data":
				audited.afterAudit = func(context.Context) error {
					if mode == "late-policy-target-scope" {
						policyEffect = mutate
					} else {
						resolverEffect = func(ctx context.Context, field models.Field) error {
							if strings.HasPrefix(mode, "final-resolver-earlier-target-") && field.Name != "code" {
								return nil
							}
							return mutate(ctx)
						}
					}
					return nil
				}
			default:
				audited.afterAudit = func(ctx context.Context) error {
					if mode == "audit-failure" {
						return errors.New("private audit provider unavailable")
					}
					return mutate(ctx)
				}
			}
			page = request("GET", link[1], nil, nil)
			code := "visible-two"
			if strings.HasPrefix(mode, "final-resolver-earlier-target-") {
				code = "visible-one"
			}
			post = request("POST", link[1], postValues(page, "Must roll back", "2", "2", code), page.Result().Cookies())
			want := 403
			if mode == "audit-failure" {
				want = 503
			}
			if post.Code != want {
				t.Fatal("late relationship mutation escaped", post.Code, post.Body.String())
			}
			store.BeforeSave, saveRelated, audited.afterAudit, policyEffect, resolverEffect = nil, nil, nil, nil, nil
			current := read()
			if current.Name != "Updated" || current.Label == nil || *current.Label != labels[1].ID || auditCount() != 1 {
				t.Fatal("parent or audit did not roll back", current)
			}
			var tenant string
			if err := db.QueryRow(ctx, backend, "SELECT tenant FROM shop_selectionlabel WHERE id=$1", []any{labels[1].ID}, &tenant); err != nil || tenant != "one" {
				t.Fatal("target did not roll back", tenant, err)
			}
		})
	}
	// A second transaction cannot move a selected target before the parent
	// transaction finishes. A short database lock timeout proves the lock, with
	// no timing-based goroutine assertion and no concurrent fixture mutation.
	lockObserved := false
	saveRelated = func(context.Context, admin.Object) error {
		competitorErr := db.Atomic(context.Background(), backend, db.AtomicOptions{}, func(other context.Context) error {
			if _, err := db.ExecutorFor(other, backend).Exec(other, "SET LOCAL lock_timeout='50ms'"); err != nil {
				return err
			}
			_, err := db.ExecutorFor(other, backend).Exec(other, "UPDATE shop_selectionlabel SET tenant='two' WHERE id=$1", labels[1].ID)
			return err
		})
		if !db.IsCode(competitorErr, db.Unavailable) || !strings.Contains(competitorErr.Error(), "(55P03)") {
			return fmt.Errorf("target lock was not observed: %w", competitorErr)
		}
		lockObserved = true
		return errors.New("roll back lock-verification submission")
	}
	page = request("GET", link[1], nil, nil)
	post = request("POST", link[1], postValues(page, "Lock verification", "2", "2", "visible-two"), page.Result().Cookies())
	saveRelated = nil
	if !lockObserved || post.Code != 503 || read().Name != "Updated" || auditCount() != 1 {
		t.Fatal("selected target lock/rollback failed", lockObserved, post.Code)
	}
	// A provider failure during field cleaning must not become a successful
	// empty choice or a validation-error response containing provider details.
	page = request("GET", link[1], nil, nil)
	resolverCalls, failResolverAt = 0, 10 // Nine display-candidate callbacks precede field cleaning.
	post = request("POST", link[1], postValues(page, "Unavailable", "2", "2", "visible-two"), page.Result().Cookies())
	failResolverAt = 0
	if post.Code != 503 || strings.Contains(post.Body.String(), "private") || read().Name != "Updated" || auditCount() != 1 {
		t.Fatal("field resolver outage was swallowed", post.Code)
	}
	denyTarget = true
	if denied := request("GET", link[1], nil, nil); denied.Code != 403 {
		t.Fatal("target model view denial ignored", denied.Code)
	}
	denyTarget = false
	// Hidden prior values never get reintroduced as an option or into history.
	if _, err := backend.Exec(ctx, "UPDATE shop_selection SET label=$1 WHERE id=$2", labels[2].ID, parent.ID); err != nil {
		t.Fatal(err)
	}
	page = request("GET", link[1], nil, nil)
	if strings.Contains(page.Body.String(), `value="3"`) {
		t.Fatal("hidden stored value appended to choices")
	}
	post = request("POST", link[1], postValues(page, "Visible replacement", "1", "", ""), page.Result().Cookies())
	if post.Code != 303 {
		t.Fatal(post.Code, post.Body.String())
	}
	var raw string
	if err := db.QueryRow(ctx, backend, "SELECT changed_fields::text FROM gogo_admin_log ORDER BY occurred_at DESC LIMIT 1", nil, &raw); err != nil || strings.Contains(raw, `"label"`) {
		t.Fatal("hidden prior ID entered relation audit", raw, err)
	}
	page = request("GET", link[1], nil, nil)
	post = request("POST", link[1], postValues(page, "Cleared", "", "", ""), page.Result().Cookies())
	if post.Code != 303 || read().Label != nil {
		t.Fatal("optional relation could not be cleared", post.Code)
	}
	page = request("GET", "/admin/shop/selection/add/", nil, nil)
	post = request("POST", "/admin/shop/selection/add/", postValues(page, "Created", strconv.FormatInt(labels[0].ID, 10), "", labels[0].Code), page.Result().Cookies())
	if post.Code != 303 {
		t.Fatal("add with ordinary relation failed", post.Code, post.Body.String())
	}
	created, err := orm.For(store, func() *ordinarySelection { return &ordinarySelection{} }).Filter(orm.Q("name", "Created")).Get(ctx)
	if err != nil || created.Label == nil || *created.Label != labels[0].ID || created.Code == nil || *created.Code != labels[0].Code {
		t.Fatal("created relationships missing", fmt.Sprint(created), err)
	}
}
