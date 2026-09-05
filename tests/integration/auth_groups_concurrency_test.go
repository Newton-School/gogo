package integration

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type groupMutationGateKey struct{}

// The gate pauses the second authorization call, after Accounts has acquired
// its first user/group row lock. No sleeps or scheduler timing establish order.
type groupMutationGate struct {
	held    chan int
	proceed chan struct{}
	once    sync.Once
}

func newGroupMutationGate() *groupMutationGate {
	return &groupMutationGate{held: make(chan int, 1), proceed: make(chan struct{})}
}

func (g *groupMutationGate) release() { g.once.Do(func() { close(g.proceed) }) }

func TestPostgresGroupMembershipPermissionConcurrency(t *testing.T) {
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
	ctx := context.Background()
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
	oldGrant, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).Filter(orm.Q("codename", "view_user")).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newGrant, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).Filter(orm.Q("codename", "change_user")).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldPermissionName := (&auth.User{}).Schema().AppLabel + "." + oldGrant.Codename
	newPermissionName := (&auth.User{}).Schema().AppLabel + "." + newGrant.Codename
	accounts, err := auth.NewAccounts(auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, _ auth.AccountChange) error {
		if auth.FromContext(ctx).ID != "fixture-group-operator" {
			return auth.ErrPermissionDenied
		}
		gate, _ := ctx.Value(groupMutationGateKey{}).(*groupMutationGate)
		if gate == nil || !db.InTransaction(ctx, backend.Alias()) {
			return nil
		}
		var pid int
		if err := db.QueryRow(ctx, db.ExecutorFor(ctx, backend), "SELECT pg_backend_pid()", nil, &pid); err != nil {
			return err
		}
		select {
		case gate.held <- pid:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-gate.proceed:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"addition after grant replacement", "opposite locks roll back a deadlock", "removal while grant writer waits"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			ctx = auth.WithPrincipal(ctx, auth.Principal{ID: "fixture-group-operator", Authenticated: true, Active: true})
			user, err := accounts.CreateUser(ctx, scenario, "fixture concurrent group password", auth.CreateUserOptions{})
			if err != nil {
				t.Fatal(err)
			}
			group, err := accounts.CreateGroup(ctx, scenario)
			if err != nil {
				t.Fatal(err)
			}
			if err := accounts.SetGroupPermissions(ctx, group.ID, []int64{oldGrant.ID}); err != nil {
				t.Fatal(err)
			}
			if scenario != "addition after grant replacement" {
				if err := accounts.SetUserGroups(ctx, user.ID, []string{group.ID}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := accounts.LoadPrincipal(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			membership, grants := newGroupMutationGate(), newGroupMutationGate()
			defer membership.release()
			defer grants.release()
			memberDone, grantDone := make(chan error, 1), make(chan error, 1)
			go func() {
				grantDone <- accounts.SetGroupPermissions(context.WithValue(ctx, groupMutationGateKey{}, grants), group.ID, []int64{newGrant.ID})
			}()
			grantPID := awaitGroupLock(t, ctx, grants.held)
			go func() {
				groups := []string{group.ID}
				if scenario == "removal while grant writer waits" {
					groups = nil
				}
				memberDone <- accounts.SetUserGroups(context.WithValue(ctx, groupMutationGateKey{}, membership), user.ID, groups)
			}()
			memberPID := awaitGroupLock(t, ctx, membership.held)
			if scenario == "removal while grant writer waits" {
				grants.release()
				// Prove that the grant writer has read the old membership and is
				// waiting for this user lock before permitting removal to commit.
				awaitGroupBlocker(t, ctx, backend, grantPID, memberPID)
				membership.release()
			} else {
				membership.release()
				awaitGroupBlocker(t, ctx, backend, memberPID, grantPID)
				grants.release()
			}
			memberErr := awaitGroupResult(t, ctx, memberDone)
			grantErr := awaitGroupResult(t, ctx, grantDone)
			after, err := accounts.LoadPrincipal(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "addition after grant replacement":
				if memberErr != nil || grantErr != nil || after.AuthVersion != before.AuthVersion+1 || !slices.Equal(after.Permissions, []string{newPermissionName}) {
					t.Fatal("concurrent addition missed committed grants/version", memberErr, grantErr, before, after)
				}
			case "removal while grant writer waits":
				if memberErr != nil || grantErr != nil || after.AuthVersion != before.AuthVersion+2 || len(after.Permissions) != 0 {
					t.Fatal("concurrent removal retained grants or lost invalidation", memberErr, grantErr, before, after)
				}
			case "opposite locks roll back a deadlock":
				if (memberErr == nil) == (grantErr == nil) {
					t.Fatal("forced opposite locks did not yield one rollback", memberErr, grantErr)
				}
				loser := memberErr
				if loser == nil {
					loser = grantErr
				}
				var databaseError *db.Error
				if !errors.As(loser, &databaseError) || databaseError.Code != db.Deadlock {
					t.Fatal("deadlock was not typed for whole-operation retry", loser)
				}
				if grantErr != nil {
					if after.AuthVersion != before.AuthVersion || !slices.Equal(after.Permissions, []string{oldPermissionName}) {
						t.Fatal("losing grant transaction leaked partial changes", before, after)
					}
					if err := accounts.SetGroupPermissions(ctx, group.ID, []int64{newGrant.ID}); err != nil {
						t.Fatal("explicit whole-operation retry failed", err)
					}
					after, err = accounts.LoadPrincipal(ctx, user.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
				if after.AuthVersion != before.AuthVersion+1 || !slices.Equal(after.Permissions, []string{newPermissionName}) {
					t.Fatal("grant/version state did not match the durable winner", before, after)
				}
			}
		})
	}
}

func awaitGroupLock(t *testing.T, ctx context.Context, held <-chan int) int {
	t.Helper()
	select {
	case pid := <-held:
		return pid
	case <-ctx.Done():
		t.Fatal("account mutation did not acquire its initial lock", ctx.Err())
		return 0
	}
}

func awaitGroupResult(t *testing.T, ctx context.Context, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		t.Fatal("concurrent account mutation did not terminate", ctx.Err())
		return ctx.Err()
	}
}

func awaitGroupBlocker(t *testing.T, ctx context.Context, backend db.Backend, blocked, blocker int) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := db.QueryRow(ctx, backend, "SELECT $1::int = ANY(pg_blocking_pids($2::int))", []any{blocker, blocked}, &waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("expected PostgreSQL lock dependency did not form", ctx.Err())
		}
	}
}
