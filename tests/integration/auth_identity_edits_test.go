package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestAccountIdentityEditsAndUnusableCreationAreAuthorizedGuardedAndAtomic(t *testing.T) {
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
	validatorCalls := 0
	deny, denyUnusable := false, false
	var observed []auth.AccountChange
	accounts, err := auth.NewAccounts(auth.AccountsConfig{Store: store,
		NormalizeIdentifier: func(raw string) (string, error) { return "namespace-" + raw, nil },
		PasswordValidators:  []auth.PasswordValidator{func(context.Context, string, auth.Principal) error { validatorCalls++; return nil }},
		Authorize: func(_ context.Context, change auth.AccountChange) error {
			observed = append(observed, change)
			if deny || denyUnusable && change.Unusable {
				return auth.ErrPermissionDenied
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	user, err := accounts.CreateUserWithoutPassword(ctx, "first", auth.CreateUserOptions{Staff: true})
	if err != nil || validatorCalls != 0 || user.Identifier != "namespace-first" || user.AuthVersion != 1 || !user.Active || !user.Staff || user.Superuser || user.PasswordHash == nil || auth.HasUsablePassword(*user.PasswordHash) {
		t.Fatal("unusable creation fabricated a credential", err)
	}
	for _, change := range observed {
		if change.Action != "create_user" || !change.Unusable || change.Name != "namespace-first" || !change.Staff {
			t.Fatal("untruthful creation authority delta")
		}
	}
	other, err := accounts.CreateUserWithoutPassword(ctx, "other", auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	denyUnusable = true
	if _, err := accounts.CreateUserWithoutPassword(ctx, "denied", auth.CreateUserOptions{}); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("unusable creation permission bypassed", err)
	}
	if err := accounts.SetUnusablePassword(ctx, user.ID); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("disabled password effect omitted from delta", err)
	}
	denyUnusable = false
	load := func(id string) *auth.User {
		t.Helper()
		row, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", id)).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	if err := accounts.ChangeIdentifier(ctx, user.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	current := load(user.ID)
	if current.Identifier != "namespace-renamed" || current.AuthVersion != 2 || *current.PasswordHash != *user.PasswordHash {
		t.Fatal("identifier transition lost version/credential")
	}
	if _, _, err := accounts.Lookup(ctx, "first"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("old identity still resolves", err)
	}
	if principal, _, err := accounts.Lookup(ctx, "renamed"); err != nil || principal.ID != user.ID || principal.AuthVersion != 2 {
		t.Fatal("new identity normalization differs", err)
	}
	if err := accounts.ChangeIdentifier(ctx, user.ID, "renamed"); err != nil || load(user.ID).AuthVersion != 2 {
		t.Fatal("identity no-op advanced version", err)
	}
	if err := accounts.ChangeIdentifier(ctx, user.ID, "other"); err == nil || load(user.ID).Identifier != "namespace-renamed" || load(user.ID).AuthVersion != 2 {
		t.Fatal("duplicate identity partially committed", err)
	}
	// A failed outer audit-like callback must roll back the nested identity
	// transition along with its invalidation version.
	rollback := errors.New("synthetic outer audit failure")
	if err := db.Atomic(ctx, store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		if err := accounts.ChangeIdentifier(ctx, user.ID, "rolled-back"); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if load(user.ID).AuthVersion != 2 || load(user.ID).Identifier != "namespace-renamed" {
		t.Fatal("outer rollback lost identity/version")
	}
	// Before/after hooks cannot retarget the user even if an after hook would
	// restore the in-memory fields after an unsafe UPDATE.
	store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if event.Record.Schema().Key() == (&auth.User{}).Schema().Key() {
			return event.Record.Set("id", other.ID)
		}
		return nil
	}}
	store.AfterSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if event.Record.Schema().Key() == (&auth.User{}).Schema().Key() {
			return event.Record.Set("id", user.ID)
		}
		return nil
	}}
	if err := accounts.ChangeIdentifier(ctx, user.ID, "retargeted"); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("identifier hook retarget accepted", err)
	}
	store.BeforeSave, store.AfterSave = nil, nil
	if load(user.ID).Identifier != "namespace-renamed" || load(other.ID).Identifier != "namespace-other" {
		t.Fatal("identifier hook changed protected row")
	}
	// Revoking exact authority inside either model hook must stop the effect.
	for _, after := range []bool{false, true} {
		hook := func(_ context.Context, event orm.SaveEvent) error {
			if event.Record.Schema().Key() == (&auth.User{}).Schema().Key() {
				deny = true
			}
			return nil
		}
		if after {
			store.AfterSave = []orm.SaveReceiver{hook}
		} else {
			store.BeforeSave = []orm.SaveReceiver{hook}
		}
		if err := accounts.ChangeIdentifier(ctx, user.ID, "forbidden"); !errors.Is(err, auth.ErrPermissionDenied) {
			t.Fatal("late authority change ignored", after, err)
		}
		store.BeforeSave, store.AfterSave, deny = nil, nil, false
		if load(user.ID).AuthVersion != 2 || load(user.ID).Identifier != "namespace-renamed" {
			t.Fatal("revoked identity operation leaked")
		}
	}
	group, err := accounts.CreateGroup(ctx, "Reviewers")
	if err != nil {
		t.Fatal(err)
	}
	otherGroup, err := accounts.CreateGroup(ctx, "Operators")
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetUserGroups(ctx, user.ID, []string{group.ID}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetUserGroups(ctx, other.ID, []string{group.ID}); err != nil {
		t.Fatal(err)
	}
	beforeUser, beforeOther := load(user.ID).AuthVersion, load(other.ID).AuthVersion
	if err := accounts.RenameGroup(ctx, group.ID, "  Approved reviewers  "); err != nil {
		t.Fatal(err)
	}
	loadGroup := func(id string) *auth.Group {
		t.Helper()
		row, err := orm.For(store, func() *auth.Group { return &auth.Group{} }).Filter(orm.Q("id", id)).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	if loadGroup(group.ID).Name != "Approved reviewers" || load(user.ID).AuthVersion != beforeUser+1 || load(other.ID).AuthVersion != beforeOther+1 {
		t.Fatal("group rename lost member invalidation")
	}
	if err := accounts.RenameGroup(ctx, group.ID, "Approved reviewers"); err != nil || load(user.ID).AuthVersion != beforeUser+1 {
		t.Fatal("group no-op invalidated members", err)
	}
	if err := accounts.RenameGroup(ctx, group.ID, "Operators"); err == nil || load(user.ID).AuthVersion != beforeUser+1 || loadGroup(group.ID).Name != "Approved reviewers" {
		t.Fatal("duplicate group name leaked effects", err)
	}
	if err := db.Atomic(ctx, store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		if err := accounts.RenameGroup(ctx, group.ID, "Rolled back"); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if loadGroup(group.ID).Name != "Approved reviewers" || load(user.ID).AuthVersion != beforeUser+1 {
		t.Fatal("group rollback lost version/name")
	}
	store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if event.Record.Schema().Key() == (&auth.Group{}).Schema().Key() {
			return event.Record.Set("id", otherGroup.ID)
		}
		return nil
	}}
	store.AfterSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if event.Record.Schema().Key() == (&auth.Group{}).Schema().Key() {
			return event.Record.Set("id", group.ID)
		}
		return nil
	}}
	if err := accounts.RenameGroup(ctx, group.ID, "Hijacked"); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("group hook retarget accepted", err)
	}
	store.BeforeSave, store.AfterSave = nil, nil
	if loadGroup(group.ID).Name != "Approved reviewers" || loadGroup(otherGroup.ID).Name != "Operators" {
		t.Fatal("group hook retarget persisted")
	}
	// A hook-time membership change is a stale rename, not a silently missed
	// member invalidation. Its nested account operation is rolled back too.
	store.BeforeSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
		if event.Record.Schema().Key() == (&auth.Group{}).Schema().Key() {
			return accounts.SetUserGroups(ctx, other.ID, nil)
		}
		return nil
	}}
	if err := accounts.RenameGroup(ctx, group.ID, "Stale members"); !errors.Is(err, auth.ErrAccountChanged) {
		t.Fatal("rename missed hook-time membership change", err)
	}
	store.BeforeSave = nil
	links, err := orm.For(store, func() *auth.UserGroup { return &auth.UserGroup{} }).Filter(orm.Q("group_id", group.ID)).Count(ctx)
	if err != nil || links != 2 || load(other.ID).AuthVersion != beforeOther+1 {
		t.Fatal("membership side effect escaped rollback", err)
	}
	for _, after := range []bool{false, true} {
		hook := func(_ context.Context, event orm.SaveEvent) error {
			if event.Record.Schema().Key() == (&auth.Group{}).Schema().Key() {
				deny = true
			}
			return nil
		}
		if after {
			store.AfterSave = []orm.SaveReceiver{hook}
		} else {
			store.BeforeSave = []orm.SaveReceiver{hook}
		}
		if err := accounts.RenameGroup(ctx, group.ID, "Forbidden"); !errors.Is(err, auth.ErrPermissionDenied) {
			t.Fatal("group late authority ignored", after, err)
		}
		store.BeforeSave, store.AfterSave, deny = nil, nil, false
		if loadGroup(group.ID).Name != "Approved reviewers" || load(user.ID).AuthVersion != beforeUser+1 {
			t.Fatal("group denied hook leaked version/name")
		}
	}
	for _, change := range observed {
		if strings.Contains(change.Name, "password") {
			t.Fatal("credential material entered authority DTO")
		}
	}
}

func TestAccountIdentityFinalAuthorizerCannotReplaceCommittedSnapshot(t *testing.T) {
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
	for _, action := range []string{"create_user", "change_identifier"} {
		t.Run(action, func(t *testing.T) {
			for _, mutation := range []string{"nested_identity", "nested_flags", "returned_pointer"} {
				t.Run(mutation, func(t *testing.T) {
					var accounts *auth.Accounts
					var captured *auth.User
					fired := false
					outer := action + "-" + mutation
					var err error
					accounts, err = auth.NewAccounts(auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, change auth.AccountChange) error {
						if fired || change.Action != action || change.Name != outer || !db.InTransaction(ctx, backend.Alias()) {
							return nil
						}
						row, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("identifier", outer)).Get(ctx)
						if errors.Is(err, orm.ErrNotFound) {
							return nil // Pre-save authorization is not the tested boundary.
						}
						if err != nil {
							return err
						}
						fired = true
						switch mutation {
						case "nested_identity":
							return accounts.ChangeIdentifier(ctx, row.ID, outer+"-nested")
						case "nested_flags":
							return accounts.SetAccountFlags(ctx, row.ID, true, true, true)
						default:
							if captured == nil || captured.PasswordHash == nil {
								t.Fatal("saved user pointer was not captured")
							}
							*captured.PasswordHash = "!changed-by-policy"
							return nil
						}
					}})
					if err != nil {
						t.Fatal(err)
					}
					store.AfterSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
						if model, ok := models.Underlying(event.Record); ok {
							if user, ok := model.(*auth.User); ok {
								captured = user
							}
						}
						return nil
					}}
					t.Cleanup(func() { store.AfterSave = nil })
					var original *auth.User
					if action == "create_user" {
						var created *auth.User
						created, err = accounts.CreateUserWithoutPassword(ctx, outer, auth.CreateUserOptions{})
						if created != nil {
							t.Fatal("stale created account returned to caller")
						}
					} else {
						original, err = accounts.CreateUserWithoutPassword(ctx, outer+"-initial", auth.CreateUserOptions{})
						if err != nil {
							t.Fatal(err)
						}
						err = accounts.ChangeIdentifier(ctx, original.ID, outer)
					}
					if !fired || !errors.Is(err, auth.ErrAccountChanged) {
						t.Fatal("post-save authorization mutation accepted", fired, err)
					}
					if action == "create_user" {
						count, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("identifier__in", []string{outer, outer + "-nested"})).Count(ctx)
						if err != nil || count != 0 {
							t.Fatal("stale account creation escaped rollback", count, err)
						}
					} else {
						current, err := orm.For(store, func() *auth.User { return &auth.User{} }).Filter(orm.Q("id", original.ID)).Get(ctx)
						if err != nil || current.Identifier != original.Identifier || current.AuthVersion != original.AuthVersion || current.Staff || current.Superuser || *current.PasswordHash != *original.PasswordHash {
							t.Fatal("nested policy account effect escaped rollback", err)
						}
					}
				})
			}
		})
	}
}
