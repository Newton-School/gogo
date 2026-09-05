package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

func TestAdminStockGrantFormsEnforceVisibleTokensReadonlyAndAtomicAudit(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	registry := &models.Registry{}
	for _, schema := range append(auth.Schemas(), (&contenttypes.ContentType{}).Schema(), admin.LogSchema()) {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	commitBackend := &adminCredentialCommitBackend{Backend: backend}
	store := orm.New(commitBackend, registry)
	runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: append(contenttypes.Migrations(), auth.Migrations()...)}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := backend.SchemaEditor().CreateModel(ctx, backend, admin.LogSchema()); err != nil {
		t.Fatal(err)
	}
	if _, err := contenttypes.Sync(ctx, store, registry, nil); err != nil {
		t.Fatal(err)
	}
	if err := auth.SyncPermissions(ctx, store, registry, nil); err != nil {
		t.Fatal(err)
	}
	var targetID, policyHidden string
	denyDomain, readonly, failAudit := false, false, false
	adapter, err := admin.NewAccountStore(admin.AccountStoreConfig{ORM: admin.ORMConfig{Store: store,
		QueryScope: func(_ context.Context, _ auth.Principal, schema models.Schema) (admin.QueryScope, error) {
			switch schema.Key() {
			case (&auth.User{}).Schema().Key():
				return admin.QueryScope{Predicate: orm.Q("id", targetID), Identity: "form-user"}, nil
			case (&auth.Group{}).Schema().Key():
				return admin.QueryScope{Predicate: orm.Q("name__startswith", "Visible"), Identity: "form-groups"}, nil
			case (&auth.Permission{}).Schema().Key():
				return admin.QueryScope{Predicate: orm.Q("id__gt", 0), Identity: "form-permissions"}, nil
			}
			return admin.QueryScope{}, auth.ErrPermissionDenied
		}, ValidateWrite: func(context.Context, auth.Principal, models.Record) error { return nil },
	}, Accounts: auth.AccountsConfig{Authorize: func(ctx context.Context, change auth.AccountChange) error {
		if auth.FromContext(ctx).ID == "bootstrap" {
			return nil
		}
		if denyDomain {
			return auth.ErrPermissionDenied
		}
		if slices.Contains([]string{"set_user_groups", "set_user_permissions", "set_group_permissions", "create_group"}, change.Action) {
			return nil
		}
		return auth.ErrPermissionDenied
	}}})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := auth.WithPrincipal(ctx, auth.Principal{ID: "bootstrap", Authenticated: true, Active: true})
	var groups []*auth.Group
	for _, name := range []string{"Visible old", "Visible new", "Visible policy secret", "Hidden tenant secret"} {
		group, err := adapter.Accounts().CreateGroup(bootstrap, name)
		if err != nil {
			t.Fatal(err)
		}
		groups = append(groups, group)
	}
	policyHidden = groups[2].ID
	user, err := adapter.Accounts().CreateUserWithoutPassword(bootstrap, "selector-user", auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	targetID = user.ID
	if err := adapter.Accounts().SetUserGroups(bootstrap, user.ID, []string{groups[0].ID, groups[2].ID, groups[3].ID}); err != nil {
		t.Fatal(err)
	}
	permissions, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).OrderBy("id").Limit(2).All(ctx)
	if err != nil || len(permissions) != 2 {
		t.Fatal(err)
	}
	p0, p1 := strconv.FormatInt(permissions[0].ID, 10), strconv.FormatInt(permissions[1].ID, 10)
	secret, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(secret)}, nil, "grant-form")
	if err != nil {
		t.Fatal(err)
	}
	site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.ModelPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	userOptions := adapter.UserAdmin()
	userOptions.GetReadonlyFields = func(context.Context, admin.Object) []string {
		if readonly {
			return []string{"groups", "user_permissions"}
		}
		return nil
	}
	if err := site.Register(userOptions); err != nil {
		t.Fatal(err)
	}
	groupOptions := adapter.GroupAdmin()
	groupOptions.Authorize = func(_ context.Context, _ auth.Principal, action string, object admin.Object) error {
		if action == "delete" {
			return auth.ErrPermissionDenied
		}
		if action == "view" && object.Record != nil {
			id, _ := object.Record.Get("id")
			if id == policyHidden {
				return auth.ErrPermissionDenied
			}
		}
		return nil
	}
	if err := site.Register(groupOptions); err != nil {
		t.Fatal(err)
	}
	actor := auth.Principal{ID: "grant-manager", Authenticated: true, Active: true, Staff: true, Permissions: []string{"gogo_auth.view_user", "gogo_auth.change_user", "gogo_auth.view_group", "gogo_auth.change_group", "gogo_auth.add_group", "gogo_auth.view_permission"}}
	request := func(method, path string, values url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(values.Encode()))
		r = r.WithContext(auth.WithPrincipal(r.Context(), actor))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		site.ServeHTTP(w, r)
		return w
	}
	form := func(path string) (*httptest.ResponseRecorder, url.Values) {
		t.Helper()
		w := request("GET", path, nil, nil)
		if w.Code != 200 {
			t.Fatal("grant form GET failed", w.Code)
		}
		values := url.Values{"_continue": {"1"}}
		for _, name := range []string{"csrfmiddlewaretoken", "_edit_token", "_account_grants_token"} {
			m := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`).FindStringSubmatch(w.Body.String())
			if len(m) == 2 {
				values.Set(name, html.UnescapeString(m[1]))
			}
		}
		return w, values
	}
	userKey := base64.RawURLEncoding.EncodeToString([]byte(`["` + user.ID + `"]`))
	userPath := "/admin/gogo_auth/user/" + userKey + "/change/"
	page, values := form(userPath)
	if !strings.Contains(page.Body.String(), `name="groups"`) || !strings.Contains(page.Body.String(), `name="user_permissions"`) || values.Get("_account_grants_token") == "" {
		t.Fatal("stock selectors or signed visible snapshot missing")
	}
	for _, hidden := range groups[2:] {
		if strings.Contains(page.Body.String(), hidden.ID) || strings.Contains(page.Body.String(), hidden.Name) {
			t.Fatal("hidden group disclosed in form")
		}
	}
	values.Set("identifier", user.Identifier)
	values.Set("active", "on")
	values["groups"] = []string{groups[1].ID}
	values["user_permissions"] = []string{p0}
	denyDomain = true
	if denied := request("POST", userPath, values, page.Result().Cookies()); denied.Code != 403 || denied.Header().Get("X-Gogo-Account-Change") != "unchanged" {
		t.Fatal("model grant bypassed domain authority", denied.Code)
	}
	denyDomain = false
	store.BeforeSave = append(store.BeforeSave, func(_ context.Context, event orm.SaveEvent) error {
		if failAudit && event.Record.Schema().Key() == admin.LogSchema().Key() {
			return errors.New("synthetic audit failure")
		}
		return nil
	})
	failAudit = true
	if failed := request("POST", userPath, values, page.Result().Cookies()); failed.Code != 503 {
		t.Fatal("audit failure accepted", failed.Code)
	}
	failAudit = false
	if saved := request("POST", userPath, values, page.Result().Cookies()); saved.Code != 303 || saved.Header().Get("X-Gogo-Account-Change") != "changed" {
		t.Fatal("stock grant save failed", saved.Code)
	}
	links, err := orm.For(store, func() *auth.UserGroup { return &auth.UserGroup{} }).Filter(orm.Q("user_id", user.ID)).All(ctx)
	if err != nil || len(links) != 3 || !slices.ContainsFunc(links, func(link *auth.UserGroup) bool { return link.GroupID == groups[1].ID }) || slices.ContainsFunc(links, func(link *auth.UserGroup) bool { return link.GroupID == groups[0].ID }) {
		t.Fatal("visible groups not replaced with hidden links retained", err)
	}
	scoped, err := adapter.Scope(ctx, actor, "admin", user.Schema())
	if err != nil {
		t.Fatal(err)
	}
	logs, err := scoped.History(ctx, userKey, 0, 10)
	if err != nil || len(logs) != 1 {
		t.Fatal("atomic audit missing or rollback leaked audit", err)
	}
	auditJSON, err := json.Marshal(logs[0].Changes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(auditJSON), `"groups"`) || !strings.Contains(string(auditJSON), groups[1].ID) || !strings.Contains(string(auditJSON), `"user_permissions"`) {
		t.Fatal("visible grant audit missing")
	}
	for _, hidden := range groups[2:] {
		if strings.Contains(string(auditJSON), hidden.ID) || strings.Contains(string(auditJSON), hidden.Name) {
			t.Fatal("hidden retained grant disclosed in audit")
		}
	}
	if stale := request("POST", userPath, values, page.Result().Cookies()); stale.Code != 409 {
		t.Fatal("stale grant edit accepted", stale.Code)
	}
	readonly = true
	page, values = form(userPath)
	if strings.Contains(page.Body.String(), `name="groups"`) || strings.Contains(page.Body.String(), `name="user_permissions"`) || !strings.Contains(page.Body.String(), groups[1].Name) {
		t.Fatal("readonly grant presentation incorrect")
	}
	values.Set("identifier", user.Identifier)
	values.Set("active", "on")
	values["groups"] = []string{groups[0].ID}
	values["user_permissions"] = []string{p1}
	if saved := request("POST", userPath, values, page.Result().Cookies()); saved.Code != 303 || saved.Header().Get("X-Gogo-Account-Change") != "unchanged" {
		t.Fatal("readonly forged POST affected account", saved.Code)
	}
	links, err = orm.For(store, func() *auth.UserGroup { return &auth.UserGroup{} }).Filter(orm.Q("user_id", user.ID)).All(ctx)
	if err != nil || slices.ContainsFunc(links, func(link *auth.UserGroup) bool { return link.GroupID == groups[0].ID }) {
		t.Fatal("readonly forged group mutation", err)
	}
	readonly = false
	// Group names have no version bump when only permissions change. The
	// separate visible-grant token must still reject a concurrent edit.
	groupKey := base64.RawURLEncoding.EncodeToString([]byte(`["` + groups[1].ID + `"]`))
	groupPath := "/admin/gogo_auth/group/" + groupKey + "/change/"
	page, values = form(groupPath)
	values.Set("name", groups[1].Name)
	values["permissions"] = []string{p0}
	if err := adapter.Accounts().SetGroupPermissions(bootstrap, groups[1].ID, []int64{permissions[1].ID}); err != nil {
		t.Fatal(err)
	}
	if stale := request("POST", groupPath, values, page.Result().Cookies()); stale.Code != 409 {
		t.Fatal("concurrent group permission edit accepted", stale.Code)
	}
	page, values = form(groupPath)
	values.Set("name", groups[1].Name)
	values["permissions"] = []string{p0}
	if saved := request("POST", groupPath, values, page.Result().Cookies()); saved.Code != 303 || saved.Header().Get("X-Gogo-Account-Change") != "changed" {
		t.Fatal("group grant outcome not reported changed", saved.Code)
	}
	// A lost COMMIT acknowledgement must not redirect or invite automatic
	// repetition. The successful database effect remains inspectable.
	page, values = form(groupPath)
	values.Set("name", groups[1].Name)
	values["permissions"] = []string{p1}
	commitBackend.unknown = true
	if unknown := request("POST", groupPath, values, page.Result().Cookies()); unknown.Code != 503 || unknown.Header().Get("X-Gogo-Account-Change") != "unknown" || unknown.Header().Get("Location") != "" {
		t.Fatal("unknown grant commit advertised success", unknown.Code)
	}
	commitBackend.unknown = false
	currentLinks, err := orm.For(store, func() *auth.GroupPermission { return &auth.GroupPermission{} }).Filter(orm.Q("group_id", groups[1].ID)).All(ctx)
	if err != nil || len(currentLinks) != 1 || currentLinks[0].PermissionID != permissions[1].ID {
		t.Fatal("unknown acknowledgement lost durable group grant", err)
	}
	page, values = form("/admin/gogo_auth/group/add/")
	values.Set("name", "Visible created with permissions")
	values["permissions"] = []string{p0, p1}
	if created := request("POST", "/admin/gogo_auth/group/add/", values, page.Result().Cookies()); created.Code != 303 || created.Header().Get("X-Gogo-Account-Change") != "changed" {
		t.Fatal("group creation and permissions failed", created.Code)
	}
	created, err := orm.For(store, func() *auth.Group { return &auth.Group{} }).Filter(orm.Q("name", "Visible created with permissions")).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := orm.For(store, func() *auth.GroupPermission { return &auth.GroupPermission{} }).Filter(orm.Q("group_id", created.ID)).Count(ctx); err != nil || count != 2 {
		t.Fatal("new group grants not persisted", err)
	}
	actor.Permissions = []string{"gogo_auth.view_user", "gogo_auth.change_user"}
	page, values = form(userPath)
	if strings.Contains(page.Body.String(), `name="groups"`) || strings.Contains(page.Body.String(), `name="user_permissions"`) || values.Has("_account_grants_token") {
		t.Fatal("model-denied selectors shown")
	}
	values.Set("identifier", user.Identifier)
	values.Set("active", "on")
	values["groups"] = []string{groups[0].ID}
	if saved := request("POST", userPath, values, page.Result().Cookies()); saved.Code != 303 || saved.Header().Get("X-Gogo-Account-Change") != "unchanged" {
		t.Fatal("model-denied forged selector changed account", saved.Code)
	}
}
