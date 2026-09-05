package integration

import (
	"context"
	"errors"
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

type renameMutationGate struct {
	gate *groupMutationGate
	seen bool
}
type renameMutationGateKey struct{}

func TestPostgresGroupRenameMembershipConcurrency(t *testing.T) {
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
	accounts, err := auth.NewAccounts(auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, _ auth.AccountChange) error {
		state, _ := ctx.Value(renameMutationGateKey{}).(*renameMutationGate)
		if state == nil || state.seen || !db.InTransaction(ctx, backend.Alias()) {
			return nil
		}
		state.seen = true // authorization calls are serial within this request
		var pid int
		if err := db.QueryRow(ctx, db.ExecutorFor(ctx, backend), "SELECT pg_backend_pid()", nil, &pid); err != nil {
			return err
		}
		select {
		case state.gate.held <- pid:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-state.gate.proceed:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"join after rename", "opposite locks fail atomically", "removal invalidates rename snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			user, err := accounts.CreateUserWithoutPassword(ctx, scenario, auth.CreateUserOptions{})
			if err != nil {
				t.Fatal(err)
			}
			group, err := accounts.CreateGroup(ctx, scenario)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "join after rename" {
				if err := accounts.SetUserGroups(ctx, user.ID, []string{group.ID}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := accounts.LoadPrincipal(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			rename, member := newGroupMutationGate(), newGroupMutationGate()
			defer rename.release()
			defer member.release()
			renameDone, memberDone := make(chan error, 1), make(chan error, 1)
			go func() {
				renameDone <- accounts.RenameGroup(context.WithValue(ctx, renameMutationGateKey{}, &renameMutationGate{gate: rename}), group.ID, scenario+" renamed")
			}()
			renamePID := awaitGroupLock(t, ctx, rename.held)
			go func() {
				ids := []string{group.ID}
				if scenario == "removal invalidates rename snapshot" {
					ids = nil
				}
				memberDone <- accounts.SetUserGroups(context.WithValue(ctx, renameMutationGateKey{}, &renameMutationGate{gate: member}), user.ID, ids)
			}()
			memberPID := awaitGroupLock(t, ctx, member.held)
			if scenario == "removal invalidates rename snapshot" {
				rename.release()
				awaitGroupBlocker(t, ctx, backend, renamePID, memberPID)
				member.release()
			} else {
				member.release()
				awaitGroupBlocker(t, ctx, backend, memberPID, renamePID)
				rename.release()
			}
			renameErr, memberErr := awaitGroupResult(t, ctx, renameDone), awaitGroupResult(t, ctx, memberDone)
			if scenario == "join after rename" {
				if renameErr != nil || memberErr != nil {
					t.Fatal("serial join/rename failed", renameErr, memberErr)
				}
			} else if scenario == "removal invalidates rename snapshot" {
				if !errors.Is(renameErr, auth.ErrAccountChanged) || memberErr != nil {
					t.Fatal("stale membership snapshot accepted", renameErr, memberErr)
				}
				if err := accounts.RenameGroup(ctx, group.ID, scenario+" renamed"); err != nil {
					t.Fatal("explicit stale rename retry failed", err)
				}
			} else {
				if (renameErr == nil) == (memberErr == nil) {
					t.Fatal("opposite locks did not yield exactly one rollback", renameErr, memberErr)
				}
				loser := renameErr
				if loser == nil {
					loser = memberErr
				}
				if !db.IsCode(loser, db.Deadlock) {
					t.Fatal("opposite locks lost typed deadlock", loser)
				}
				if renameErr != nil {
					if err := accounts.RenameGroup(ctx, group.ID, scenario+" renamed"); err != nil {
						t.Fatal("explicit deadlock retry failed", err)
					}
				}
			}
			after, err := accounts.LoadPrincipal(ctx, user.ID)
			if err != nil || after.AuthVersion != before.AuthVersion+1 {
				t.Fatal("rename/membership invalidation lost or duplicated", err, before.AuthVersion, after.AuthVersion)
			}
			stored, err := orm.For(store, func() *auth.Group { return &auth.Group{} }).Filter(orm.Q("id", group.ID)).Get(ctx)
			if err != nil || stored.Name != scenario+" renamed" {
				t.Fatal("group name did not match durable outcome", err)
			}
		})
	}
}
