package integration_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestAdminAccountGrantWritesPreserveHiddenLinksAndRecheckExactAuthority(t *testing.T) {
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
	var targetID, tenantHidden, policyHidden, lateHidden, scopeExcluded string
	var scopeAfterTarget string
	var afterDomain bool
	var rejectComplete, denyChanges, nestedSource bool
	var hook func(context.Context, auth.AccountChange) error
	var adapter *admin.AccountStore
	var err error
	adapter, err = admin.NewAccountStore(admin.AccountStoreConfig{ORM: admin.ORMConfig{Store: store,
		QueryScope: func(_ context.Context, _ auth.Principal, schema models.Schema) (admin.QueryScope, error) {
			switch schema.Key() {
			case (&auth.User{}).Schema().Key():
				return admin.QueryScope{Predicate: orm.Q("id", targetID), Identity: "grant-parent"}, nil
			case (&auth.Group{}).Schema().Key():
				predicate := orm.Q("name__startswith", "Visible")
				if scopeExcluded != "" {
					predicate = orm.And(predicate, orm.Not(orm.Q("id", scopeExcluded)))
				}
				return admin.QueryScope{Predicate: predicate, Identity: "grant-groups"}, nil
			case (&auth.Permission{}).Schema().Key():
				return admin.QueryScope{Predicate: orm.Q("id__gt", int64(0)), Identity: "grant-permissions"}, nil
			}
			return admin.QueryScope{}, auth.ErrPermissionDenied
		}, ValidateWrite: func(context.Context, auth.Principal, models.Record) error { return nil },
	}, Accounts: auth.AccountsConfig{Authorize: func(ctx context.Context, change auth.AccountChange) error {
		if auth.FromContext(ctx).ID == "bootstrap" {
			return nil
		}
		if auth.FromContext(ctx).ID != "grant-manager" {
			return auth.ErrPermissionDenied
		}
		if denyChanges {
			return auth.ErrPermissionDenied
		}
		if rejectComplete && slices.Contains(change.GroupIDs, policyHidden) {
			return auth.ErrPermissionDenied
		}
		if hook != nil {
			return hook(ctx, change)
		}
		return nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := auth.WithPrincipal(ctx, auth.Principal{ID: "bootstrap", Authenticated: true, Active: true})
	var groups []*auth.Group
	for _, name := range []string{"Visible old", "Visible new", "Visible policy-hidden", "Hidden tenant"} {
		group, err := adapter.Accounts().CreateGroup(bootstrap, name)
		if err != nil {
			t.Fatal(err)
		}
		groups = append(groups, group)
	}
	policyHidden, tenantHidden = groups[2].ID, groups[3].ID
	user, err := adapter.Accounts().CreateUserWithoutPassword(bootstrap, "grant-edited-user", auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	targetID = user.ID
	if err := adapter.Accounts().SetUserGroups(bootstrap, user.ID, []string{groups[0].ID, policyHidden, tenantHidden}); err != nil {
		t.Fatal(err)
	}
	actor := auth.Principal{ID: "grant-manager", Authenticated: true, Active: true, Staff: true}
	scoped, err := adapter.Scope(ctx, actor, "admin", user.Schema())
	if err != nil {
		t.Fatal(err)
	}
	editor := scoped.(admin.AccountGrantEditor)
	load := func() admin.Object {
		t.Helper()
		page, err := scoped.List(ctx, admin.ListQuery{Limit: 1})
		if err != nil || len(page.Objects) != 1 {
			t.Fatal("missing scoped user", err)
		}
		return page.Objects[0]
	}
	check := func(ctx context.Context, kind admin.AccountGrantKind, object admin.Object) error {
		if object.Record == nil {
			return nil
		}
		id, _ := object.Record.Get("id")
		if afterDomain && id == scopeAfterTarget {
			ordered := []string{groups[0].ID, groups[1].ID}
			slices.Sort(ordered)
			scopeExcluded = ordered[0]
		}
		if id == policyHidden || id == lateHidden {
			return auth.ErrPermissionDenied
		}
		if nestedSource {
			nestedSource = false
			return adapter.Accounts().ChangeIdentifier(ctx, targetID, "callback-retargeted-identity")
		}
		return nil
	}
	apply := func(object admin.Object, values map[admin.AccountGrantKind][]string, tail func(context.Context) error) (admin.Object, error) {
		var written admin.Object
		err := scoped.Atomic(ctx, func(ctx context.Context) error {
			var err error
			written, err = editor.SaveAccountGrants(ctx, object, values, check)
			if err != nil {
				return err
			}
			if tail != nil {
				return tail(ctx)
			}
			return nil
		})
		return written, err
	}
	assertGroups := func(expected ...string) {
		t.Helper()
		links, err := orm.For(store, func() *auth.UserGroup { return &auth.UserGroup{} }).Filter(orm.Q("user_id", targetID)).All(ctx)
		if err != nil {
			t.Fatal(err)
		}
		actual := make([]string, len(links))
		for i, link := range links {
			actual[i] = link.GroupID
		}
		slices.Sort(actual)
		slices.Sort(expected)
		if !slices.Equal(actual, expected) {
			t.Fatal("account grant state changed unexpectedly")
		}
	}
	initial := load()
	if _, err := editor.SaveAccountGrants(ctx, initial, map[admin.AccountGrantKind][]string{admin.UserGroups: {groups[1].ID}}, check); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("mutation without outer audit transaction", err)
	}
	if _, err := apply(initial, map[admin.AccountGrantKind][]string{admin.UserGroups: {tenantHidden}}, nil); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("hidden submitted target accepted", err)
	}
	if _, err := apply(initial, map[admin.AccountGrantKind][]string{admin.UserGroups: {policyHidden}}, nil); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("policy-hidden submitted target accepted", err)
	}
	rejectComplete = true
	if _, err := apply(initial, map[admin.AccountGrantKind][]string{admin.UserGroups: {groups[1].ID}}, nil); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("retained hidden grant bypassed complete domain authority", err)
	}
	rejectComplete = false
	assertGroups(groups[0].ID, policyHidden, tenantHidden)
	nestedSource = true
	if _, err := apply(initial, map[admin.AccountGrantKind][]string{admin.UserGroups: {groups[1].ID}}, nil); !errors.Is(err, admin.ErrConflict) {
		t.Fatal("nested source mutation accepted", err)
	}
	if current := load(); current.Version != initial.Version {
		t.Fatal("nested callback source mutation persisted")
	}
	hook = func(_ context.Context, change auth.AccountChange) error {
		if change.Action == "set_user_groups" {
			lateHidden = groups[0].ID
		}
		return nil
	}
	// The old visible choice is explicitly posted and remains linked. Its
	// revocation after prevalidation must still reject the complete edit.
	if _, err := apply(initial, map[admin.AccountGrantKind][]string{admin.UserGroups: {groups[0].ID, groups[1].ID}}, nil); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("unchanged submitted target escaped final check", err)
	}
	lateHidden, hook = "", nil
	assertGroups(groups[0].ID, policyHidden, tenantHidden)
	ordered := []string{groups[0].ID, groups[1].ID}
	slices.Sort(ordered)
	scopeAfterTarget = ordered[1]
	hook = func(_ context.Context, change auth.AccountChange) error {
		if change.Action == "set_user_groups" {
			afterDomain = true
		}
		return nil
	}
	if _, err := apply(initial, map[admin.AccountGrantKind][]string{admin.UserGroups: ordered}, nil); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("later callback hid earlier target after final authority", err)
	}
	scopeExcluded, scopeAfterTarget, afterDomain, hook = "", "", false, nil
	assertGroups(groups[0].ID, policyHidden, tenantHidden)
	rollback := errors.New("synthetic audit rollback")
	if _, err := apply(initial, map[admin.AccountGrantKind][]string{admin.UserGroups: {groups[1].ID}}, func(context.Context) error { return rollback }); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	assertGroups(groups[0].ID, policyHidden, tenantHidden)
	if current := load(); current.Version != initial.Version {
		t.Fatal("audit rollback changed auth version")
	}
	written, err := apply(initial, map[admin.AccountGrantKind][]string{admin.UserGroups: {groups[1].ID}}, nil)
	if err != nil {
		t.Fatal("visible edit retaining hidden links", err)
	}
	assertGroups(groups[1].ID, policyHidden, tenantHidden)
	if _, err := apply(initial, map[admin.AccountGrantKind][]string{admin.UserGroups: {groups[0].ID}}, nil); !errors.Is(err, admin.ErrConflict) {
		t.Fatal("stale source version accepted", err)
	}
	denyChanges = true
	noOp, err := apply(written, map[admin.AccountGrantKind][]string{admin.UserGroups: {groups[1].ID}}, nil)
	if err != nil || noOp.Version != written.Version {
		t.Fatal("unchanged grant set invoked mutation authority or changed version", err)
	}
	denyChanges = false
	permissions, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).OrderBy("id").Limit(2).All(ctx)
	if err != nil || len(permissions) != 2 {
		t.Fatal(err)
	}
	ids := []string{strconv.FormatInt(permissions[0].ID, 10), strconv.FormatInt(permissions[1].ID, 10)}
	written, err = apply(written, map[admin.AccountGrantKind][]string{admin.UserPermissions: ids}, nil)
	if err != nil {
		t.Fatal("direct permission edit failed", err)
	}
	links, err := orm.For(store, func() *auth.UserPermission { return &auth.UserPermission{} }).Filter(orm.Q("user_id", targetID)).Count(ctx)
	if err != nil || links != 2 {
		t.Fatal("direct permissions missing", err)
	}
	assertGroups(groups[1].ID, policyHidden, tenantHidden)
	if _, err := apply(written, map[admin.AccountGrantKind][]string{admin.UserPermissions: {"01"}}, nil); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("noncanonical permission ID accepted", err)
	}
	// A second denied family must roll back the first family's domain write
	// and version bump, even if the caller handles the returned error.
	beforeCombined := load()
	hook = func(_ context.Context, change auth.AccountChange) error {
		if change.Action == "set_user_permissions" {
			return auth.ErrPermissionDenied
		}
		return nil
	}
	if _, err := apply(beforeCombined, map[admin.AccountGrantKind][]string{admin.UserGroups: {groups[0].ID}, admin.UserPermissions: {}}, nil); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("second family denial ignored", err)
	}
	hook = nil
	if current := load(); current.Version != beforeCombined.Version {
		t.Fatal("partial combined version bump committed")
	}
	assertGroups(groups[1].ID, policyHidden, tenantHidden)
	// Group permissions use their own domain action and invalidate existing
	// members. A readonly-hidden permission is retained, not disclosed.
	groupScoped, err := adapter.Scope(ctx, actor, "admin", (&auth.Group{}).Schema())
	if err != nil {
		t.Fatal(err)
	}
	groupRows, err := groupScoped.List(ctx, admin.ListQuery{Filters: map[string]string{"id": groups[1].ID}, Limit: 1})
	if err != nil || len(groupRows.Objects) != 1 {
		t.Fatal(err)
	}
	groupObject := groupRows.Objects[0]
	if err := adapter.Accounts().SetGroupPermissions(bootstrap, groups[1].ID, []int64{permissions[0].ID}); err != nil {
		t.Fatal(err)
	}
	memberBefore := load()
	groupEditor := groupScoped.(admin.AccountGrantEditor)
	permissionCheck := func(_ context.Context, kind admin.AccountGrantKind, object admin.Object) error {
		if kind != admin.GroupPermissions {
			return auth.ErrPermissionDenied
		}
		if object.Record != nil {
			id, _ := object.Record.Get("id")
			if id == permissions[0].ID {
				return auth.ErrPermissionDenied
			}
		}
		return nil
	}
	choices, err := groupEditor.GrantChoices(ctx, groupObject, admin.GroupPermissions, permissionCheck)
	if err != nil || len(choices.Selected) != 0 || slices.ContainsFunc(choices.Choices, func(choice forms.Choice) bool { return choice.Value == ids[0] }) {
		t.Fatal("group selector disclosed hidden selected permission", err)
	}
	if err := groupScoped.Atomic(ctx, func(ctx context.Context) error {
		_, err := groupEditor.SaveAccountGrants(ctx, groupObject, map[admin.AccountGrantKind][]string{admin.GroupPermissions: {ids[1]}}, permissionCheck)
		return err
	}); err != nil {
		t.Fatal("group grant edit failed", err)
	}
	groupLinks, err := orm.For(store, func() *auth.GroupPermission { return &auth.GroupPermission{} }).Filter(orm.Q("group_id", groups[1].ID)).Count(ctx)
	if err != nil || groupLinks != 2 {
		t.Fatal("hidden group grant was not retained", err)
	}
	if current := load(); current.Version == memberBefore.Version {
		t.Fatal("group grant did not invalidate member session version")
	}
}
