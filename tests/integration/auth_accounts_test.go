package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestPostgresAccountsPermissionsAndCredentialInvalidation(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	registry := &models.Registry{}
	for _, schema := range append(auth.Schemas(), (&contenttypes.ContentType{}).Schema(), (&genericAsset{}).Schema()) {
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
	if _, err := contenttypes.Sync(ctx, store, registry, nil); err != nil {
		t.Fatal(err)
	}
	if err := auth.SyncPermissions(ctx, store, registry, map[string][]auth.PermissionDefinition{"catalog.Asset": {{Codename: "publish_asset", Name: "Can publish Asset"}}}); err != nil {
		t.Fatal(err)
	}
	if err := auth.SyncPermissions(ctx, store, registry, nil); err != nil {
		t.Fatal(err)
	}
	operatorCtx := auth.WithPrincipal(ctx, auth.Principal{ID: "fixture-operator", Authenticated: true, Active: true})
	accounts, err := auth.NewAccounts(auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, change auth.AccountChange) error {
		if auth.FromContext(ctx).ID != "fixture-operator" {
			return auth.ErrPermissionDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	readonly, err := auth.NewAccounts(auth.AccountsConfig{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readonly.CreateUser(operatorCtx, "unauthorized", "fixture password value", auth.CreateUserOptions{}); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("missing grant authority did not deny account creation", err)
	}
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
	if _, err := accounts.CreateUser(operatorCtx, "hook-escalated", "fixture hook password", auth.CreateUserOptions{}); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("paired hooks hid persisted privilege escalation", err)
	}
	store.BeforeSave, store.AfterSave = nil, nil
	if exists, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("identifier", "hook-escalated")).Exists(ctx); err != nil || exists {
		t.Fatal("hook escalation insert was not rolled back", err)
	}
	user, err := accounts.CreateUser(operatorCtx, "  fixture-user  ", "fixture original password", auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if user.Identifier != "fixture-user" || !user.Active || user.Staff || user.Superuser || user.AuthVersion != 1 || user.CreatedAt.IsZero() || user.UpdatedAt.IsZero() {
		t.Fatalf("incorrect new account defaults: id=%s active=%v version=%d", user.ID, user.Active, user.AuthVersion)
	}
	encoded, _ := json.Marshal(user)
	if strings.Contains(string(encoded), *user.PasswordHash) || strings.Contains(fmt.Sprintf("%+v %#v", user, user), *user.PasswordHash) {
		t.Fatal("account formatting disclosed a password hash")
	}
	if _, err := accounts.CreateUser(operatorCtx, "fixture-user", "fixture duplicate password", auth.CreateUserOptions{}); err == nil {
		t.Fatal("normalized identity uniqueness missing")
	}
	actor, err := auth.NewAuthenticator(accounts, 2)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := actor.Authenticate(ctx, " fixture-user ", "fixture original password")
	if err != nil || principal.ID != user.ID || !principal.Authenticated {
		t.Fatal("persisted credential did not authenticate", err)
	}
	if _, err := actor.Authenticate(ctx, "missing-user", "fixture original password"); !errors.Is(err, auth.ErrCredentials) {
		t.Fatal("missing identity disclosed a different credential outcome", err)
	}
	permission, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).Filter(orm.Q("codename", "view_asset")).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	publish, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).Filter(orm.Q("codename", "publish_asset")).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if link, ok := event.Model.(*auth.UserPermission); ok {
			link.PermissionID = publish.ID
		}
		return nil
	}}
	store.AfterSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if link, ok := event.Model.(*auth.UserPermission); ok {
			link.PermissionID = permission.ID
		}
		return nil
	}}
	if err := accounts.SetUserPermissions(operatorCtx, user.ID, []int64{permission.ID}); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("paired hooks hid persisted grant retargeting", err)
	}
	store.BeforeSave, store.AfterSave = nil, nil
	beforeGrant, err := accounts.LoadPrincipal(ctx, user.ID)
	if err != nil || beforeGrant.AuthVersion != 1 || len(beforeGrant.Permissions) != 0 {
		t.Fatal("retargeted grant did not roll back", err)
	}
	if err := accounts.SetUserPermissions(ctx, user.ID, []int64{permission.ID}); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("untrusted request granted permissions", err)
	}
	if err := accounts.SetUserPermissions(operatorCtx, user.ID, []int64{permission.ID}); err != nil {
		t.Fatal(err)
	}
	principal, err = accounts.LoadPrincipal(ctx, user.ID)
	if err != nil || principal.AuthVersion != 2 {
		t.Fatal("grant change did not invalidate old session version", err, principal.AuthVersion)
	}
	if err := (auth.ModelPolicy{}).Authorize(ctx, principal, "view", auth.Resource{App: "catalog", Model: "Asset"}); err != nil {
		t.Fatal("persisted direct model permission not honored", err)
	}
	if err := accounts.SetUserPermissions(operatorCtx, user.ID, []int64{permission.ID}); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := accounts.LoadPrincipal(ctx, user.ID)
	if unchanged.AuthVersion != 2 {
		t.Fatal("no-op grant replacement unnecessarily invalidated sessions")
	}
	if err := accounts.SetUserPermissions(operatorCtx, user.ID, []int64{permission.ID, 99999999}); err == nil {
		t.Fatal("missing permission accepted")
	}
	stillGranted, _ := accounts.LoadPrincipal(ctx, user.ID)
	if stillGranted.AuthVersion != 2 || len(stillGranted.Permissions) != 1 {
		t.Fatal("failed grant transaction changed authorization")
	}
	if err := accounts.ChangePassword(operatorCtx, user.ID, "fixture replacement password"); err != nil {
		t.Fatal(err)
	}
	if _, err := actor.Authenticate(ctx, "fixture-user", "fixture original password"); !errors.Is(err, auth.ErrCredentials) {
		t.Fatal("old password remained usable", err)
	}
	principal, err = actor.Authenticate(ctx, "fixture-user", "fixture replacement password")
	if err != nil || principal.AuthVersion != 3 {
		t.Fatal("new credential did not carry changed version", err, principal.AuthVersion)
	}
	if _, err := accounts.RevalidateCredential(ctx, user.ID, *user.PasswordHash); !errors.Is(err, auth.ErrCredentials) {
		t.Fatal("concurrently replaced credential was revalidated", err)
	}
	if err := accounts.SetUserPermissions(operatorCtx, user.ID, nil); err != nil {
		t.Fatal(err)
	}
	principal, err = accounts.LoadPrincipal(ctx, user.ID)
	if err != nil || len(principal.Permissions) != 0 || principal.AuthVersion != 4 {
		t.Fatal("permission removal was not persisted/versioned", err)
	}
	store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if changed, ok := event.Model.(*auth.User); ok {
			changed.Superuser = true
		}
		return nil
	}}
	if err := accounts.SetAccountFlags(operatorCtx, user.ID, true, true, false); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("model hook smuggled an unauthorized account flag", err)
	}
	store.BeforeSave = nil
	principal, err = accounts.LoadPrincipal(ctx, user.ID)
	if err != nil || principal.Staff || principal.Superuser || principal.AuthVersion != 4 {
		t.Fatal("hook escalation was not rolled back", err)
	}
	changing, err := auth.NewAccounts(auth.AccountsConfig{Store: store, Authorize: func(context.Context, auth.AccountChange) error { return nil }, PasswordValidators: []auth.PasswordValidator{
		func(ctx context.Context, _ string, p auth.Principal) error {
			if p.Staff {
				return errors.New("fixture staff password rule")
			}
			return accounts.SetAccountFlags(operatorCtx, p.ID, true, true, false)
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := changing.ChangePassword(operatorCtx, user.ID, "fixture stale validation password"); !errors.Is(err, auth.ErrAccountChanged) {
		t.Fatal("password validation used stale security state", err)
	}
	if _, err := actor.Authenticate(ctx, "fixture-user", "fixture replacement password"); err != nil {
		t.Fatal("rejected stale validation changed stored password", err)
	}
	prospective, err := auth.NewAccounts(auth.AccountsConfig{Store: store, Authorize: func(context.Context, auth.AccountChange) error { return nil }, PasswordValidators: []auth.PasswordValidator{
		func(_ context.Context, _ string, p auth.Principal) error {
			if p.Staff {
				return errors.New("fixture staff password rule")
			}
			return nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prospective.CreateUser(operatorCtx, "new-staff", "fixture staff password", auth.CreateUserOptions{Staff: true}); err == nil {
		t.Fatal("new account password validation ignored prospective flags")
	}
	if err := accounts.SetUnusablePassword(operatorCtx, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := actor.Authenticate(ctx, "fixture-user", "fixture replacement password"); !errors.Is(err, auth.ErrCredentials) {
		t.Fatal("unusable credential authenticated", err)
	}
	inactive, err := accounts.CreateUser(operatorCtx, "inactive-fixture", "fixture inactive password", auth.CreateUserOptions{Inactive: true})
	if err != nil || inactive.Active {
		t.Fatal("explicit inactive flag overwritten by default", err)
	}
	if _, err := actor.Authenticate(ctx, inactive.Identifier, "fixture inactive password"); !errors.Is(err, auth.ErrCredentials) {
		t.Fatal("inactive user authenticated", err)
	}
}
