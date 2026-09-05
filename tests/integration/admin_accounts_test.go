package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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

func TestAdminAccountFlagsRequireExactAuthorityAndAtomicAudit(t *testing.T) {
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
	store := orm.New(backend, registry)
	runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: append(contenttypes.Migrations(), auth.Migrations()...)}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := backend.SchemaEditor().CreateModel(ctx, backend, admin.LogSchema()); err != nil {
		t.Fatal(err)
	}
	var targetID string
	authorityCalls := 0
	adapter, err := admin.NewAccountStore(admin.AccountStoreConfig{ORM: admin.ORMConfig{Store: store, QueryScope: func(_ context.Context, _ auth.Principal, schema models.Schema) (admin.QueryScope, error) {
		if schema.Key() != (&auth.User{}).Schema().Key() {
			return admin.QueryScope{}, auth.ErrPermissionDenied
		}
		return admin.QueryScope{Predicate: orm.Q("id", targetID), Identity: "managed-account"}, nil
	}, ValidateWrite: func(_ context.Context, _ auth.Principal, record models.Record) error {
		id, err := record.Get("id")
		if err != nil || id != targetID {
			return auth.ErrPermissionDenied
		}
		return nil
	}}, Accounts: auth.AccountsConfig{Authorize: func(ctx context.Context, change auth.AccountChange) error {
		actor := auth.FromContext(ctx)
		if actor.ID == "fixture-bootstrap" {
			return nil
		}
		if change.Action == "change_account_flags" {
			authorityCalls++
			if actor.ID == "flag-manager" && actor.Authenticated && actor.Active && change.UserID == targetID && !change.Superuser {
				return nil
			}
		}
		return auth.ErrPermissionDenied
	}}})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := auth.WithPrincipal(ctx, auth.Principal{ID: "fixture-bootstrap", Authenticated: true, Active: true})
	password, _ := security.RandomToken(24)
	target, err := adapter.Accounts().CreateUser(bootstrap, "managed-user", password, auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	targetID = target.ID
	hidden, err := adapter.Accounts().CreateUser(bootstrap, "hidden-user", password, auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := security.RandomToken(32)
	signer, _ := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(secret)}, nil, "account-admin")
	site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.ModelPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Register(adapter.UserAdmin()); err != nil {
		t.Fatal(err)
	}
	manager := auth.Principal{ID: "flag-manager", Authenticated: true, Active: true, Staff: true, Permissions: []string{"gogo_auth.view_user", "gogo_auth.change_user", "gogo_auth.add_user", "gogo_auth.delete_user"}}
	ordinary := manager
	ordinary.ID = "ordinary-admin"
	request := func(p auth.Principal, method, path string, values url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(values.Encode()))
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		site.ServeHTTP(w, r)
		return w
	}
	list := request(manager, "GET", "/admin/gogo_auth/user/", nil, nil)
	link := regexp.MustCompile(`href="(/admin/gogo_auth/user/[^"]+/change/)"`).FindStringSubmatch(list.Body.String())
	if list.Code != 200 || len(link) != 2 || strings.Contains(list.Body.String(), hidden.Identifier) {
		t.Fatal("scoped account list failed", list.Code)
	}
	get := func(p auth.Principal) *httptest.ResponseRecorder {
		form := request(p, "GET", link[1], nil, nil)
		if form.Code != 200 || strings.Contains(form.Body.String(), *target.PasswordHash) || strings.Contains(form.Body.String(), password) || strings.Contains(form.Body.String(), `name="identifier"`) || strings.Contains(form.Body.String(), `name="password_hash"`) {
			t.Fatal("unsafe account form", form.Code)
		}
		return form
	}
	valuesFor := func(form *httptest.ResponseRecorder) url.Values {
		t.Helper()
		values := url.Values{"active": {"on"}, "staff": {"on"}, "identifier": {"forged-identifier"}, "password_hash": {"forged-credential"}, "auth_version": {"999"}}
		for _, name := range []string{"csrfmiddlewaretoken", "_edit_token"} {
			match := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(form.Body.String())
			if len(match) != 2 {
				t.Fatal("missing account form token", name)
			}
			values.Set(name, match[1])
		}
		return values
	}
	check := func(version uint64, staff bool, audits int) {
		t.Helper()
		principal, err := adapter.Accounts().LoadPrincipal(ctx, targetID)
		if err != nil || principal.AuthVersion != version || principal.Staff != staff || principal.Superuser || !principal.Active {
			t.Fatal("account state/version changed unexpectedly", err)
		}
		user, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", targetID)).Get(ctx)
		if err != nil || user.Identifier != target.Identifier || user.PasswordHash == nil || *user.PasswordHash != *target.PasswordHash {
			t.Fatal("protected identity/credential mutated", err)
		}
		scoped, err := adapter.Scope(ctx, manager, "admin", target.Schema())
		if err != nil {
			t.Fatal(err)
		}
		rows, err := scoped.List(ctx, admin.ListQuery{Limit: 10})
		if err != nil || len(rows.Objects) != 1 {
			t.Fatal("scoped account read failed", err)
		}
		value, err := rows.Objects[0].Record.Get("password_hash")
		if err != nil || value != nil {
			t.Fatal("credential entered display record")
		}
		logs, err := scoped.History(ctx, rows.Objects[0].ID, 0, 10)
		if err != nil || len(logs) != audits {
			t.Fatal("account audit not atomic", err)
		}
		encoded, err := json.Marshal(logs)
		if err != nil || strings.Contains(string(encoded), "password_hash") || strings.Contains(string(encoded), *target.PasswordHash) || strings.Contains(string(encoded), "forged-") {
			t.Fatal("credential or ignored POST entered audit")
		}
	}
	form := get(ordinary)
	denied := request(ordinary, "POST", link[1], valuesFor(form), form.Result().Cookies())
	if denied.Code != 403 {
		t.Fatal("ordinary user-change grant permitted privileged flag mutation", denied.Code)
	}
	check(1, false, 0)
	form = get(manager)
	values := valuesFor(form)
	saved := request(manager, "POST", link[1], values, form.Result().Cookies())
	if saved.Code != 303 || authorityCalls < 3 {
		t.Fatal("authorized account flags not committed", saved.Code, saved.Body.String())
	}
	check(2, true, 1)
	stale := request(manager, "POST", link[1], values, form.Result().Cookies())
	if stale.Code != 409 {
		t.Fatal("stale account edit was not rejected", stale.Code)
	}
	form = get(manager)
	values = valuesFor(form)
	values.Set("superuser", "on")
	denied = request(manager, "POST", link[1], values, form.Result().Cookies())
	if denied.Code != 403 {
		t.Fatal("limited flag authority granted superuser", denied.Code)
	}
	check(2, true, 1)
	store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if event.Record.Schema().Key() == admin.LogSchema().Key() {
			return errors.New("synthetic audit outage")
		}
		return nil
	}}
	form = get(manager)
	values = valuesFor(form)
	values.Del("staff")
	failed := request(manager, "POST", link[1], values, form.Result().Cookies())
	store.BeforeSave = nil
	if failed.Code != 503 || strings.Contains(failed.Body.String(), "synthetic") {
		t.Fatal("account audit failure was not safe", failed.Code)
	}
	check(2, true, 1)
	store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if user, ok := event.Model.(*auth.User); ok {
			user.Superuser = true
		}
		return nil
	}}
	store.AfterSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if user, ok := event.Model.(*auth.User); ok {
			user.Superuser = false
		}
		return nil
	}}
	form = get(manager)
	values = valuesFor(form)
	values.Del("staff")
	denied = request(manager, "POST", link[1], values, form.Result().Cookies())
	store.BeforeSave, store.AfterSave = nil, nil
	if denied.Code != 403 {
		t.Fatal("account save hooks bypassed exact flag authority", denied.Code)
	}
	check(2, true, 1)
	scoped, err := adapter.Scope(ctx, manager, "admin", target.Schema())
	if err != nil {
		t.Fatal(err)
	}
	page, err := scoped.List(ctx, admin.ListQuery{Limit: 1})
	if err != nil || len(page.Objects) != 1 {
		t.Fatal("missing scoped user", err)
	}
	for _, field := range []struct {
		name  string
		value any
	}{{"id", hidden.ID}, {"identifier", "direct-forgery"}, {"password_hash", "direct-credential"}, {"auth_version", int64(999)}} {
		err := scoped.Atomic(ctx, func(ctx context.Context) error {
			object, err := scoped.Get(ctx, page.Objects[0].ID, true)
			if err != nil {
				return err
			}
			if err := object.Record.Set(field.name, field.value); err != nil {
				return err
			}
			_, err = scoped.Save(ctx, object)
			return err
		})
		if !errors.Is(err, auth.ErrPermissionDenied) {
			t.Fatal("direct account store accepted protected field", field.name, err)
		}
	}
	ordinaryScope, err := adapter.Scope(ctx, ordinary, "admin", target.Schema())
	if err != nil {
		t.Fatal(err)
	}
	err = ordinaryScope.Atomic(bootstrap, func(ctx context.Context) error {
		object, err := ordinaryScope.Get(ctx, page.Objects[0].ID, true)
		if err != nil {
			return err
		}
		if err := object.Record.Set("staff", false); err != nil {
			return err
		}
		_, err = ordinaryScope.Save(ctx, object)
		return err
	})
	if !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("unrelated privileged context widened scoped account authority", err)
	}
	restricted, err := auth.ConstrainPrincipal(manager, []string{"gogo_auth.view_user"})
	if err != nil {
		t.Fatal(err)
	}
	restrictedScope, err := adapter.Scope(ctx, restricted, "admin", target.Schema())
	if err != nil {
		t.Fatal(err)
	}
	err = restrictedScope.Atomic(bootstrap, func(ctx context.Context) error {
		object, err := restrictedScope.Get(ctx, page.Objects[0].ID, true)
		if err != nil {
			return err
		}
		if err := object.Record.Set("staff", false); err != nil {
			return err
		}
		_, err = restrictedScope.Save(ctx, object)
		return err
	})
	if !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("account store discarded the scoped principal's token ceiling", err)
	}
	check(2, true, 1)
	for _, path := range []string{"/admin/gogo_auth/user/add/", strings.TrimSuffix(link[1], "change/") + "delete/"} {
		if response := request(manager, "GET", path, nil, nil); response.Code != 403 {
			t.Fatal("unsupported account mutation exposed", response.Code)
		}
	}
	encodedID, _ := json.Marshal([]string{hidden.ID})
	hiddenPath := "/admin/gogo_auth/user/" + base64.RawURLEncoding.EncodeToString(encodedID) + "/change/"
	if response := request(manager, "GET", hiddenPath, nil, nil); response.Code != 404 {
		t.Fatal("out-of-scope account was visible", response.Code)
	}
}
