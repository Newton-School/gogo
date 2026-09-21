package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestAdminAccountGrantChoicesHideScopedAndPolicyHiddenIdentities(t *testing.T) {
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
	var targetID string
	targetScopes := 0
	adapter, err := admin.NewAccountStore(admin.AccountStoreConfig{ORM: admin.ORMConfig{Store: store,
		QueryScope: func(_ context.Context, _ auth.Principal, schema models.Schema) (admin.QueryScope, error) {
			if schema.Key() == (&auth.User{}).Schema().Key() {
				return admin.QueryScope{Predicate: orm.Q("id", targetID), Identity: "grant-parent"}, nil
			}
			if schema.Key() == (&auth.Group{}).Schema().Key() {
				targetScopes++
				return admin.QueryScope{Predicate: orm.Q("name__startswith", "Visible"), Identity: "grant-target"}, nil
			}
			return admin.QueryScope{}, auth.ErrPermissionDenied
		}, ValidateWrite: func(context.Context, auth.Principal, models.Record) error { return nil },
	}, Accounts: auth.AccountsConfig{Authorize: func(context.Context, auth.AccountChange) error { return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	var groups []*auth.Group
	for _, name := range []string{"Visible allowed", "Visible policy-hidden", "Hidden tenant"} {
		group, err := adapter.Accounts().CreateGroup(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		groups = append(groups, group)
	}
	user, err := adapter.Accounts().CreateUserWithoutPassword(ctx, "grant-user", auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	targetID = user.ID
	if err := adapter.Accounts().SetUserGroups(ctx, user.ID, []string{groups[0].ID, groups[1].ID, groups[2].ID}); err != nil {
		t.Fatal(err)
	}
	scoped, err := adapter.Scope(ctx, auth.Principal{ID: "grant-manager", Authenticated: true, Active: true, Staff: true}, "admin", user.Schema())
	if err != nil {
		t.Fatal(err)
	}
	page, err := scoped.List(ctx, admin.ListQuery{Limit: 1})
	if err != nil || len(page.Objects) != 1 {
		t.Fatal(err)
	}
	parent := page.Objects[0]
	reader, ok := scoped.(admin.AccountGrantReader)
	if !ok {
		t.Fatal("default account grant reader absent")
	}
	modelChecked := false
	check := func(_ context.Context, kind admin.AccountGrantKind, object admin.Object) error {
		if kind != admin.UserGroups {
			t.Fatal("wrong target family")
		}
		if object.Record == nil {
			modelChecked = true
			return nil
		}
		if !modelChecked {
			t.Fatal("target data read before model authority")
		}
		id, _ := object.Record.Get("id")
		if id == groups[1].ID {
			return auth.ErrPermissionDenied
		}
		return nil
	}
	choices, err := reader.GrantChoices(ctx, parent, admin.UserGroups, check)
	if err != nil || len(choices.Choices) != 1 || !slices.Equal(choices.Selected, []string{groups[0].ID}) || choices.Choices[0].Value != groups[0].ID {
		t.Fatal("visible grant choices incorrect", err)
	}
	encoded, err := json.Marshal(choices)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range groups[1:] {
		if strings.Contains(string(encoded), `"`+hidden.ID+`"`) || strings.Contains(string(encoded), hidden.Name) {
			t.Fatal("hidden current grant disclosed")
		}
	}
	beforeScopes := targetScopes
	if _, err := reader.GrantChoices(ctx, parent, admin.UserGroups, func(context.Context, admin.AccountGrantKind, admin.Object) error { return auth.ErrPermissionDenied }); !errors.Is(err, auth.ErrPermissionDenied) || targetScopes != beforeScopes {
		t.Fatal("model denial queried target scope", err)
	}
	outage := errors.New("synthetic private policy failure")
	choices, err = reader.GrantChoices(ctx, parent, admin.UserGroups, func(_ context.Context, _ admin.AccountGrantKind, object admin.Object) error {
		if object.Record != nil {
			return outage
		}
		return nil
	})
	if !errors.Is(err, outage) || len(choices.Choices) != 0 || len(choices.Selected) != 0 {
		t.Fatal("policy outage masqueraded as hidden choice", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	choices, err = reader.GrantChoices(canceled, parent, admin.UserGroups, func(_ context.Context, _ admin.AccountGrantKind, object admin.Object) error {
		if object.Record != nil {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || len(choices.Choices) != 0 {
		t.Fatal("callback cancellation ignored", err)
	}
	if _, err := reader.GrantChoices(ctx, parent, admin.GroupPermissions, check); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("wrong account grant family accepted", err)
	}
	if _, err := reader.GrantChoices(ctx, parent, admin.UserGroups, nil); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("selector omitted explicit authority", err)
	}
	unrelated := auth.WithPrincipal(ctx, auth.Principal{ID: "unrelated-caller", Authenticated: true, Active: true, Superuser: true})
	if _, err := reader.GrantChoices(unrelated, parent, admin.UserGroups, func(ctx context.Context, _ admin.AccountGrantKind, _ admin.Object) error {
		if auth.FromContext(ctx).ID != "grant-manager" {
			return auth.ErrPermissionDenied
		}
		return nil
	}); err != nil {
		t.Fatal("reader callback used unrelated caller identity", err)
	}
	changed := false
	choices, err = reader.GrantChoices(ctx, parent, admin.UserGroups, func(ctx context.Context, _ admin.AccountGrantKind, object admin.Object) error {
		if object.Record == nil {
			return nil
		}
		id, _ := object.Record.Get("id")
		if id == groups[1].ID {
			// The later callback moves an earlier authorized choice out of row
			// scope without changing its already-loaded Go record.
			changed = true
			if err := adapter.Accounts().RenameGroup(ctx, groups[0].ID, "Hidden during selector read"); err != nil {
				return err
			}
			return auth.ErrPermissionDenied
		}
		return nil
	})
	if err != nil || !changed || len(choices.Choices) != 0 || len(choices.Selected) != 0 {
		t.Fatal("post-callback target scope change disclosed old choice", err)
	}
}
