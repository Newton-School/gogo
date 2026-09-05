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
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

type adminCredentialFailDelete struct {
	record  sessions.Record
	deletes int
}

type adminCredentialCommitBackend struct {
	db.Backend
	unknown bool
}

func (b *adminCredentialCommitBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &adminCredentialCommitTx{Transaction: tx, unknown: b.unknown}, nil
}

type adminCredentialCommitTx struct {
	db.Transaction
	unknown bool
}

func (t *adminCredentialCommitTx) Commit() error {
	if err := t.Transaction.Commit(); err != nil {
		return err
	}
	if t.unknown {
		return &db.Error{Code: db.UnknownCommit, Message: "synthetic lost commit acknowledgement"}
	}
	return nil
}

func (s *adminCredentialFailDelete) Load(_ context.Context, id string) (sessions.Record, error) {
	if id != s.record.ID {
		return sessions.Record{}, sessions.ErrNotFound
	}
	return sessions.Clone(s.record), nil
}
func (*adminCredentialFailDelete) Create(context.Context, sessions.Record) error {
	return errors.New("unexpected session create")
}
func (*adminCredentialFailDelete) Save(context.Context, sessions.Record, uint64) error {
	return errors.New("unexpected session save")
}
func (s *adminCredentialFailDelete) Delete(context.Context, string) error {
	s.deletes++
	return errors.New("synthetic session deletion failure")
}

func TestAdminUserCreationAndPasswordRequireScopedAuthorityAndAtomicAudit(t *testing.T) {
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
	var targetID, hiddenID string
	retargetWrite, retargetCreation, allowHiddenPassword := false, false, false
	authorityCalls := 0
	adapter, err := admin.NewAccountStore(admin.AccountStoreConfig{
		ORM: admin.ORMConfig{Store: store, QueryScope: func(context.Context, auth.Principal, models.Schema) (admin.QueryScope, error) {
			return admin.QueryScope{Predicate: orm.Q("identifier__startswith", "visible-"), Identity: "visible-accounts"}, nil
		}, ValidateWrite: func(_ context.Context, _ auth.Principal, record models.Record) error {
			id, _ := record.Get("id")
			if record.State().Persisted && (retargetCreation || retargetWrite && id == targetID) {
				return record.Set("id", hiddenID)
			}
			return nil
		}},
		Accounts: auth.AccountsConfig{Authorize: func(ctx context.Context, change auth.AccountChange) error {
			p := auth.FromContext(ctx)
			if p.ID == "bootstrap" {
				return nil
			}
			authorityCalls++
			if !p.Authenticated || !p.Active {
				return auth.ErrPermissionDenied
			}
			if p.ID == "creator" && change.Action == "create_user" && change.Active && !change.Staff && !change.Superuser {
				return nil
			}
			if (p.ID == "password-manager" || p.ID == targetID) && change.Action == "change_password" && (change.UserID == targetID || allowHiddenPassword && change.UserID == hiddenID) {
				return nil
			}
			return auth.ErrPermissionDenied
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := auth.WithPrincipal(ctx, auth.Principal{ID: "bootstrap", Authenticated: true, Active: true})
	oldPassword, _ := security.RandomToken(24)
	target, err := adapter.Accounts().CreateUser(bootstrap, "visible-target", oldPassword, auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	targetID = target.ID
	hidden, err := adapter.Accounts().CreateUser(bootstrap, "hidden-target", oldPassword, auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hiddenID = hidden.ID
	secret, _ := security.RandomToken(32)
	signer, _ := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(secret)}, nil, "user-credential-admin")
	site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.ModelPolicy{}, LoginURL: "/admin/login/"})
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Register(adapter.UserAdmin()); err != nil {
		t.Fatal(err)
	}
	actor := func(id string) auth.Principal {
		return auth.Principal{ID: id, Authenticated: true, Active: true, Staff: true, Permissions: []string{"gogo_auth.view_user", "gogo_auth.add_user", "gogo_auth.change_user"}}
	}
	var handler http.Handler = site
	request := func(p auth.Principal, method, path string, values url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(values.Encode()))
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	key := func(id string) string {
		encoded, _ := json.Marshal([]string{id})
		return base64.RawURLEncoding.EncodeToString(encoded)
	}
	passwordPath := "/admin/gogo_auth/user/" + key(targetID) + "/password/"
	get := func(p auth.Principal, path string) *httptest.ResponseRecorder {
		w := request(p, "GET", path, nil, nil)
		if w.Code != 200 || strings.Contains(w.Body.String(), oldPassword) || strings.Contains(w.Body.String(), *target.PasswordHash) {
			t.Fatal("unsafe credential form", w.Code)
		}
		return w
	}
	values := func(w *httptest.ResponseRecorder, password string) url.Values {
		data := url.Values{"identifier": {"visible-new"}, "password_mode": {"set"}, "password1": {password}, "password2": {password}, "staff": {"on"}, "superuser": {"on"}, "password_hash": {"forged"}}
		for _, name := range []string{"csrfmiddlewaretoken", "_edit_token"} {
			match := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`).FindStringSubmatch(w.Body.String())
			if len(match) != 2 {
				t.Fatal("missing credential form token", name)
			}
			data.Set(name, match[1])
		}
		return data
	}
	newPassword := "  a new private credential value  "
	createPath := "/admin/gogo_auth/user/add/"
	for _, who := range []string{"ordinary", "creator"} {
		page := get(actor(who), createPath)
		data := values(page, newPassword)
		if response := request(actor(who), "POST", createPath, data, nil); response.Code != 403 {
			t.Fatal("CSRF skipped", response.Code)
		}
		response := request(actor(who), "POST", createPath, data, page.Result().Cookies())
		want := 403
		if who == "creator" {
			want = 303
		}
		if response.Code != want || strings.Contains(response.Body.String(), newPassword) {
			t.Fatal("wrong create authority result", who, response.Code)
		}
	}
	created, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("identifier", "visible-new")).Get(ctx)
	if err != nil || created.Staff || created.Superuser || !created.Active || created.PasswordHash == nil {
		t.Fatal("creation accepted forged privileged fields", err)
	}
	if valid, _, err := auth.VerifyPassword(newPassword, *created.PasswordHash); err != nil || !valid {
		t.Fatal("created password differs")
	}
	addOnly := actor("creator")
	addOnly.Permissions = []string{"gogo_auth.add_user"}
	addPage := get(addOnly, createPath)
	addData := values(addPage, newPassword)
	addData.Set("identifier", "visible-add-only")
	if response := request(addOnly, "POST", createPath, addData, addPage.Result().Cookies()); response.Code != 303 || response.Header().Get("Location") != "/admin/" {
		t.Fatal("add-only actor redirected into a forbidden account view", response.Code)
	}
	// Out-of-scope creation and failed audit both roll back the account insert.
	for _, mode := range []string{"hidden-created", "visible-audit-failure"} {
		if strings.Contains(mode, "audit") {
			store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
				if event.Record.Schema().Key() == admin.LogSchema().Key() {
					return errors.New("synthetic audit failure")
				}
				return nil
			}}
		}
		page := get(actor("creator"), createPath)
		data := values(page, newPassword)
		data.Set("identifier", mode)
		response := request(actor("creator"), "POST", createPath, data, page.Result().Cookies())
		store.BeforeSave = nil
		if response.Code != 403 && response.Code != 503 {
			t.Fatal("create failure claimed success", mode, response.Code)
		}
		count, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("identifier", mode)).Count(ctx)
		if err != nil || count != 0 {
			t.Fatal("failed create persisted", err, count)
		}
	}
	page := get(actor("creator"), createPath)
	data := values(page, newPassword)
	data.Set("password2", "mismatch")
	if response := request(actor("creator"), "POST", createPath, data, page.Result().Cookies()); response.Code != 400 || strings.Contains(response.Body.String(), newPassword) {
		t.Fatal("invalid password pair echoed or accepted", response.Code)
	}
	if response := request(actor("password-manager"), "GET", "/admin/gogo_auth/user/"+key(hidden.ID)+"/password/", nil, nil); response.Code != 404 {
		t.Fatal("hidden credential target exposed", response.Code)
	}
	for _, mode := range []string{"ordinary", "audit-failure", "password-manager"} {
		p := actor(mode)
		if mode == "audit-failure" {
			p = actor("password-manager")
		}
		page := get(p, passwordPath)
		data := values(page, newPassword)
		if mode == "audit-failure" {
			store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
				if event.Record.Schema().Key() == admin.LogSchema().Key() {
					return errors.New("synthetic audit failure")
				}
				return nil
			}}
		}
		response := request(p, "POST", passwordPath, data, page.Result().Cookies())
		store.BeforeSave = nil
		want := 403
		if mode == "audit-failure" {
			want = 503
		}
		if mode == "password-manager" {
			want = 303
		}
		if response.Code != want || strings.Contains(response.Body.String(), newPassword) {
			t.Fatal("password authority/audit outcome wrong", mode, response.Code)
		}
		current, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", targetID)).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		wantPassword := oldPassword
		version := int64(1)
		if mode == "password-manager" {
			wantPassword = newPassword
			version = 2
		}
		if valid, _, err := auth.VerifyPassword(wantPassword, *current.PasswordHash); err != nil || !valid || current.AuthVersion != version {
			t.Fatal("password did not roll back/commit atomically", mode)
		}
		if mode == "password-manager" {
			if replay := request(p, "POST", passwordPath, data, page.Result().Cookies()); replay.Code != 409 {
				t.Fatal("stale password form replayed", replay.Code)
			}
		}
	}
	// A password edit is not proof for preserving one's current session.
	page = get(actor(targetID), passwordPath)
	data = values(page, "")
	data.Set("password_mode", "unusable")
	response := request(actor(targetID), "POST", passwordPath, data, page.Result().Cookies())
	if response.Code != 303 || response.Header().Get("Location") != "/admin/login/" || response.Header().Get("X-Gogo-Password-Change") != "changed" {
		t.Fatal("self admin password mutation promoted a session", response.Code)
	}
	rotated := false
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "gogo_csrf" {
			for _, before := range page.Result().Cookies() {
				if before.Name == cookie.Name {
					rotated = cookie.Value != before.Value
				}
			}
		}
	}
	if !rotated {
		t.Fatal("self password edit did not rotate CSRF state")
	}
	current, _ := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", targetID)).Get(ctx)
	if current.AuthVersion != 3 || auth.HasUsablePassword(*current.PasswordHash) {
		t.Fatal("unusable password transition missing")
	}
	scoped, err := adapter.Scope(ctx, actor("password-manager"), "admin", (&auth.User{}).Schema())
	if err != nil {
		t.Fatal(err)
	}
	audits, err := scoped.History(ctx, key(targetID), 0, 10)
	if err != nil || len(audits) != 2 {
		t.Fatal("credential audit count incorrect", err, len(audits))
	}
	encoded, _ := json.Marshal(audits)
	for _, secret := range []string{oldPassword, newPassword, *target.PasswordHash, *current.PasswordHash} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("credential material entered audit")
		}
	}
	// Session deletion may fail after the credential/audit transaction commits.
	// The response must still describe a durable password transition, never a
	// rejected credential that the client could safely repeat automatically.
	sessionID, _ := sessions.NewID()
	failingSessions := &adminCredentialFailDelete{record: sessions.Record{ID: sessionID, Version: 1, ExpiresAt: time.Now().Add(time.Hour)}}
	middleware, err := sessions.Middleware(sessions.MiddlewareConfig{Store: failingSessions, Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	handler = middleware(site)
	page = get(actor(targetID), passwordPath)
	data = values(page, newPassword)
	cookieValue, _ := signer.Sign([]byte(sessionID))
	cookies := append(page.Result().Cookies(), &http.Cookie{Name: "gogo_session", Value: cookieValue})
	response = request(actor(targetID), "POST", passwordPath, data, cookies)
	if response.Code != 503 || response.Header().Get("X-Gogo-Password-Change") != "changed" || failingSessions.deletes != 1 || strings.Contains(response.Body.String(), newPassword) {
		t.Fatal("session failure misreported durable credential transition", response.Code, failingSessions.deletes)
	}
	current, _ = orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", targetID)).Get(ctx)
	valid, _, verifyErr := auth.VerifyPassword(newPassword, *current.PasswordHash)
	if !valid || verifyErr != nil || current.AuthVersion != 4 {
		t.Fatal("session failure undid committed credential")
	}
	audits, err = scoped.History(ctx, key(targetID), 0, 10)
	if err != nil || len(audits) != 3 {
		t.Fatal("session failure lost durable audit", err)
	}
	handler = site
	// A mutable ValidateWrite projection cannot substitute a hidden target,
	// even when the domain policy itself permits changing either identity.
	retargetWrite, allowHiddenPassword = true, true
	page = get(actor("password-manager"), passwordPath)
	data = values(page, "a malicious redirected credential")
	response = request(actor("password-manager"), "POST", passwordPath, data, page.Result().Cookies())
	retargetWrite, allowHiddenPassword = false, false
	if response.Code != 403 {
		t.Fatal("ValidateWrite retargeted a password mutation", response.Code)
	}
	for id, password := range map[string]string{targetID: newPassword, hiddenID: oldPassword} {
		user, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", id)).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if valid, _, err := auth.VerifyPassword(password, *user.PasswordHash); err != nil || !valid {
			t.Fatal("retarget callback changed an account credential")
		}
	}
	retargetCreation = true
	page = get(actor("creator"), createPath)
	data = values(page, newPassword)
	data.Set("identifier", "visible-retargeted-create")
	response = request(actor("creator"), "POST", createPath, data, page.Result().Cookies())
	retargetCreation = false
	if response.Code != 403 {
		t.Fatal("ValidateWrite retargeted a create projection", response.Code)
	}
	count, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("identifier", "visible-retargeted-create")).Count(ctx)
	if err != nil || count != 0 {
		t.Fatal("invalid create projection escaped transaction", err)
	}
	// Simulate losing the acknowledgement after PostgreSQL committed. Neither
	// creation nor credential replacement may return a success redirect or
	// internally retry the operation when the durable outcome is unknown.
	page = get(actor("creator"), createPath)
	data = values(page, newPassword)
	data.Set("identifier", "visible-unknown-create")
	commitBackend.unknown = true
	response = request(actor("creator"), "POST", createPath, data, page.Result().Cookies())
	commitBackend.unknown = false
	if response.Code != 503 || response.Header().Get("Location") != "" || !strings.Contains(response.Body.String(), "Do not repeat automatically") {
		t.Fatal("unknown creation was claimed as success", response.Code)
	}
	count, err = orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("identifier", "visible-unknown-create")).Count(ctx)
	if err != nil || count != 1 {
		t.Fatal("unknown create retried or rolled back unexpectedly", err, count)
	}
	page = get(actor(targetID), passwordPath)
	data = values(page, newPassword+"-unknown")
	commitBackend.unknown = true
	response = request(actor(targetID), "POST", passwordPath, data, page.Result().Cookies())
	commitBackend.unknown = false
	if response.Code != 503 || response.Header().Get("Location") != "" || response.Header().Get("X-Gogo-Password-Change") != "unknown" {
		t.Fatal("unknown credential outcome misclassified", response.Code)
	}
	current, _ = orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", targetID)).Get(ctx)
	valid, _, verifyErr = auth.VerifyPassword(newPassword+"-unknown", *current.PasswordHash)
	if !valid || verifyErr != nil || current.AuthVersion != 5 {
		t.Fatal("unknown credential operation retried or lost")
	}
	if authorityCalls == 0 {
		t.Fatal("account authority never consulted")
	}
	if _, err := scoped.(admin.UserEditor).ChangeUserPassword(ctx, admin.Object{}, newPassword); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("credential edit escaped caller transaction", err)
	}
	// Admin's prospective scope check must not feed a normalized value through
	// the domain normalizer a second time. A namespacing normalizer is valid
	// even though applying it twice produces a different identity.
	var prospectiveIdentifier any
	normalizedAdapter, err := admin.NewAccountStore(admin.AccountStoreConfig{
		ORM: admin.ORMConfig{Store: store, QueryScope: func(context.Context, auth.Principal, models.Schema) (admin.QueryScope, error) {
			return admin.QueryScope{Predicate: orm.Q("identifier__startswith", "visible-"), Identity: "normalized-accounts"}, nil
		}, ValidateWrite: func(_ context.Context, _ auth.Principal, record models.Record) error {
			if !record.State().Persisted {
				prospectiveIdentifier, _ = record.Get("identifier")
			}
			return nil
		}}, Accounts: auth.AccountsConfig{NormalizeIdentifier: func(raw string) (string, error) { return "visible-" + raw, nil }, Authorize: func(context.Context, auth.AccountChange) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := normalizedAdapter.Accounts().CreateUser(ctx, "direct-normalized", newPassword, auth.CreateUserOptions{})
	if err != nil || direct.Identifier != "visible-direct-normalized" {
		t.Fatal("domain normalizer fixture failed", err)
	}
	normalizedScope, err := normalizedAdapter.Scope(ctx, actor("creator"), "admin", (&auth.User{}).Schema())
	if err != nil {
		t.Fatal(err)
	}
	err = normalizedScope.Atomic(ctx, func(ctx context.Context) error {
		_, err := normalizedScope.(admin.UserEditor).CreateUser(ctx, "admin-normalized", newPassword, auth.CreateUserOptions{})
		return err
	})
	if err != nil || prospectiveIdentifier != "visible-admin-normalized" {
		t.Fatal("prospective normalized scope changed", err)
	}
	principal, _, err := normalizedAdapter.Accounts().Lookup(ctx, "admin-normalized")
	if err != nil || !principal.Authenticated {
		t.Fatal("Admin-created identity cannot use ordinary login normalization", err)
	}
}
