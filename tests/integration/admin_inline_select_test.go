package integration_test

import (
	"context"
	"encoding/base64"
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
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

type selectionInline struct {
	models.Base
	ID, ParentID int64
	Tenant, Body string
	Label        *int64
	Only         *int64
	Code         *string
}

func (*selectionInline) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "SelectionInline", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.CharField("tenant", models.WithStructField("Tenant"), models.ReadOnly),
		models.ForeignKeyField("parent", models.Relation{Target: "shop.Selection", OnDelete: models.Cascade}, models.WithStructField("ParentID")),
		models.CharField("body", models.WithStructField("Body")),
		models.ForeignKeyField("label", models.Relation{Target: "shop.SelectionLabel"}, models.WithStructField("Label"), models.Nullable, models.Optional),
		models.OneToOneField("only", models.Relation{Target: "shop.SelectionLabel"}, models.WithStructField("Only"), models.Nullable, models.Optional),
		models.ForeignKeyField("code", models.Relation{Target: "shop.SelectionLabel", TargetFields: []string{"code"}}, models.WithStructField("Code"), models.Nullable, models.Optional),
	}}
}

type inlineSelectionFixture struct {
	t            *testing.T
	backend      db.Backend
	store        *orm.Store
	labels       []*ordinarySelectionLabel
	parent       *ordinarySelection
	children     []*selectionInline
	site         *admin.Site
	onResolve    func(context.Context, models.Field, []string) error
	onScope      func(context.Context, models.Schema) error
	onAuthorize  func(context.Context, string, auth.Resource) error
	afterRelated func(context.Context) error
	resolveCalls int
}

func newInlineSelectionFixture(t *testing.T, configure func(*admin.ModelAdmin)) *inlineSelectionFixture {
	t.Helper()
	backend := testservice.Postgres(t)
	ctx := context.Background()
	schemas := []models.Schema{(&ordinarySelectionLabel{}).Schema(), (&ordinarySelection{}).Schema(), (&selectionInline{}).Schema(), admin.LogSchema()}
	registry := &models.Registry{}
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
	labels := []*ordinarySelectionLabel{{Tenant: "one", Name: "First", Code: "first"}, {Tenant: "one", Name: "Second", Code: "second"}, {Tenant: "two", Name: "Hidden", Code: "hidden"}}
	for _, label := range labels {
		if err := store.Save(ctx, label, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	parent := &ordinarySelection{Tenant: "one", Name: "Parent", Label: &labels[0].ID}
	if err := store.Save(ctx, parent, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	children := []*selectionInline{{Tenant: "one", ParentID: parent.ID, Body: "First child", Label: &labels[0].ID}, {Tenant: "one", ParentID: parent.ID, Body: "Second child", Label: &labels[1].ID}}
	for _, child := range children {
		if err := store.Save(ctx, child, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	fixture := &inlineSelectionFixture{t: t, backend: backend, store: store, labels: labels, parent: parent, children: children}
	adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{
		"shop.Selection":       func() models.Model { return &ordinarySelection{} },
		"shop.SelectionLabel":  func() models.Model { return &ordinarySelectionLabel{} },
		"shop.SelectionInline": func() models.Model { return &selectionInline{} },
	}, QueryScope: func(ctx context.Context, p auth.Principal, schema models.Schema) (admin.QueryScope, error) {
		if fixture.onScope != nil {
			if err := fixture.onScope(ctx, schema); err != nil {
				return admin.QueryScope{}, err
			}
		}
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
	resolver := func(ctx context.Context, field models.Field, ids []string) ([]any, error) {
		fixture.resolveCalls++
		if len(ids) != 1 {
			return nil, auth.ErrPermissionDenied
		}
		if fixture.onResolve != nil {
			if err := fixture.onResolve(ctx, field, ids); err != nil {
				return nil, err
			}
		}
		key := "id"
		if field.Name == "code" {
			key = "code"
		}
		label, err := orm.For(store, func() *ordinarySelectionLabel { return &ordinarySelectionLabel{} }).Filter(orm.Q(key, ids[0]), orm.Q("tenant", auth.FromContext(ctx).ID)).Get(ctx)
		if errors.Is(err, orm.ErrNotFound) {
			return nil, auth.ErrPermissionDenied
		}
		if err != nil {
			return nil, err
		}
		if key == "code" {
			return []any{label.Code}, nil
		}
		return []any{label.ID}, nil
	}
	key, _ := security.RandomToken(32)
	signer, _ := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "inline-selection")
	site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.PolicyFunc(func(ctx context.Context, principal auth.Principal, action string, resource auth.Resource) error {
		if fixture.onAuthorize != nil {
			if err := fixture.onAuthorize(ctx, action, resource); err != nil {
				return err
			}
		}
		return (auth.ModelPolicy{AllowSuperuser: true}).Authorize(ctx, principal, action, resource)
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Register(admin.ModelAdmin{Schema: labels[0].Schema(), Fields: []string{"name", "code"}}); err != nil {
		t.Fatal(err)
	}
	options := admin.ModelAdmin{Schema: parent.Schema(), Fields: []string{"name", "label"}, ListDisplay: []string{"name"}, ConstraintChecker: store, ResolveRelation: resolver, Inlines: []admin.Inline{{Name: "rows", Schema: children[0].Schema(), FKName: "parent", Fields: []string{"body", "label"}, Maximum: 4, ConstraintChecker: store, ResolveRelation: resolver}}, SaveRelated: func(ctx context.Context, _ admin.ScopedStore, _ admin.Object, _ *http.Request) error {
		if fixture.afterRelated != nil {
			return fixture.afterRelated(ctx)
		}
		return nil
	}}
	if configure != nil {
		configure(&options)
	}
	if err := site.Register(options); err != nil {
		t.Fatal(err)
	}
	fixture.site = site
	return fixture
}

func (fixture *inlineSelectionFixture) request(method, path string, values url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	principal := auth.Principal{ID: "one", Authenticated: true, Active: true, Staff: true, Superuser: true}
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
	fixture.site.ServeHTTP(w, r)
	return w
}

func (fixture *inlineSelectionFixture) edit() (string, *httptest.ResponseRecorder, url.Values) {
	t := fixture.t
	t.Helper()
	hidden := func(body, name string) string {
		t.Helper()
		match := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(body)
		if len(match) != 2 {
			t.Fatalf("missing %s", name)
		}
		return match[1]
	}
	list := fixture.request("GET", "/admin/shop/selection/", nil, nil)
	link := regexp.MustCompile(`href="(/admin/shop/selection/[^"]+/change/)"`).FindStringSubmatch(list.Body.String())
	if len(link) != 2 {
		t.Fatal(list.Code, list.Body.String())
	}
	page := fixture.request("GET", link[1], nil, nil)
	if page.Code != 200 {
		t.Fatal(page.Code, page.Body.String())
	}
	values := url.Values{"name": {"Updated parent"}, "label": {"1"}, "rows-TOTAL_FORMS": {"2"}, "rows-INITIAL_FORMS": {"2"}, "_edit_token": {hidden(page.Body.String(), "_edit_token")}, "csrfmiddlewaretoken": {hidden(page.Body.String(), "csrfmiddlewaretoken")}}
	for i := range fixture.children {
		prefix := "rows-" + strconv.Itoa(i)
		id := hidden(page.Body.String(), prefix+"-_id")
		var child *selectionInline
		for _, candidate := range fixture.children {
			if inlineObjectID(candidate.ID) == id {
				child = candidate
				break
			}
		}
		if child == nil {
			t.Fatal("unrecognized scoped inline", id)
		}
		values.Set(prefix+"-body", "Updated "+child.Body)
		values.Set(prefix+"-label", strconv.FormatInt(*child.Label, 10))
		values.Set(prefix+"-_id", id)
		values.Set(prefix+"-_edit_token", hidden(page.Body.String(), prefix+"-_edit_token"))
	}
	return link[1], page, values
}

func inlineObjectID(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("[%d]", id)))
}

func (fixture *inlineSelectionFixture) assertRolledBack() {
	t := fixture.t
	t.Helper()
	ctx := context.Background()
	var label, logs int64
	if err := db.QueryRow(ctx, fixture.backend, "SELECT label FROM shop_selectioninline WHERE id=$1", []any{fixture.children[0].ID}, &label); err != nil || label != fixture.labels[0].ID {
		t.Fatal("earlier inline failed to roll back", label, err)
	}
	if err := db.QueryRow(ctx, fixture.backend, "SELECT count(*) FROM gogo_admin_log", nil, &logs); err != nil || logs != 0 {
		t.Fatal("inline audit failed to roll back", logs, err)
	}
	var name, tenant, body string
	if err := db.QueryRow(ctx, fixture.backend, "SELECT name, label FROM shop_selection WHERE id=$1", []any{fixture.parent.ID}, &name, &label); err != nil || name != "Parent" || label != fixture.labels[0].ID {
		t.Fatal("parent failed to roll back", name, label, err)
	}
	if err := db.QueryRow(ctx, fixture.backend, "SELECT name, tenant FROM shop_selectionlabel WHERE id=$1", []any{fixture.labels[0].ID}, &name, &tenant); err != nil || name != "First" || tenant != "one" {
		t.Fatal("target failed to roll back", name, tenant, err)
	}
	var owner int64
	if err := db.QueryRow(ctx, fixture.backend, "SELECT parent, body FROM shop_selectioninline WHERE id=$1", []any{fixture.children[0].ID}, &owner, &body); err != nil || owner != fixture.parent.ID || body != "First child" {
		t.Fatal("inline ownership/body failed to roll back", owner, body, err)
	}
}

func TestAdminInlineRelationFinalFenceRejectsLaterCallbacks(t *testing.T) {
	for _, mode := range []string{"save-earlier-inline", "save-earlier-owner", "resolver-parent", "resolver-earlier-inline", "resolver-earlier-target-scope", "resolver-earlier-target-version", "scope-earlier-inline", "scope-earlier-target"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newInlineSelectionFixture(t, nil)
			path, page, values := fixture.edit()
			var changed, finalPhase bool
			mutate := func(ctx context.Context) error {
				changed = true
				query := "UPDATE shop_selectioninline SET label=2 WHERE id=1"
				switch mode {
				case "save-earlier-owner":
					// Another valid owner exists, so only the Admin owner fence
					// (not a database foreign-key error) can reject this change.
					query = "UPDATE shop_selectioninline SET parent=2 WHERE id=1"
				case "resolver-parent":
					query = "UPDATE shop_selection SET label=2 WHERE id=1"
				case "resolver-earlier-target-scope":
					query = "UPDATE shop_selectionlabel SET tenant='two' WHERE id=1"
				case "resolver-earlier-target-version", "scope-earlier-target":
					query = "UPDATE shop_selectionlabel SET name='Changed by last callback' WHERE id=1"
				}
				_, err := db.ExecutorFor(ctx, fixture.backend).Exec(ctx, query)
				return err
			}
			if mode == "save-earlier-owner" {
				if err := fixture.store.Save(context.Background(), &ordinarySelection{Tenant: "one", Name: "Other parent"}, orm.SaveOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			fixture.afterRelated = func(context.Context) error { finalPhase = true; return nil }
			fixture.store.BeforeSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
				if child, ok := event.Model.(*selectionInline); ok && child.ID == fixture.children[1].ID && strings.HasPrefix(mode, "save-") {
					return mutate(ctx)
				}
				return nil
			}}
			lastResolver := false
			fixture.onResolve = func(ctx context.Context, _ models.Field, ids []string) error {
				if finalPhase && ids[0] == "2" && !changed {
					lastResolver = true
					if strings.HasPrefix(mode, "resolver-") {
						return mutate(ctx)
					}
				}
				return nil
			}
			inlineScopes := 0
			fixture.onScope = func(ctx context.Context, schema models.Schema) error {
				// Only after the last row's final resolver has returned, count
				// source-scope preparation. The last inline source scope must
				// run before the first row's final callback-free checks.
				if lastResolver && schema.Key() == "shop.SelectionInline" {
					inlineScopes++
					if inlineScopes == 2 && strings.HasPrefix(mode, "scope-") {
						return mutate(ctx)
					}
				}
				return nil
			}
			post := fixture.request("POST", path, values, page.Result().Cookies())
			if !changed || post.Code != 403 {
				t.Fatalf("cross-row callback escaped final fence: changed=%v status=%d body=%s", changed, post.Code, post.Body.String())
			}
			fixture.assertRolledBack()
		})
	}
}

func inlineSelectHTML(t *testing.T, body, name string) string {
	t.Helper()
	match := regexp.MustCompile(`(?s)<select\b[^>]*name="` + regexp.QuoteMeta(name) + `"[^>]*>.*?</select>`).FindString(body)
	if match == "" {
		t.Fatalf("missing ordinary select %s", name)
	}
	return match
}

func TestAdminInlineRelationSelectChoicesAndWriteModes(t *testing.T) {
	for _, mode := range []string{"clear", "change", "new-row", "ignored-empty-row", "delete", "unique-target", "one-to-one", "forged", "duplicate-identity", "stale", "resolver-failure", "override-narrows"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newInlineSelectionFixture(t, func(options *admin.ModelAdmin) {
				if mode == "resolver-failure" {
					options.Fields = []string{"name"}
				}
				inline := &options.Inlines[0]
				inline.Extra = 1
				inline.CanDelete = true
				if mode == "unique-target" {
					inline.Fields = append(inline.Fields, "code")
				}
				if mode == "one-to-one" {
					inline.Fields = append(inline.Fields, "only")
				}
				if mode == "override-narrows" {
					field, _ := inline.Schema.Field("label")
					override, _ := forms.FieldFromModel(field)
					override.Choices = []forms.Choice{{Value: "1", Label: "User override"}, {Value: "3", Label: "Hidden override"}}
					inline.FormOverrides = map[string]forms.Field{"label": override}
				}
			})
			path, page, values := fixture.edit()
			for _, row := range []int{0, 1, 2} {
				selectHTML := inlineSelectHTML(t, page.Body.String(), fmt.Sprintf("rows-%d-label", row))
				for _, id := range []string{"", "1"} {
					if !strings.Contains(selectHTML, `value="`+id+`"`) {
						t.Fatal("eligible/empty choice missing", selectHTML)
					}
				}
				if strings.Contains(selectHTML, `value="3"`) || strings.Contains(selectHTML, "Hidden override") {
					t.Fatal("hidden choice disclosed", selectHTML)
				}
				if mode == "override-narrows" && strings.Contains(selectHTML, `value="2"`) {
					t.Fatal("override widened", selectHTML)
				}
			}
			// Two candidates for the parent and one shared inline choice
			// population, independent of the three rendered inline rows.
			if mode == "change" && fixture.resolveCalls != 4 {
				t.Fatal("inline choices reloaded per row", fixture.resolveCalls)
			}
			want := http.StatusSeeOther
			switch mode {
			case "clear":
				values.Set("rows-0-label", "")
			case "change":
				values.Set("rows-0-label", "2")
			case "new-row", "ignored-empty-row":
				values.Set("rows-TOTAL_FORMS", "3")
				if mode == "new-row" {
					values.Set("rows-2-body", "Added child")
					values.Set("rows-2-label", "2")
				}
			case "delete":
				values.Set("rows-0-DELETE", "on")
				values.Set("rows-0-label", "3")
			case "unique-target":
				values.Set("rows-0-code", "second")
			case "one-to-one":
				values.Set("rows-0-only", "2")
			case "forged":
				values.Set("rows-0-label", "3")
				want = http.StatusBadRequest
			case "duplicate-identity":
				values.Set("rows-1-_id", values.Get("rows-0-_id"))
				want = http.StatusBadRequest
			case "stale":
				if _, err := fixture.backend.Exec(context.Background(), "UPDATE shop_selectionlabel SET tenant='two' WHERE id=2"); err != nil {
					t.Fatal(err)
				}
				want = http.StatusBadRequest
			case "resolver-failure":
				fixture.onResolve = func(context.Context, models.Field, []string) error { return errors.New("private resolver details") }
				want = http.StatusServiceUnavailable
			case "override-narrows":
				values.Set("rows-1-label", "1")
			}
			post := fixture.request("POST", path, values, page.Result().Cookies())
			if post.Code != want {
				t.Fatalf("status=%d want=%d body=%s", post.Code, want, post.Body.String())
			}
			if strings.Contains(post.Body.String(), "private resolver details") {
				t.Fatal("provider error disclosed")
			}
			if want != http.StatusSeeOther {
				fixture.assertRolledBack()
				return
			}
			ctx := context.Background()
			rows, err := orm.For(fixture.store, func() *selectionInline { return &selectionInline{} }).OrderBy("id").All(ctx)
			if err != nil {
				t.Fatal(err)
			}
			count := 2
			if mode == "new-row" {
				count = 3
			}
			if mode == "delete" {
				count = 1
			}
			if len(rows) != count {
				t.Fatal("inline row count", len(rows), count)
			}
			for _, row := range rows {
				if row.ParentID != fixture.parent.ID {
					t.Fatal("wrong parent", row.ParentID)
				}
			}
			switch mode {
			case "clear":
				if rows[0].Label != nil {
					t.Fatal("optional selection not cleared")
				}
			case "change":
				if rows[0].Label == nil || *rows[0].Label != 2 {
					t.Fatal("selection not saved")
				}
			case "new-row":
				if rows[2].Label == nil || *rows[2].Label != 2 || rows[2].Body != "Added child" {
					t.Fatal("new row not saved")
				}
			case "unique-target":
				if rows[0].Code == nil || *rows[0].Code != "second" {
					t.Fatal("non-primary target not saved")
				}
			case "one-to-one":
				if rows[0].Only == nil || *rows[0].Only != 2 {
					t.Fatal("one-to-one target not saved")
				}
			}
		})
	}
}

func TestAdminInlineRelationSelectPreservesExplicitModesAndReadonlyRows(t *testing.T) {
	for _, mode := range []string{"custom-text", "disabled", "readonly-field", "readonly-row"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newInlineSelectionFixture(t, func(options *admin.ModelAdmin) {
				inline := &options.Inlines[0]
				if mode == "readonly-field" {
					inline.Readonly = []string{"label"}
				} else if mode == "custom-text" || mode == "disabled" {
					metadata, _ := inline.Schema.Field("label")
					field, _ := forms.FieldFromModel(metadata)
					field.Widget = forms.InputWidget{Type: "text"}
					field.Disabled = mode == "disabled"
					inline.FormOverrides = map[string]forms.Field{"label": field}
				}
			})
			if mode == "readonly-row" {
				fixture.onAuthorize = func(_ context.Context, action string, resource auth.Resource) error {
					if action == "change" && resource.Model == "SelectionInline" {
						if record, ok := resource.Object.(models.Record); ok {
							if id, _ := record.Get("id"); id == fixture.children[0].ID {
								return auth.ErrPermissionDenied
							}
						}
					}
					return nil
				}
			}
			path, page, values := fixture.edit()
			if regexp.MustCompile(`<select\b[^>]*name="rows-0-label"`).MatchString(page.Body.String()) {
				t.Fatal("explicit or readonly mode replaced with ordinary select")
			}
			values.Set("rows-0-label", "2")
			post := fixture.request("POST", path, values, page.Result().Cookies())
			if post.Code != http.StatusSeeOther {
				t.Fatal(post.Code, post.Body.String())
			}
			var label int64
			var body string
			if err := db.QueryRow(context.Background(), fixture.backend, "SELECT label,body FROM shop_selectioninline WHERE id=1", nil, &label, &body); err != nil {
				t.Fatal(err)
			}
			want := int64(1)
			if mode == "custom-text" {
				want = 2
			}
			if label != want {
				t.Fatal("explicit relation mode did not preserve binding", label, want)
			}
			if mode == "readonly-row" && body != "First child" {
				t.Fatal("readonly row changed", body)
			}
		})
	}
}

func TestAdminInlineRelationSelectRedactsHiddenPriorAuditIdentity(t *testing.T) {
	fixture := newInlineSelectionFixture(t, nil)
	if _, err := fixture.backend.Exec(context.Background(), "UPDATE shop_selectioninline SET label=3 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	path, page, values := fixture.edit()
	for i := range fixture.children {
		if strings.Contains(inlineSelectHTML(t, page.Body.String(), fmt.Sprintf("rows-%d-label", i)), `value="3"`) {
			t.Fatal("hidden current value appended to choices")
		}
	}
	post := fixture.request("POST", path, values, page.Result().Cookies())
	if post.Code != http.StatusSeeOther {
		t.Fatal(post.Code, post.Body.String())
	}
	var changes string
	if err := db.QueryRow(context.Background(), fixture.backend, "SELECT changed_fields::text FROM gogo_admin_log WHERE model='shop.SelectionInline' AND object_id=$1", []any{inlineObjectID(fixture.children[0].ID)}, &changes); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(changes, `"label"`) || !strings.Contains(changes, `"body"`) {
		t.Fatal("hidden previous relation was disclosed or misrepresented as null", changes)
	}
}

func TestAdminInlineRelationSelectLateProviderFailureRollsBack(t *testing.T) {
	fixture := newInlineSelectionFixture(t, nil)
	path, page, values := fixture.edit()
	late := false
	fixture.afterRelated = func(context.Context) error { late = true; return nil }
	fixture.onResolve = func(_ context.Context, _ models.Field, ids []string) error {
		if late && ids[0] == "2" {
			return errors.New("private inline target provider failure")
		}
		return nil
	}
	post := fixture.request("POST", path, values, page.Result().Cookies())
	if post.Code != http.StatusServiceUnavailable || strings.Contains(post.Body.String(), "private inline") {
		t.Fatal(post.Code, post.Body.String())
	}
	fixture.assertRolledBack()
}

func TestAdminInlineRelationSelectBindingProviderFailureIsOperational(t *testing.T) {
	for _, call := range []int{3, 5} {
		t.Run(fmt.Sprintf("resolver-call-%d", call), func(t *testing.T) {
			fixture := newInlineSelectionFixture(t, func(options *admin.ModelAdmin) { options.Fields = []string{"name"} })
			path, page, values := fixture.edit()
			fixture.resolveCalls = 0
			failed := false
			fixture.onResolve = func(context.Context, models.Field, []string) error {
				// The two display candidates precede formset cleaning; both
				// formset rows then precede model-form cleaning.
				if fixture.resolveCalls == call {
					failed = true
					return errors.New("private binding resolver error")
				}
				return nil
			}
			post := fixture.request("POST", path, values, page.Result().Cookies())
			if !failed || post.Code != http.StatusServiceUnavailable || strings.Contains(post.Body.String(), "private binding") {
				t.Fatal("provider failure became a validation response", failed, post.Code, post.Body.String())
			}
			fixture.assertRolledBack()
		})
	}
}

func TestAdminInlineRelationSelectReadonlyAuthorityCannotWidenDuringBinding(t *testing.T) {
	for _, mode := range []string{"initial-denial", "formset-denial"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newInlineSelectionFixture(t, nil)
			path, page, values := fixture.edit()
			calls := 0
			fixture.onAuthorize = func(_ context.Context, action string, resource auth.Resource) error {
				if action == "change" && resource.Model == "SelectionInline" {
					calls++
					if mode == "initial-denial" && calls <= 2 || mode == "formset-denial" && calls > 2 && calls <= 4 {
						return auth.ErrPermissionDenied
					}
				}
				return nil
			}
			values.Set("rows-0-label", "2")
			post := fixture.request("POST", path, values, page.Result().Cookies())
			if post.Code != http.StatusSeeOther {
				t.Fatal(post.Code, post.Body.String())
			}
			var body string
			var label, audits int64
			if err := db.QueryRow(context.Background(), fixture.backend, "SELECT body,label FROM shop_selectioninline WHERE id=1", nil, &body, &label); err != nil || body != "First child" || label != 1 {
				t.Fatal("a later policy grant widened earlier readonly binding", body, label, calls, err)
			}
			if err := db.QueryRow(context.Background(), fixture.backend, "SELECT count(*) FROM gogo_admin_log WHERE model='shop.SelectionInline'", nil, &audits); err != nil || audits != 0 {
				t.Fatal("readonly inline was audited as a write", audits, err)
			}
		})
	}
}
