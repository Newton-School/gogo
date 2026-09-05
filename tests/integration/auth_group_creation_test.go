package integration_test

import (
	"context"
	"errors"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestAccountGroupCreationRechecksAuthorityAndGrantFreeSnapshot(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	registry := &models.Registry{}
	for _, schema := range append(auth.Schemas(), (&contenttypes.ContentType{}).Schema()) {
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
	permission, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).First(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var accounts *auth.Accounts
	var memberID string
	deny, fired := false, false
	mode := ""
	accounts, err = auth.NewAccounts(auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, change auth.AccountChange) error {
		if deny {
			return auth.ErrPermissionDenied
		}
		if fired || change.Action != "create_group" || !db.InTransaction(ctx, backend.Alias()) || mode == "" {
			return nil
		}
		group, err := orm.For(store, func() *auth.Group { return &auth.Group{} }).Filter(orm.Q("name", change.Name)).Get(ctx)
		if errors.Is(err, orm.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		fired = true
		if mode == "rename" {
			return accounts.RenameGroup(ctx, group.ID, "nested-name")
		}
		if mode == "grant" {
			return accounts.SetGroupPermissions(ctx, group.ID, []int64{permission.ID})
		}
		if mode == "member" {
			return accounts.SetUserGroups(ctx, memberID, []string{group.ID})
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	member, err := accounts.CreateUserWithoutPassword(ctx, "member", auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memberID = member.ID
	for _, phase := range []string{"before", "after"} {
		hook := func(_ context.Context, event orm.SaveEvent) error {
			if event.Record.Schema().Key() == (&auth.Group{}).Schema().Key() {
				deny = true
			}
			return nil
		}
		if phase == "before" {
			store.BeforeSave = []orm.SaveReceiver{hook}
		} else {
			store.AfterSave = []orm.SaveReceiver{hook}
		}
		if group, err := accounts.CreateGroup(ctx, phase); group != nil || !errors.Is(err, auth.ErrPermissionDenied) {
			t.Fatal("group creation ignored revoked authority", phase, err)
		}
		deny = false
		store.BeforeSave, store.AfterSave = nil, nil
	}
	for _, operation := range []string{"rename", "grant", "member"} {
		mode, fired = operation, false
		if group, err := accounts.CreateGroup(ctx, operation); group != nil || !fired || !errors.Is(err, auth.ErrAccountChanged) {
			t.Fatal("post-save policy changed created group", operation, fired, err)
		}
	}
	mode = ""
	count, err := orm.For(store, func() *auth.Group { return &auth.Group{} }).Count(ctx)
	if err != nil || count != 0 {
		t.Fatal("failed group creation escaped rollback", count, err)
	}
	grants, err := orm.For(store, func() *auth.GroupPermission { return &auth.GroupPermission{} }).Count(ctx)
	if err != nil || grants != 0 {
		t.Fatal("nested group grant escaped rollback", grants, err)
	}
	principal, err := accounts.LoadPrincipal(ctx, memberID)
	if err != nil || principal.AuthVersion != 1 {
		t.Fatal("nested membership invalidation escaped rollback", err)
	}
	links, err := orm.For(store, func() *auth.UserGroup { return &auth.UserGroup{} }).Count(ctx)
	if err != nil || links != 0 {
		t.Fatal("nested membership escaped rollback", links, err)
	}
	if group, err := accounts.CreateGroup(ctx, "Allowed"); err != nil || group == nil || group.Name != "Allowed" {
		t.Fatal("ordinary group creation failed", err)
	}
}
