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
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
)

func TestAdminUserIdentifierUsesExactDeltaAuthorityAndAtomicAudit(t *testing.T) {
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
	deny, mutateProjection, failAudit, nestedProjection := false, false, false, false
	proposedProjection := false
	identityCalls, flagCalls := 0, 0
	var adapter *admin.AccountStore
	var err error
	adapter, err = admin.NewAccountStore(admin.AccountStoreConfig{
		ORM: admin.ORMConfig{Store: store,
			QueryScope: func(_ context.Context, _ auth.Principal, schema models.Schema) (admin.QueryScope, error) {
				if schema.Key() != (&auth.User{}).Schema().Key() {
					return admin.QueryScope{}, auth.ErrPermissionDenied
				}
				return admin.QueryScope{Predicate: orm.Q("id", targetID), Identity: "identity-fixture"}, nil
			},
			ValidateWrite: func(ctx context.Context, _ auth.Principal, record models.Record) error {
				id, err := record.Get("id")
				if err != nil || id != targetID {
					return auth.ErrPermissionDenied
				}
				if mutateProjection {
					return record.Set("id", hiddenID)
				}
				identifier, err := record.Get("identifier")
				if err != nil {
					return err
				}
				if nestedProjection && identifier == "namespace-late-policy" {
					nestedProjection = false
					return adapter.Accounts().ChangeIdentifier(ctx, targetID, "nested-policy")
				}
				if proposedProjection && identifier == "early-policy" {
					proposedProjection = false
					return adapter.Accounts().ChangeIdentifier(ctx, targetID, "nested-policy")
				}
				return nil
			},
		},
		Accounts: auth.AccountsConfig{
			NormalizeIdentifier: func(value string) (string, error) { return "namespace-" + value, nil },
			Authorize: func(ctx context.Context, change auth.AccountChange) error {
				if auth.FromContext(ctx).ID == "bootstrap" {
					return nil
				}
				if change.Action == "change_account_flags" {
					flagCalls++
					return auth.ErrPermissionDenied
				}
				if change.Action == "change_identifier" {
					identityCalls++
					if !deny && change.UserID == targetID && strings.HasPrefix(change.Name, "namespace-") {
						return nil
					}
				}
				return auth.ErrPermissionDenied
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := auth.WithPrincipal(ctx, auth.Principal{ID: "bootstrap", Authenticated: true, Active: true})
	target, err := adapter.Accounts().CreateUserWithoutPassword(bootstrap, "initial", auth.CreateUserOptions{Staff: true})
	if err != nil {
		t.Fatal(err)
	}
	targetID = target.ID
	hidden, err := adapter.Accounts().CreateUserWithoutPassword(bootstrap, "hidden", auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hiddenID = hidden.ID
	secret, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(secret)}, nil, "identity-admin")
	if err != nil {
		t.Fatal(err)
	}
	site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.ModelPolicy{}, LoginURL: "/login/"})
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Register(adapter.UserAdmin()); err != nil {
		t.Fatal(err)
	}
	actor := auth.Principal{ID: "identity-manager", Authenticated: true, Active: true, Staff: true, Permissions: []string{"gogo_auth.view_user", "gogo_auth.change_user"}}
	key := base64.RawURLEncoding.EncodeToString([]byte(`[` + strconv.Quote(targetID) + `]`))
	path := "/admin/gogo_auth/user/" + key + "/change/"
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
	form := func(p auth.Principal) (*httptest.ResponseRecorder, url.Values) {
		t.Helper()
		w := request(p, "GET", path, nil, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `name="identifier"`) || strings.Contains(w.Body.String(), `name="password_hash"`) {
			t.Fatal("unsafe identifier form", w.Code)
		}
		values := url.Values{"active": {"on"}, "staff": {"on"}, "_continue": {"1"}}
		for _, name := range []string{"identifier", "csrfmiddlewaretoken", "_edit_token"} {
			match := regexp.MustCompile(`name="` + name + `"[^>]*value="([^"]*)"`).FindStringSubmatch(w.Body.String())
			if len(match) != 2 {
				t.Fatal("missing form field", name)
			}
			values.Set(name, html.UnescapeString(match[1]))
		}
		return w, values
	}
	check := func(identifier string, version int64, audits int) {
		t.Helper()
		row, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", targetID)).Get(ctx)
		if err != nil || row.Identifier != identifier || row.AuthVersion != version || !row.Staff || row.Superuser || *row.PasswordHash != *target.PasswordHash {
			t.Fatal("unexpected account effect", err)
		}
		scoped, err := adapter.Scope(ctx, actor, "admin", target.Schema())
		if err != nil {
			t.Fatal(err)
		}
		logs, err := scoped.History(ctx, key, 0, 100)
		if err != nil || len(logs) != audits {
			t.Fatal("identity audit not atomic", len(logs), err)
		}
		for _, log := range logs {
			if _, ok := log.Changes["password_hash"]; ok {
				t.Fatal("credential entered audit")
			}
		}
	}
	w, values := form(actor)
	values.Set("identifier", "renamed")
	deny = true
	response := request(actor, "POST", path, values, w.Result().Cookies())
	if response.Code != 403 {
		t.Fatal("ordinary model grant bypassed identity authority", response.Code)
	}
	check("namespace-initial", 1, 0)
	deny = false
	response = request(actor, "POST", path, values, w.Result().Cookies())
	if response.Code != 303 || response.Header().Get("X-Gogo-Account-Change") != "changed" || identityCalls < 3 || flagCalls != 0 {
		t.Fatal("identity-only edit required flag authority", response.Code, response.Body.String(), flagCalls)
	}
	check("namespace-renamed", 2, 1)
	if _, _, err := adapter.Accounts().Lookup(ctx, "renamed"); err != nil {
		t.Fatal("normalization applied more than once", err)
	}
	response = request(actor, "POST", path, values, w.Result().Cookies())
	if response.Code != 409 {
		t.Fatal("stale identity token accepted", response.Code)
	}
	w, values = form(actor)
	beforeCalls := identityCalls
	response = request(actor, "POST", path, values, w.Result().Cookies())
	if response.Code != 303 || response.Header().Get("X-Gogo-Account-Change") != "unchanged" || identityCalls != beforeCalls {
		t.Fatal("stored identity normalized on no-op", response.Code)
	}
	check("namespace-renamed", 2, 2)
	// A second unauthorized delta must roll back the preceding allowed rename.
	w, values = form(actor)
	values.Set("identifier", "both")
	values.Del("staff")
	response = request(actor, "POST", path, values, w.Result().Cookies())
	if response.Code != 403 || flagCalls == 0 {
		t.Fatal("combined delta authority skipped", response.Code)
	}
	check("namespace-renamed", 2, 2)
	store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if failAudit && event.Record.Schema().Key() == admin.LogSchema().Key() {
			return errors.New("synthetic audit failure")
		}
		return nil
	}}
	failAudit = true
	w, values = form(actor)
	values.Set("identifier", "rolled-back")
	response = request(actor, "POST", path, values, w.Result().Cookies())
	if response.Code != 503 || strings.Contains(response.Body.String(), "synthetic") {
		t.Fatal("audit failure unsafe", response.Code)
	}
	failAudit = false
	store.BeforeSave = nil
	check("namespace-renamed", 2, 2)
	mutateProjection = true
	w, values = form(actor)
	values.Set("identifier", "retargeted")
	response = request(actor, "POST", path, values, w.Result().Cookies())
	if response.Code != 403 {
		t.Fatal("scope validator retargeted account", response.Code)
	}
	mutateProjection = false
	check("namespace-renamed", 2, 2)
	nestedProjection = true
	w, values = form(actor)
	values.Set("identifier", "late-policy")
	response = request(actor, "POST", path, values, w.Result().Cookies())
	if response.Code != 409 || nestedProjection {
		t.Fatal("scope callback nested mutation returned stale success", response.Code)
	}
	check("namespace-renamed", 2, 2)
	proposedProjection = true
	w, values = form(actor)
	values.Set("identifier", "early-policy")
	response = request(actor, "POST", path, values, w.Result().Cookies())
	if response.Code != 409 || proposedProjection {
		t.Fatal("proposed-state callback replaced prior account snapshot", response.Code)
	}
	check("namespace-renamed", 2, 2)
	row, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", hiddenID)).Get(ctx)
	if err != nil || row.Identifier != "namespace-hidden" || row.AuthVersion != 1 {
		t.Fatal("hidden account changed", err)
	}
	// A self identity edit clears the current principal and rotates CSRF even
	// without session middleware, then returns to the explicit login route.
	self := actor
	self.ID = targetID
	w, values = form(self)
	values.Set("identifier", "self-renamed")
	response = request(self, "POST", path, values, w.Result().Cookies())
	if response.Code != 303 || response.Header().Get("Location") != "/login/" || response.Header().Get("X-Gogo-Account-Change") != "changed" {
		t.Fatal("self identity edit retained old login", response.Code)
	}
	rotated := false
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "gogo_csrf" {
			rotated = true
		}
	}
	if !rotated {
		t.Fatal("self edit did not rotate CSRF")
	}
	check("namespace-self-renamed", 3, 3)
	// Commit acknowledgement loss is not a rejected change and must never
	// cause an automatic retry or a success redirect under the old identity.
	w, values = form(self)
	values.Set("identifier", "unknown-outcome")
	commitBackend.unknown = true
	response = request(self, "POST", path, values, w.Result().Cookies())
	commitBackend.unknown = false
	if response.Code != 503 || response.Header().Get("X-Gogo-Account-Change") != "unknown" || response.Header().Get("Location") != "" {
		t.Fatal("unknown identity commit misreported", response.Code)
	}
	check("namespace-unknown-outcome", 4, 4)
	// A later session-store failure cannot relabel the completed account/audit
	// transaction as unchanged, even though the response requires recovery.
	sessionID, err := sessions.NewID()
	if err != nil {
		t.Fatal(err)
	}
	failingSessions := &adminCredentialFailDelete{record: sessions.Record{ID: sessionID, Version: 1, ExpiresAt: time.Now().Add(time.Hour)}}
	middleware, err := sessions.Middleware(sessions.MiddlewareConfig{Store: failingSessions, Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	handler = middleware(site)
	w, values = form(self)
	values.Set("identifier", "session-failed")
	cookieValue, err := signer.Sign([]byte(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	cookies := append(w.Result().Cookies(), &http.Cookie{Name: "gogo_session", Value: cookieValue})
	response = request(self, "POST", path, values, cookies)
	if response.Code != 503 || response.Header().Get("X-Gogo-Account-Change") != "changed" || failingSessions.deletes != 1 {
		t.Fatal("session failure mislabeled committed identifier change", response.Code, failingSessions.deletes)
	}
	check("namespace-session-failed", 5, 5)
}
