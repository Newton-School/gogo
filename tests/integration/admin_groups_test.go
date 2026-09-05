package integration_test

import (
	"context"
	"encoding/base64"
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
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

func TestAdminGroupsUseScopedDomainCreationRenameAndAtomicAudit(t *testing.T) {
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
	allowCreate, allowRename, failAudit, retarget, nested := false, false, false, false, false
	var hiddenID string
	var adapter *admin.AccountStore
	var err error
	adapter, err = admin.NewAccountStore(admin.AccountStoreConfig{ORM: admin.ORMConfig{Store: store,
		QueryScope: func(_ context.Context, _ auth.Principal, schema models.Schema) (admin.QueryScope, error) {
			if schema.Key() != (&auth.Group{}).Schema().Key() {
				return admin.QueryScope{}, auth.ErrPermissionDenied
			}
			return admin.QueryScope{Predicate: orm.Q("name__startswith", "Visible"), Identity: "group-fixture"}, nil
		},
		ValidateWrite: func(ctx context.Context, _ auth.Principal, record models.Record) error {
			name, err := record.Get("name")
			if err != nil {
				return err
			}
			if !strings.HasPrefix(name.(string), "Visible") {
				return auth.ErrPermissionDenied
			}
			if retarget && record.State().Persisted {
				return record.Set("id", hiddenID)
			}
			if nested && name == "Visible nested" {
				nested = false
				id, _ := record.Get("id")
				return adapter.Accounts().RenameGroup(ctx, id.(string), "Visible callback")
			}
			return nil
		},
	}, Accounts: auth.AccountsConfig{Authorize: func(ctx context.Context, change auth.AccountChange) error {
		if auth.FromContext(ctx).ID == "bootstrap" {
			return nil
		}
		if change.Action == "create_group" && allowCreate {
			return nil
		}
		if change.Action == "rename_group" && allowRename {
			return nil
		}
		return auth.ErrPermissionDenied
	}}})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := auth.WithPrincipal(ctx, auth.Principal{ID: "bootstrap", Authenticated: true, Active: true})
	hidden, err := adapter.Accounts().CreateGroup(bootstrap, "Hidden group")
	if err != nil {
		t.Fatal(err)
	}
	hiddenID = hidden.ID
	member, err := adapter.Accounts().CreateUserWithoutPassword(bootstrap, "group-member", auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(secret)}, nil, "group-admin")
	if err != nil {
		t.Fatal(err)
	}
	site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.ModelPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Register(adapter.GroupAdmin()); err != nil {
		t.Fatal(err)
	}
	actor := auth.Principal{ID: "group-manager", Authenticated: true, Active: true, Staff: true, Permissions: []string{"gogo_auth.view_group", "gogo_auth.add_group", "gogo_auth.change_group", "gogo_auth.delete_group"}}
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
		if w.Code != 200 || !strings.Contains(w.Body.String(), `name="name"`) || strings.Contains(w.Body.String(), `name="id"`) {
			t.Fatal("group form failed", w.Code)
		}
		values := url.Values{"id": {hiddenID}, "permissions": {"999"}, "_continue": {"1"}}
		for _, name := range []string{"csrfmiddlewaretoken", "_edit_token"} {
			m := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`).FindStringSubmatch(w.Body.String())
			if len(m) != 2 {
				t.Fatal("missing group token", name)
			}
			values.Set(name, html.UnescapeString(m[1]))
		}
		return w, values
	}
	add := "/admin/gogo_auth/group/add/"
	w, values := form(add)
	values.Set("name", "Visible team")
	response := request("POST", add, values, w.Result().Cookies())
	if response.Code != 403 {
		t.Fatal("ordinary model add bypassed group authority", response.Code)
	}
	allowCreate = true
	response = request("POST", add, values, w.Result().Cookies())
	if response.Code != 303 || response.Header().Get("X-Gogo-Account-Change") != "changed" {
		t.Fatal("authorized group creation failed", response.Code, response.Body.String())
	}
	path := response.Header().Get("Location")
	group, err := orm.For(store, func() *auth.Group { return &auth.Group{} }).Filter(orm.Q("name", "Visible team")).Get(ctx)
	if err != nil || group.ID == hiddenID {
		t.Fatal("posted identity replaced group", err)
	}
	if err := adapter.Accounts().SetUserGroups(bootstrap, member.ID, []string{group.ID}); err != nil {
		t.Fatal(err)
	}
	key := base64.RawURLEncoding.EncodeToString([]byte("[" + strconv.Quote(group.ID) + "]"))
	check := func(name string, version uint64, audits int) {
		t.Helper()
		current, err := orm.For(store, func() *auth.Group { return &auth.Group{} }).Filter(orm.Q("id", group.ID)).Get(ctx)
		if err != nil || current.Name != name {
			t.Fatal("group name changed unexpectedly", err)
		}
		p, err := adapter.Accounts().LoadPrincipal(ctx, member.ID)
		if err != nil || p.AuthVersion != version {
			t.Fatal("group member invalidation not atomic", err, p.AuthVersion)
		}
		scoped, err := adapter.Scope(ctx, actor, "admin", group.Schema())
		if err != nil {
			t.Fatal(err)
		}
		logs, err := scoped.History(ctx, key, 0, 100)
		if err != nil || len(logs) != audits {
			t.Fatal("group audit not atomic", len(logs), err)
		}
	}
	check("Visible team", 2, 1)
	w, values = form(path)
	values.Set("name", "Visible renamed")
	response = request("POST", path, values, w.Result().Cookies())
	if response.Code != 403 {
		t.Fatal("ordinary model change bypassed rename authority", response.Code)
	}
	allowRename = true
	response = request("POST", path, values, w.Result().Cookies())
	if response.Code != 303 {
		t.Fatal("authorized group rename failed", response.Code, response.Body.String())
	}
	check("Visible renamed", 3, 2)
	if response = request("POST", path, values, w.Result().Cookies()); response.Code != 409 {
		t.Fatal("stale group token accepted", response.Code)
	}
	store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if failAudit && event.Record.Schema().Key() == admin.LogSchema().Key() {
			return errors.New("synthetic audit failure")
		}
		return nil
	}}
	failAudit = true
	w, values = form(path)
	values.Set("name", "Visible rollback")
	response = request("POST", path, values, w.Result().Cookies())
	if response.Code != 503 {
		t.Fatal("group audit error misreported", response.Code)
	}
	failAudit = false
	store.BeforeSave = nil
	check("Visible renamed", 3, 2)
	retarget = true
	w, values = form(path)
	values.Set("name", "Visible retargeted")
	response = request("POST", path, values, w.Result().Cookies())
	if response.Code != 403 {
		t.Fatal("group validator retargeted hidden group", response.Code)
	}
	retarget = false
	check("Visible renamed", 3, 2)
	// A proposed-state validator cannot perform a nested rename and have the
	// outer domain operation overwrite it from an already-stale snapshot.
	nested = true
	w, values = form(path)
	values.Set("name", "Visible nested")
	response = request("POST", path, values, w.Result().Cookies())
	if response.Code != 409 || nested {
		t.Fatal("nested group callback returned stale success", response.Code)
	}
	check("Visible renamed", 3, 2)
	w, values = form(add)
	values.Set("name", "Outside scope")
	response = request("POST", add, values, w.Result().Cookies())
	if response.Code != 403 {
		t.Fatal("out-of-scope group created", response.Code)
	}
	hiddenKey := base64.RawURLEncoding.EncodeToString([]byte("[" + strconv.Quote(hidden.ID) + "]"))
	if response = request("GET", "/admin/gogo_auth/group/"+hiddenKey+"/change/", nil, nil); response.Code != 404 {
		t.Fatal("hidden group exposed", response.Code)
	}
	if response = request("POST", strings.TrimSuffix(path, "change/")+"delete/", nil, nil); response.Code != 403 {
		t.Fatal("generic account deletion exposed", response.Code)
	}
	w, values = form(path)
	values.Set("name", "Visible unknown")
	commitBackend.unknown = true
	response = request("POST", path, values, w.Result().Cookies())
	commitBackend.unknown = false
	if response.Code != 503 || response.Header().Get("X-Gogo-Account-Change") != "unknown" || response.Header().Get("Location") != "" {
		t.Fatal("unknown group commit misreported", response.Code)
	}
	check("Visible unknown", 4, 3)
}
