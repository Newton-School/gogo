package main

import (
	"context"
	"errors"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

func TestDemoAccountAuthorityIsBoundedAndBootstrapExpires(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	registry := &models.Registry{}
	for _, schema := range append(auth.Schemas(), (&contenttypes.ContentType{}).Schema(), (&Product{}).Schema(), (&ProductNote{}).Schema()) {
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
	if err := auth.SyncPermissions(ctx, store, registry, nil); err != nil {
		t.Fatal(err)
	}
	catalogGrant, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).Filter(orm.Q("codename", "view_product")).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	authGrant, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).Filter(orm.Q("codename", "change_user")).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scope := &demoAccountScope{bootstrap: true}
	scope.grantPermissions = []int64{catalogGrant.ID}
	adapter, err := newDemoAccountStore(store, scope)
	if err != nil {
		t.Fatal(err)
	}
	accounts := adapter.Accounts()
	bootstrap := auth.WithPrincipal(ctx, auth.Principal{ID: "fixture-bootstrap", Authenticated: true, Active: true})
	password, _ := security.RandomToken(24)
	reviewer, err := accounts.CreateUser(bootstrap, "reviewer", password, auth.CreateUserOptions{Staff: true})
	if err != nil {
		t.Fatal(err)
	}
	managed, err := accounts.CreateUserWithoutPassword(bootstrap, "demo-managed", auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hiddenGroup, err := accounts.CreateGroup(bootstrap, "Hidden group")
	if err != nil {
		t.Fatal(err)
	}
	scope.reviewerID, scope.managedID, scope.bootstrap = reviewer.ID, managed.ID, false
	principal, err := accounts.LoadPrincipal(ctx, reviewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.WithPrincipal(ctx, principal)
	if err := accounts.SetAccountFlags(actor, managed.ID, true, true, false); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func() error{
		func() error { return accounts.SetAccountFlags(bootstrap, managed.ID, true, false, false) },
		func() error { return accounts.SetAccountFlags(actor, reviewer.ID, false, false, false) },
		func() error { return accounts.SetAccountFlags(actor, managed.ID, true, true, true) },
		func() error { return accounts.SetUserGroups(actor, managed.ID, []string{hiddenGroup.ID}) },
		func() error { return accounts.SetUserPermissions(actor, managed.ID, []int64{999}) },
		func() error { return accounts.SetUserPermissions(actor, managed.ID, []int64{authGrant.ID}) },
		func() error {
			_, err := accounts.CreateUserWithoutPassword(actor, "outside-scope", auth.CreateUserOptions{})
			return err
		},
		func() error { return accounts.ChangeIdentifier(actor, managed.ID, "outside-scope") },
	} {
		if err := mutation(); !errors.Is(err, auth.ErrPermissionDenied) {
			t.Fatal("fixture authority widened", err)
		}
	}
	created, err := accounts.CreateUserWithoutPassword(actor, "demo-created", auth.CreateUserOptions{})
	if err != nil || created.PasswordHash == nil || auth.HasUsablePassword(*created.PasswordHash) {
		t.Fatal("scoped password-disabled creation failed", err)
	}
	if err := accounts.ChangeIdentifier(actor, created.ID, "demo-renamed"); err != nil {
		t.Fatal(err)
	}
	group, err := accounts.CreateGroup(actor, "Demo editors")
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetUserGroups(actor, created.ID, []string{group.ID}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetUserPermissions(actor, created.ID, []int64{catalogGrant.ID}); err != nil {
		t.Fatal("allowed catalog direct grant failed", err)
	}
	if err := accounts.SetGroupPermissions(actor, group.ID, []int64{catalogGrant.ID}); err != nil {
		t.Fatal("allowed catalog group grant failed", err)
	}
	if err := accounts.SetGroupPermissions(actor, group.ID, []int64{authGrant.ID}); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("fixture allowed an authentication grant", err)
	}
	if err := accounts.RenameGroup(actor, group.ID, "Demo renamed editors"); err != nil {
		t.Fatal(err)
	}
	// A stale request principal must not retain management authority after an
	// account-version transition, even if its public staff flag remains true.
	if _, err := accounts.ChangeOwnPassword(actor, password, password+"-changed"); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetAccountFlags(actor, managed.ID, true, false, false); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("stale fixture actor retained grant authority", err)
	}
}

func TestDemoInvalidMailPortFailsBeforeOpeningServices(t *testing.T) {
	t.Setenv("GOGO_TEST_POSTGRES_DSN", "host=127.0.0.1 user=gogo_test dbname=postgres")
	t.Setenv("GOGO_ADMIN_DEMO_REDIS_URL", "redis://127.0.0.1:6379")
	t.Setenv("GOGO_ADMIN_DEMO_PASSWORD", "synthetic required configuration")
	for _, value := range []string{"invalid", "0", "65536"} {
		t.Setenv("GOGO_ADMIN_DEMO_SMTP_PORT", value)
		if err := run(); err == nil {
			t.Fatal("invalid local SMTP capture port accepted")
		}
	}
}
