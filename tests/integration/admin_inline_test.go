package integration_test

import (
	"context"
	"encoding/json"
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

type adminNote struct {
	models.Base
	ID, ProductID int64
	Tenant, Body  string
}

func (*adminNote) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "Note", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.CharField("tenant", models.WithStructField("Tenant"), models.WithMaxLength(128), models.ReadOnly), models.ForeignKeyField("product", models.Relation{Target: "shop.Product", OnDelete: models.Cascade}, models.WithStructField("ProductID")), models.CharField("body", models.WithStructField("Body"), models.WithMaxLength(100))}}
}

type adminWatcher struct {
	models.Base
	ID        int64
	ProductID *int64
	Tenant    string
}

func (*adminWatcher) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "Watcher", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.CharField("tenant", models.WithStructField("Tenant"), models.WithMaxLength(128), models.ReadOnly), models.ForeignKeyField("product", models.Relation{Target: "shop.Product", OnDelete: models.SetNull}, models.WithStructField("ProductID"), models.Nullable)}}
}
func TestAdminInlineAtomicSaveAndOwnership(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx := context.Background()
	schemas := []models.Schema{(&adminProduct{}).Schema(), (&adminNote{}).Schema(), (&adminWatcher{}).Schema(), admin.LogSchema()}
	editor, err := backend.SchemaEditor().(db.SchemaResolverEditor).WithSchemas(schemas)
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range schemas {
		if err = editor.CreateModel(ctx, backend, schema); err != nil {
			t.Fatal(err)
		}
	}
	registry := &models.Registry{}
	for _, schema := range schemas {
		if err = registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err = registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, registry)
	parent := &adminProduct{Tenant: "one", Name: "Parent", Secret: "server"}
	if err = store.Save(ctx, parent, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	child := &adminNote{Tenant: "one", ProductID: parent.ID, Body: "Original"}
	if err = store.Save(ctx, child, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	watcher := &adminWatcher{Tenant: "one", ProductID: &parent.ID}
	if err = store.Save(ctx, watcher, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{"shop.Product": func() models.Model { return &adminProduct{} }, "shop.Note": func() models.Model { return &adminNote{} }, "shop.Watcher": func() models.Model { return &adminWatcher{} }}, QueryScope: func(_ context.Context, p auth.Principal, _ models.Schema) (admin.QueryScope, error) {
		return admin.QueryScope{Predicate: orm.Q("tenant", p.ID), Identity: p.ID}, nil
	}, ValidateWrite: func(_ context.Context, p auth.Principal, r models.Record) error {
		v, _ := r.Get("tenant")
		if v != p.ID {
			return auth.ErrPermissionDenied
		}
		return nil
	}, Initialize: func(_ context.Context, p auth.Principal, r models.Record) error { return r.Set("tenant", p.ID) }})
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := security.RandomToken(32)
	signer, _ := security.NewSigner(security.SigningKey{ID: "test", Value: []byte(secret)}, nil, "inline")
	denyWatcherChange := true
	denyNoteChange := false
	site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.PolicyFunc(func(_ context.Context, _ auth.Principal, action string, resource auth.Resource) error {
		if denyWatcherChange && resource.Model == "Watcher" && action == "change" {
			return auth.ErrPermissionDenied
		}
		if denyNoteChange && resource.Model == "Note" && action == "change" {
			return auth.ErrPermissionDenied
		}
		return nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	err = site.Register(admin.ModelAdmin{Schema: parent.Schema(), Fieldsets: []admin.Fieldset{{Name: "Details", Fields: []string{"name", "secret"}}}, ReadonlyFields: []string{"secret"}, ListDisplay: []string{"name"}, SearchFields: []string{"name"}, ConstraintChecker: store, Inlines: []admin.Inline{{Name: "notes", Schema: child.Schema(), FKName: "product", Fields: []string{"body"}, Maximum: 10, Extra: 1, ConstraintChecker: store}}})
	if err != nil {
		t.Fatal(err)
	}
	err = site.Register(admin.ModelAdmin{Schema: child.Schema(), Fields: []string{"product", "body"}, AutocompleteFields: []string{"product"}, ConstraintChecker: store, ResolveRelation: func(ctx context.Context, _ models.Field, ids []string) ([]any, error) {
		if len(ids) != 1 {
			return nil, auth.ErrPermissionDenied
		}
		id, err := strconv.ParseInt(ids[0], 10, 64)
		if err != nil {
			return nil, auth.ErrPermissionDenied
		}
		object, err := orm.For(store, func() *adminProduct { return &adminProduct{} }).Filter(orm.Q("id", id), orm.Q("tenant", auth.FromContext(ctx).ID)).Get(ctx)
		if err != nil {
			return nil, auth.ErrPermissionDenied
		}
		return []any{object.ID}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	p := auth.Principal{ID: "one", Authenticated: true, Active: true, Staff: true, Superuser: true}
	request := func(method, path string, values url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(values.Encode()))
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
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
		m := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(body)
		if len(m) != 2 {
			t.Fatalf("missing %s in %s", name, body)
		}
		return m[1]
	}
	hiddenParent := &adminProduct{Tenant: "two", Name: "Hidden parent", Secret: "private"}
	if err := store.Save(ctx, hiddenParent, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	lookup := request("GET", "/admin/autocomplete/?app_label=shop&model_name=note&field_name=product&term=Parent", nil, nil)
	var choices struct{ Results []struct{ ID, Text string } }
	if lookup.Code != 200 || json.Unmarshal(lookup.Body.Bytes(), &choices) != nil || len(choices.Results) != 1 || choices.Results[0].ID != strconv.FormatInt(parent.ID, 10) {
		t.Fatal(lookup.Code, lookup.Body.String())
	}
	lookupForm := request("GET", "/admin/shop/note/add/", nil, nil)
	if lookupForm.Code != 200 || !strings.Contains(lookupForm.Body.String(), "data-relation-url") {
		t.Fatal(lookupForm.Code, lookupForm.Body.String())
	}
	forged := url.Values{"product": {strconv.FormatInt(hiddenParent.ID, 10)}, "body": {"Must not create"}, "_edit_token": {hidden(lookupForm.Body.String(), "_edit_token")}, "csrfmiddlewaretoken": {hidden(lookupForm.Body.String(), "csrfmiddlewaretoken")}}
	denied := request("POST", "/admin/shop/note/add/", forged, lookupForm.Result().Cookies())
	if denied.Code != 400 || strings.Contains(denied.Body.String(), "Hidden parent") {
		t.Fatal(denied.Code, denied.Body.String())
	}
	if count, err := orm.For(store, func() *adminNote { return &adminNote{} }).Count(ctx); err != nil || count != 1 {
		t.Fatal("forged relation changed data", count, err)
	}
	list := request("GET", "/admin/shop/product/", nil, nil)
	link := regexp.MustCompile(`href="(/admin/shop/product/[^"]+/change/)"`).FindStringSubmatch(list.Body.String())
	if len(link) != 2 {
		t.Fatal(list.Body.String())
	}
	get := request("GET", link[1], nil, nil)
	if get.Code != 200 || !strings.Contains(get.Body.String(), "<legend>Details</legend>") {
		t.Fatal(get.Code, get.Body.String())
	}
	body := get.Body.String()
	values := url.Values{"name": {"Updated parent"}, "notes-TOTAL_FORMS": {"2"}, "notes-INITIAL_FORMS": {"1"}, "notes-0-body": {"Updated child"}, "notes-1-body": {"New child"}, "notes-0-_id": {hidden(body, "notes-0-_id")}, "notes-0-_edit_token": {hidden(body, "notes-0-_edit_token")}, "_edit_token": {hidden(body, "_edit_token")}, "csrfmiddlewaretoken": {hidden(body, "csrfmiddlewaretoken")}}
	post := request("POST", link[1], values, get.Result().Cookies())
	if post.Code != 303 {
		t.Fatal(post.Code, post.Body.String())
	}
	notes, err := orm.For(store, func() *adminNote { return &adminNote{} }).Filter(orm.Q("product", parent.ID)).All(ctx)
	if err != nil || len(notes) != 2 {
		t.Fatal(notes, err)
	}
	get = request("GET", link[1], nil, nil)
	body = get.Body.String()
	values = url.Values{"name": {"Must roll back"}, "notes-TOTAL_FORMS": {"3"}, "notes-INITIAL_FORMS": {"2"}, "notes-0-body": {""}, "notes-1-body": {"Retained"}, "notes-2-body": {""}, "_edit_token": {hidden(body, "_edit_token")}, "csrfmiddlewaretoken": {hidden(body, "csrfmiddlewaretoken")}}
	for _, i := range []string{"0", "1"} {
		values.Set("notes-"+i+"-_id", hidden(body, "notes-"+i+"-_id"))
		values.Set("notes-"+i+"-_edit_token", hidden(body, "notes-"+i+"-_edit_token"))
	}
	post = request("POST", link[1], values, get.Result().Cookies())
	if post.Code != 400 || !strings.Contains(post.Body.String(), "This field is required.") {
		t.Fatal(post.Code, post.Body.String())
	}
	parent, err = orm.For(store, func() *adminProduct { return &adminProduct{} }).Filter(orm.Q("id", parent.ID)).Get(ctx)
	if err != nil || parent.Name != "Updated parent" {
		t.Fatal("partial parent save", parent, err)
	}
	values.Set("notes-0-body", "valid")
	values.Set("notes-0-_id", "foreign")
	post = request("POST", link[1], values, get.Result().Cookies())
	if post.Code != 400 {
		t.Fatal("forged inline identity accepted", post.Code, post.Body.String())
	}
	denyNoteChange = true
	get = request("GET", link[1], nil, nil)
	body = get.Body.String()
	if get.Code != 200 || strings.Contains(body, `name="notes-0-body"`) {
		t.Fatal("view-only child exposed editable input", get.Code, body)
	}
	values = url.Values{"name": {"Parent with read-only notes"}, "notes-TOTAL_FORMS": {"3"}, "notes-INITIAL_FORMS": {"2"}, "notes-0-body": {"forged read-only"}, "notes-2-body": {""}, "_edit_token": {hidden(body, "_edit_token")}, "csrfmiddlewaretoken": {hidden(body, "csrfmiddlewaretoken")}}
	for _, i := range []string{"0", "1"} {
		values.Set("notes-"+i+"-_id", hidden(body, "notes-"+i+"-_id"))
		values.Set("notes-"+i+"-_edit_token", hidden(body, "notes-"+i+"-_edit_token"))
	}
	post = request("POST", link[1], values, get.Result().Cookies())
	if post.Code != 303 {
		t.Fatal("read-only inline blocked unrelated parent save", post.Code, post.Body.String())
	}
	notes, err = orm.For(store, func() *adminNote { return &adminNote{} }).Filter(orm.Q("body", "forged read-only")).All(ctx)
	if err != nil || len(notes) != 0 {
		t.Fatal("read-only inline forged write", notes, err)
	}
	deletePath := strings.TrimSuffix(link[1], "change/") + "delete/"
	get = request("GET", deletePath, nil, nil)
	if get.Code != 403 {
		t.Fatal("SET_NULL needs change permission", get.Code, get.Body.String())
	}
	denyWatcherChange = false
	get = request("GET", deletePath, nil, nil)
	if get.Code != 200 {
		t.Fatal(get.Code, get.Body.String())
	}
	post = request("POST", deletePath, url.Values{"_confirm": {"yes"}, "_edit_token": {hidden(get.Body.String(), "_edit_token")}, "csrfmiddlewaretoken": {hidden(get.Body.String(), "csrfmiddlewaretoken")}}, get.Result().Cookies())
	if post.Code != 303 {
		t.Fatal("delete graph", post.Code, post.Body.String())
	}
	count, err := orm.For(store, func() *adminNote { return &adminNote{} }).Count(ctx)
	if err != nil || count != 0 {
		t.Fatal("cascade did not execute", count, err)
	}
	watcher, err = orm.For(store, func() *adminWatcher { return &adminWatcher{} }).Get(ctx)
	if err != nil || watcher.ProductID != nil {
		t.Fatal("SET_NULL did not execute", watcher, err)
	}
}
