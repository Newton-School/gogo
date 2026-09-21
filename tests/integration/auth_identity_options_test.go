package integration_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestAccountIdentityOptionsRoundTrip(t *testing.T) {
	for _, kind := range []models.Kind{models.Auto, models.BigAuto, models.UUID} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			backend := testservice.Postgres(t)
			identity, err := auth.NewAccountModels(kind)
			if err != nil {
				t.Fatal(err)
			}
			registry := &models.Registry{}
			schemas := append(identity.Schemas(), auth.TokenSchemas()...)
			for _, schema := range append(schemas, (&contenttypes.ContentType{}).Schema(), admin.LogSchema()) {
				if err := registry.Register(schema); err != nil {
					t.Fatal(err)
				}
			}
			if err := registry.Freeze(); err != nil {
				t.Fatal(err)
			}
			store := orm.New(backend, registry)
			all := append(contenttypes.Migrations(), identity.Migrations()...)
			all = append(all, auth.TokenMigrations()...)
			runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: all}
			if err := runner.Apply(ctx, ""); err != nil {
				t.Fatal(err)
			}
			if err := runner.Apply(ctx, ""); err != nil {
				t.Fatal("repeat migration", err)
			}
			if _, err := contenttypes.Sync(ctx, store, registry, nil); err != nil {
				t.Fatal(err)
			}
			if err := auth.SyncPermissions(ctx, store, registry, nil); err != nil {
				t.Fatal(err)
			}
			adapter, err := admin.NewAccountStore(admin.AccountStoreConfig{ORM: admin.ORMConfig{Store: store,
				QueryScope: func(context.Context, auth.Principal, models.Schema) (admin.QueryScope, error) {
					return admin.QueryScope{Identity: "identity-options"}, nil
				},
				ValidateWrite: func(context.Context, auth.Principal, models.Record) error { return nil },
			}, Accounts: auth.AccountsConfig{Models: identity, Authorize: func(context.Context, auth.AccountChange) error { return nil }}})
			if err != nil {
				t.Fatal(err)
			}
			accounts := adapter.Accounts()
			permission, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).Filter(orm.Q("codename", "view_user")).Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= 2; i++ {
				user, err := accounts.CreateUserWithoutPassword(ctx, "user-"+strconv.Itoa(i), auth.CreateUserOptions{Staff: true})
				if err != nil {
					t.Fatal(err)
				}
				group, err := accounts.CreateGroup(ctx, "group-"+strconv.Itoa(i))
				if err != nil {
					t.Fatal(err)
				}
				if kind != models.UUID && (user.ID != strconv.Itoa(i) || group.ID != strconv.Itoa(i)) {
					t.Fatal("fresh IDs must start at 1", user.ID, group.ID)
				}
				if kind == models.UUID && (!strings.Contains(user.ID, "-") || !strings.Contains(group.ID, "-")) {
					t.Fatal("UUID option ignored")
				}
				if err := accounts.SetUserGroups(ctx, user.ID, []string{group.ID}); err != nil {
					t.Fatal(err)
				}
				if err := accounts.SetUserPermissions(ctx, user.ID, []int64{permission.ID}); err != nil {
					t.Fatal(err)
				}
				principal, err := accounts.LoadPrincipal(ctx, user.ID)
				if err != nil || principal.ID != user.ID {
					t.Fatal("principal round trip", err)
				}
				scope, err := adapter.Scope(ctx, principal, "test", adapter.UserAdmin().Schema)
				if err != nil {
					t.Fatal(err)
				}
				page, err := scope.List(ctx, admin.ListQuery{Limit: 10})
				if err != nil || len(page.Objects) != i {
					t.Fatal("Admin read", err)
				}
				var object admin.Object
				for _, row := range page.Objects {
					id, _ := row.Record.Get("id")
					if id == user.ID {
						object = row
					}
				}
				choices, err := scope.(admin.AccountGrantReader).GrantChoices(ctx, object, admin.UserGroups, func(context.Context, admin.AccountGrantKind, admin.Object) error { return nil })
				if err != nil || len(choices.Selected) != 1 || choices.Selected[0] != group.ID {
					t.Fatal("Admin group selector", choices.Selected, err)
				}
				tokens, err := auth.NewTokens(auth.TokensConfig{Accounts: accounts, Authorize: func(context.Context, auth.TokenChange) error { return nil }})
				if err != nil {
					t.Fatal(err)
				}
				issue, err := tokens.Issue(ctx, user.ID, []string{"gogo_auth.view_user"})
				if err != nil || issue.State != auth.TokenChanged || len(issue.ID) != 36 {
					t.Fatal("credential must retain random UUID", err)
				}
				tokenPrincipal, err := tokens.AuthenticateToken(ctx, issue.Secret.Reveal())
				if err != nil || tokenPrincipal.ID != user.ID {
					t.Fatal("token subject round trip", err)
				}
				if _, err := tokens.Revoke(ctx, issue.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := tokens.AuthenticateToken(ctx, issue.Secret.Reveal()); !errors.Is(err, auth.ErrToken) {
					t.Fatal("revoked credential accepted", err)
				}
			}
			// Independent simultaneous inserts must not race a Go-side counter.
			var workers sync.WaitGroup
			type allocation struct {
				id  string
				err error
			}
			results := make(chan allocation, 8)
			for i := range 8 {
				workers.Go(func() {
					group, err := accounts.CreateGroup(ctx, "concurrent-"+strconv.Itoa(i))
					result := allocation{err: err}
					if group != nil {
						result.id = group.ID
					}
					results <- result
				})
			}
			workers.Wait()
			close(results)
			seen := map[string]bool{}
			for result := range results {
				if result.err != nil || result.id == "" || seen[result.id] {
					t.Fatal("concurrent ID allocation", result.err)
				}
				seen[result.id] = true
			}
			if kind == models.BigAuto {
				if _, err := backend.Exec(ctx, `ALTER TABLE gogo_users ALTER COLUMN id RESTART WITH 2147483648`); err != nil {
					t.Fatal(err)
				}
				user, err := accounts.CreateUserWithoutPassword(ctx, "large", auth.CreateUserOptions{})
				if err != nil || user.ID != "2147483648" {
					t.Fatal("64-bit allocation", err)
				}
				if _, err := accounts.LoadPrincipal(ctx, user.ID); err != nil {
					t.Fatal(err)
				}
			}
			// An ID format choice is a migration contract, never auto-detected.
			if kind != models.Auto {
				if _, err := auth.NewAccounts(auth.AccountsConfig{Store: store}); err == nil {
					t.Fatal("mismatched account schemas accepted")
				}
			}
			for _, phase := range []string{"before", "after"} {
				hook := func(_ context.Context, event orm.SaveEvent) error {
					if event.Record.Schema().Key() == "gogo_auth.User" {
						return event.Record.Set("id", "1")
					}
					return nil
				}
				if phase == "before" {
					store.BeforeSave = []orm.SaveReceiver{hook}
				} else {
					store.AfterSave = []orm.SaveReceiver{hook}
				}
				if _, err := accounts.CreateUserWithoutPassword(ctx, "retarget-"+phase, auth.CreateUserOptions{}); !errors.Is(err, auth.ErrPermissionDenied) {
					t.Fatal("ID injection not rejected", phase, err)
				}
				store.BeforeSave, store.AfterSave = nil, nil
				if exists, err := orm.For(store, identity.User).Filter(orm.Q("identifier", "retarget-"+phase)).Exists(ctx); err != nil || exists {
					t.Fatal("rejected row escaped rollback", err)
				}
			}
			var sqlType string
			if err := db.QueryRow(ctx, backend, `SELECT data_type FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'gogo_users' AND column_name = 'id'`, nil, &sqlType); err != nil {
				t.Fatal(err)
			}
			if sqlType != map[models.Kind]string{models.Auto: "integer", models.BigAuto: "bigint", models.UUID: "uuid"}[kind] {
				t.Fatal("unexpected SQL type", sqlType)
			}
		})
	}
}
