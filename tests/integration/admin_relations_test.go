package integration_test

import (
	"context"
	"encoding/json"
	"errors"
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

type adminArticle struct {
	models.Base
	ID           int64
	Tenant, Name string
}

func (*adminArticle) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "Article", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.CharField("tenant", models.WithStructField("Tenant"), models.ReadOnly), models.CharField("name", models.WithStructField("Name")), models.ManyToManyField("labels", models.Relation{Target: "shop.Label"}, models.Optional)}}
}

type adminLabel struct {
	models.Base
	ID           int64
	Tenant, Name string
}

func (*adminLabel) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "Label", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.CharField("tenant", models.WithStructField("Tenant"), models.ReadOnly), models.CharField("name", models.WithStructField("Name"))}}
}

type relationAuditStore struct {
	admin.Store
	fail            bool
	beforeRelations func()
}

func (s *relationAuditStore) Scope(ctx context.Context, p auth.Principal, site string, schema models.Schema) (admin.ScopedStore, error) {
	store, err := s.Store.Scope(ctx, p, site, schema)
	if err != nil {
		return nil, err
	}
	return relationAuditScope{ScopedStore: store, RelationStore: store.(admin.RelationStore), owner: s}, nil
}

type relationAuditScope struct {
	admin.ScopedStore
	admin.RelationStore
	owner *relationAuditStore
}

func (s relationAuditScope) Audit(ctx context.Context, entry admin.LogEntry) error {
	if s.owner.fail {
		return errors.New("fixture audit unavailable")
	}
	return s.ScopedStore.Audit(ctx, entry)
}

func (s relationAuditScope) SaveRelations(ctx context.Context, object admin.Object, values map[string][]any, authorize func(context.Context, admin.RelationChange) error) error {
	if s.owner.beforeRelations != nil {
		s.owner.beforeRelations()
	}
	return s.RelationStore.SaveRelations(ctx, object, values, authorize)
}

func TestAdminManyToManyScopedSaveConflictAndAuditRollback(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx := context.Background()
	registry := &models.Registry{}
	base := []models.Schema{(&adminArticle{}).Schema(), (&adminLabel{}).Schema(), admin.LogSchema()}
	for _, schema := range base {
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
	for _, schema := range base {
		if err := editor.CreateModel(ctx, backend, schema); err != nil {
			t.Fatal(err)
		}
	}
	for _, schema := range registry.All() {
		if registry.IsAutomatic(schema.Key()) {
			if err := editor.CreateModel(ctx, backend, schema); err != nil {
				t.Fatal(err)
			}
		}
	}
	store := orm.New(backend, registry)
	article := &adminArticle{Tenant: "one", Name: "Article"}
	if err := store.Save(ctx, article, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	labels := []*adminLabel{{Tenant: "one", Name: "First"}, {Tenant: "one", Name: "Second"}, {Tenant: "two", Name: "Tenant-hidden label"}, {Tenant: "one", Name: "Policy-hidden label"}, {Tenant: "one", Name: "Resolver-hidden label"}}
	labelRecords := []models.Record{}
	for _, label := range labels {
		if err := store.Save(ctx, label, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
		record, _ := models.Bind(label)
		labelRecords = append(labelRecords, record)
	}
	articleRecord, _ := models.Bind(article)
	manager := orm.RelationManager{Store: store, Source: articleRecord, Name: "labels"}
	if err := manager.Add(ctx, labelRecords[0], labelRecords[2], labelRecords[3], labelRecords[4]); err != nil {
		t.Fatal(err)
	}
	adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{"shop.Article": func() models.Model { return &adminArticle{} }, "shop.Label": func() models.Model { return &adminLabel{} }}, QueryScope: func(_ context.Context, p auth.Principal, schema models.Schema) (admin.QueryScope, error) {
		if registry.IsAutomatic(schema.Key()) {
			t.Fatal("automatic intermediary reached tenant-column scope")
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
	audited := &relationAuditStore{Store: adapter}
	key, _ := security.RandomToken(32)
	signer, _ := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "admin-relations")
	policy := auth.PolicyFunc(func(ctx context.Context, p auth.Principal, action string, resource auth.Resource) error {
		if resource.Model == "Label" && action == "view" {
			if record, ok := resource.Object.(models.Record); ok && record != nil {
				id, _ := record.Get("id")
				if id == labels[3].ID {
					return auth.ErrPermissionDenied
				}
			}
		}
		return (auth.ModelPolicy{AllowSuperuser: true}).Authorize(ctx, p, action, resource)
	})
	site, err := admin.NewSite(admin.Config{Store: audited, Signer: signer, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	aliasFirst, denyFirstAtMutation := false, false
	if err := site.Register(admin.ModelAdmin{Schema: article.Schema(), Fields: []string{"name", "labels"}, AutocompleteFields: []string{"labels"}, ListDisplay: []string{"name"}, ConstraintChecker: store, ResolveRelation: func(ctx context.Context, _ models.Field, ids []string) ([]any, error) {
		values := []any{}
		for _, raw := range ids {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id == labels[4].ID || denyFirstAtMutation && id == labels[0].ID {
				return nil, auth.ErrPermissionDenied
			}
			label, err := orm.For(store, func() *adminLabel { return &adminLabel{} }).Filter(orm.Q("id", id), orm.Q("tenant", auth.FromContext(ctx).ID)).Get(ctx)
			if err != nil {
				return nil, auth.ErrPermissionDenied
			}
			if aliasFirst && id == labels[0].ID {
				values = append(values, labels[1].ID)
				continue
			}
			values = append(values, label.ID)
		}
		return values, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if err := site.Register(admin.ModelAdmin{Schema: (&adminLabel{}).Schema(), Fields: []string{"name"}, SearchFields: []string{"name"}}); err != nil {
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
			t.Fatalf("missing %s: %s", name, body)
		}
		return match[1]
	}
	list := request("GET", "/admin/shop/article/", nil, nil)
	link := regexp.MustCompile(`href="(/admin/shop/article/[^"]+/change/)"`).FindStringSubmatch(list.Body.String())
	if len(link) != 2 {
		t.Fatal(list.Code, list.Body.String())
	}
	aliasFirst = true
	aliased := request("GET", link[1], nil, nil)
	if aliased.Code != 200 || strings.Contains(aliased.Body.String(), `value="1" selected`) {
		t.Fatal("resolver identity substitution accepted in initial relations", aliased.Code, aliased.Body.String())
	}
	aliasFirst = false
	get := request("GET", link[1], nil, nil)
	if get.Code != 200 || !strings.Contains(get.Body.String(), `data-choice-target="id_labels"`) || !strings.Contains(get.Body.String(), `value="1" selected`) {
		t.Fatal(get.Code, get.Body.String())
	}
	for _, label := range labels[2:] {
		if strings.Contains(get.Body.String(), label.Name) || strings.Contains(get.Body.String(), `value="`+strconv.FormatInt(label.ID, 10)+`" selected`) {
			t.Fatal("hidden relation disclosed in form", get.Body.String())
		}
	}
	postValues := func(get *httptest.ResponseRecorder, name string, ids ...string) url.Values {
		return url.Values{"name": {name}, "labels": ids, "_edit_token": {hidden(get.Body.String(), "_edit_token")}, "_relation_token": {hidden(get.Body.String(), "_relation_token")}, "csrfmiddlewaretoken": {hidden(get.Body.String(), "csrfmiddlewaretoken")}}
	}
	post := request("POST", link[1], postValues(get, "Updated", "2"), get.Result().Cookies())
	if post.Code != 303 {
		t.Fatal(post.Code, post.Body.String())
	}
	check := func(name string, want ...int64) {
		t.Helper()
		want = append(want, labels[3].ID, labels[4].ID)
		current, err := orm.For(store, func() *adminArticle { return &adminArticle{} }).Filter(orm.Q("id", article.ID)).Get(ctx)
		if err != nil || current.Name != name {
			t.Fatal(current, err)
		}
		rows, err := manager.All(ctx)
		if err != nil || len(rows) != len(want) {
			t.Fatal(rows, err)
		}
		found := map[int64]bool{}
		for _, row := range rows {
			value, _ := row.Get("id")
			found[value.(int64)] = true
		}
		for _, id := range want {
			if !found[id] {
				t.Fatal("missing relationship", id)
			}
		}
	}
	check("Updated", labels[1].ID, labels[2].ID)
	var raw []byte
	if err := db.QueryRow(ctx, backend, "SELECT changed_fields FROM gogo_admin_log ORDER BY occurred_at DESC LIMIT 1", nil, &raw); err != nil {
		t.Fatal(err)
	}
	var changes map[string]any
	if json.Unmarshal(raw, &changes) != nil || changes["labels"] == nil {
		t.Fatal("relation diff missing", string(raw))
	}
	var relationDiff struct{ Before, After []string }
	encodedDiff, _ := json.Marshal(changes["labels"])
	if err := json.Unmarshal(encodedDiff, &relationDiff); err != nil || strings.Join(relationDiff.Before, ",") != "1" || strings.Join(relationDiff.After, ",") != "2" {
		t.Fatal("audit disclosed or counted hidden relationships", string(raw), err)
	}
	stale := request("GET", link[1], nil, nil)
	if err := manager.Add(ctx, labelRecords[0]); err != nil {
		t.Fatal(err)
	}
	post = request("POST", link[1], postValues(stale, "Stale", "2"), stale.Result().Cookies())
	if post.Code != 409 {
		t.Fatal("relation conflict ignored", post.Code, post.Body.String())
	}
	check("Updated", labels[0].ID, labels[1].ID, labels[2].ID)
	get = request("GET", link[1], nil, nil)
	post = request("POST", link[1], postValues(get, "Forged", "3"), get.Result().Cookies())
	if post.Code != 400 {
		t.Fatal("hidden relation accepted", post.Code, post.Body.String())
	}
	for _, label := range labels[3:] {
		get = request("GET", link[1], nil, nil)
		post = request("POST", link[1], postValues(get, "Forged hidden existing", strconv.FormatInt(label.ID, 10)), get.Result().Cookies())
		if post.Code != 400 {
			t.Fatal("existing hidden relation exposed by acceptance", post.Code, post.Body.String())
		}
		check("Updated", labels[0].ID, labels[1].ID, labels[2].ID)
	}
	// A custom store's pre-mutation hook can change application eligibility
	// after form validation and retention were computed. Final authorization
	// must re-run the resolver for actual additions/removals, not hidden links
	// that remain unchanged.
	audited.beforeRelations = func() { denyFirstAtMutation = true }
	get = request("GET", link[1], nil, nil)
	post = request("POST", link[1], postValues(get, "Late resolver denial", "2"), get.Result().Cookies())
	if post.Code != 403 {
		t.Fatal("final relation authorization missed resolver change", post.Code, post.Body.String())
	}
	audited.beforeRelations, denyFirstAtMutation = nil, false
	check("Updated", labels[0].ID, labels[1].ID, labels[2].ID)
	audited.fail = true
	get = request("GET", link[1], nil, nil)
	post = request("POST", link[1], postValues(get, "Must roll back", "2"), get.Result().Cookies())
	if post.Code != 503 {
		t.Fatal(post.Code, post.Body.String())
	}
	check("Updated", labels[0].ID, labels[1].ID, labels[2].ID)
	audited.fail = false
	get = request("GET", "/admin/shop/article/add/", nil, nil)
	post = request("POST", "/admin/shop/article/add/", postValues(get, "Created with labels", "1", "2"), get.Result().Cookies())
	if post.Code != 303 {
		t.Fatal("create with relationships", post.Code, post.Body.String())
	}
	created, err := orm.For(store, func() *adminArticle { return &adminArticle{} }).Filter(orm.Q("name", "Created with labels")).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	createdRecord, _ := models.Bind(created)
	createdRows, err := (orm.RelationManager{Store: store, Source: createdRecord, Name: "labels"}).All(ctx)
	if err != nil || len(createdRows) != 2 {
		t.Fatal("parent/relations create was incomplete", createdRows, err)
	}
	for _, mode := range []string{"primary key", "tenant"} {
		store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
			if value, ok := event.Model.(*adminArticle); ok {
				if mode == "primary key" {
					value.ID = created.ID
				} else {
					value.Tenant = "two"
				}
			}
			return nil
		}}
		store.AfterSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
			if value, ok := event.Model.(*adminArticle); ok {
				value.ID, value.Tenant = article.ID, "one"
			}
			return nil
		}}
		get = request("GET", link[1], nil, nil)
		post = request("POST", link[1], postValues(get, "Hook must not escape", "1", "2"), get.Result().Cookies())
		if post.Code != 403 {
			t.Fatal(mode, "guard ignored hook retarget", post.Code, post.Body.String())
		}
		check("Updated", labels[0].ID, labels[1].ID, labels[2].ID)
		other, err := orm.For(store, func() *adminArticle { return &adminArticle{} }).Filter(orm.Q("id", created.ID)).Get(ctx)
		if err != nil || other.Name != "Created with labels" || other.Tenant != "one" {
			t.Fatal("hook changed another scoped object", other, err)
		}
	}
	store.BeforeSave, store.AfterSave = nil, nil
}
