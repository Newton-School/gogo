package integration

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

type unknownAccountCommitBackend struct{ db.Backend }
type unknownAccountCommitTx struct{ db.Transaction }

func (b unknownAccountCommitBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return unknownAccountCommitTx{tx}, nil
}
func (tx unknownAccountCommitTx) Commit() error {
	if err := tx.Transaction.Commit(); err != nil {
		return err
	}
	return &db.Error{Code: db.UnknownCommit, Message: "commit outcome unknown"}
}

func TestPostgresSelfPasswordChangeRequiresCurrentProof(t *testing.T) {
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
	config := auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, change auth.AccountChange) error {
		p := auth.FromContext(ctx)
		if p.ID == "fixture-operator" || p.Authenticated && p.Active && change.Action == "change_own_password" && change.UserID == p.ID {
			return nil
		}
		return auth.ErrPermissionDenied
	}}
	accounts, err := auth.NewAccounts(config)
	if err != nil {
		t.Fatal(err)
	}
	operator := auth.WithPrincipal(ctx, auth.Principal{ID: "fixture-operator", Active: true, Authenticated: true})
	user, err := accounts.CreateUser(operator, "self-password", "  fixture old password  ", auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewAuthenticator(accounts, 2)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := authenticator.Authenticate(ctx, user.Identifier, "  fixture old password  ")
	if err != nil {
		t.Fatal(err)
	}
	self := auth.WithPrincipal(ctx, principal)
	for _, scenario := range []struct {
		name, old, new string
		want           error
	}{
		{"wrong_old", "wrong password", "fixture new password", auth.ErrCredentials},
		{"whitespace_significant", "fixture old password", "fixture new password", auth.ErrCredentials},
		{"policy_rejected", "  fixture old password  ", "short", nil},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			result, err := accounts.ChangeOwnPassword(self, scenario.old, scenario.new)
			if err == nil || scenario.want != nil && !errors.Is(err, scenario.want) || result.State != auth.PasswordUnchanged || result.Principal.ID != "" {
				t.Fatal(result.State, err)
			}
			current, err := accounts.LoadPrincipal(ctx, user.ID)
			if err != nil || current.AuthVersion != 1 {
				t.Fatal("failed proof changed version", err)
			}
		})
	}
	if result, err := accounts.ChangeOwnPassword(ctx, "  fixture old password  ", "fixture new password"); !errors.Is(err, auth.ErrUnauthenticated) || result.State != auth.PasswordUnchanged {
		t.Fatal("anonymous change", err)
	}
	if err := db.Atomic(self, backend, db.AtomicOptions{}, func(txctx context.Context) error {
		result, err := accounts.ChangeOwnPassword(txctx, "  fixture old password  ", "fixture new password")
		if err == nil || result.State != auth.PasswordUnchanged {
			t.Fatal("uncommitted principal returned")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := accounts.ChangeOwnPassword(self, "  fixture old password  ", "  fixture replacement password  ")
	if err != nil || result.State != auth.PasswordChanged || result.Principal.ID != user.ID || result.Principal.AuthVersion != 2 {
		t.Fatal(result.State, err)
	}
	if _, err := authenticator.Authenticate(ctx, user.Identifier, "  fixture old password  "); !errors.Is(err, auth.ErrCredentials) {
		t.Fatal("old password survived", err)
	}
	if _, err := authenticator.Authenticate(ctx, user.Identifier, "  fixture replacement password  "); err != nil {
		t.Fatal(err)
	}
	if result, err := accounts.ChangeOwnPassword(self, "  fixture replacement password  ", "fixture third password"); !errors.Is(err, auth.ErrAccountChanged) || result.State != auth.PasswordUnchanged {
		t.Fatal("stale session verified a new credential", err)
	}
	// A post-commit failure is a changed credential, never a rolled-back one.
	store.AfterSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
		if _, ok := event.Model.(*auth.User); !ok {
			return nil
		}
		return db.OnCommit(ctx, backend.Alias(), func(context.Context) error { return errors.New("fixture post-commit failure") }, false)
	}}
	fresh := auth.WithPrincipal(ctx, result.Principal)
	changed, err := accounts.ChangeOwnPassword(fresh, "  fixture replacement password  ", "fixture third password")
	var committed *db.CommittedCallbackError
	if !errors.As(err, &committed) || changed.State != auth.PasswordChanged || changed.Principal.ID != "" {
		t.Fatal("commit failure mislabeled", changed.State, err)
	}
	store.AfterSave = nil
	if p, err := authenticator.Authenticate(ctx, user.Identifier, "fixture third password"); err != nil || p.AuthVersion != 3 {
		t.Fatal("durable password change lost", err)
	}
	current, err := authenticator.Authenticate(ctx, user.Identifier, "fixture third password")
	if err != nil {
		t.Fatal(err)
	}
	currentContext := auth.WithPrincipal(ctx, current)
	permit := config.Authorize
	checks := 0
	config.Authorize = func(ctx context.Context, change auth.AccountChange) error {
		if err := permit(ctx, change); err != nil {
			return err
		}
		if change.Action == "change_own_password" {
			checks++
			if checks == 2 {
				return accounts.ChangePassword(operator, user.ID, "fixture concurrent password")
			}
		}
		return nil
	}
	racing, err := auth.NewAccounts(config)
	if err != nil {
		t.Fatal(err)
	}
	raced, err := racing.ChangeOwnPassword(currentContext, "fixture third password", "fixture denied replacement")
	if !errors.Is(err, auth.ErrAccountChanged) || raced.State != auth.PasswordUnchanged {
		t.Fatal("verification survived intervening replacement", raced.State, err)
	}
	current, err = authenticator.Authenticate(ctx, user.Identifier, "fixture concurrent password")
	if err != nil || current.AuthVersion != 4 {
		t.Fatal("raced operation overwrote concurrent credential", err)
	}
	currentContext = auth.WithPrincipal(ctx, current)
	store.Backend = unknownAccountCommitBackend{Backend: backend}
	uncertain, err := accounts.ChangeOwnPassword(currentContext, "fixture concurrent password", "fixture final password")
	store.Backend = backend
	if !db.IsCode(err, db.UnknownCommit) || uncertain.State != auth.PasswordChangeUnknown || uncertain.Principal.ID != "" {
		t.Fatal("unknown commit asserted unchanged or granted session", uncertain.State, err)
	}
	if p, err := authenticator.Authenticate(ctx, user.Identifier, "fixture final password"); err != nil || p.AuthVersion != 5 {
		t.Fatal("fixture did not actually commit unknown outcome", err)
	}
	current, err = authenticator.Authenticate(ctx, user.Identifier, "fixture final password")
	if err != nil {
		t.Fatal(err)
	}
	currentContext = auth.WithPrincipal(ctx, current)
	for _, phase := range []string{"post_lock_authorize", "password_validator"} {
		t.Run(phase, func(t *testing.T) {
			mutatingConfig := config
			checks := 0
			mutatingConfig.Authorize = func(ctx context.Context, change auth.AccountChange) error {
				if err := permit(ctx, change); err != nil {
					return err
				}
				if change.Action == "change_own_password" {
					checks++
					if phase == "post_lock_authorize" && checks == 3 {
						return accounts.ChangePassword(auth.WithPrincipal(ctx, auth.FromContext(operator)), user.ID, "fixture callback password")
					}
				}
				return nil
			}
			if phase == "password_validator" {
				mutatingConfig.PasswordValidators = []auth.PasswordValidator{func(ctx context.Context, _ string, _ auth.Principal) error {
					return accounts.ChangePassword(auth.WithPrincipal(ctx, auth.FromContext(operator)), user.ID, "fixture callback password")
				}}
			}
			mutating, err := auth.NewAccounts(mutatingConfig)
			if err != nil {
				t.Fatal(err)
			}
			result, err := mutating.ChangeOwnPassword(currentContext, "fixture final password", "fixture denied callback replacement")
			if !errors.Is(err, auth.ErrAccountChanged) || result.State != auth.PasswordUnchanged || result.Principal.ID != "" {
				t.Fatal("nested callback replaced proof without detection", phase, result.State, err)
			}
			if p, err := authenticator.Authenticate(ctx, user.Identifier, "fixture final password"); err != nil || p.AuthVersion != 5 {
				t.Fatal("callback and outer changes were not atomically rolled back", phase, err)
			}
		})
	}
	if _, err := contenttypes.Sync(ctx, store, registry, nil); err != nil {
		t.Fatal(err)
	}
	if err := auth.SyncPermissions(ctx, store, registry, nil); err != nil {
		t.Fatal(err)
	}
	permission, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).Filter(orm.Q("codename", "view_user")).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetUserPermissions(operator, user.ID, []int64{permission.ID}); err != nil {
		t.Fatal(err)
	}
	current, err = authenticator.Authenticate(ctx, user.Identifier, "fixture final password")
	if err != nil {
		t.Fatal(err)
	}
	immutableConfig := config
	immutableConfig.Authorize = permit
	immutableConfig.PasswordValidators = []auth.PasswordValidator{func(_ context.Context, _ string, p auth.Principal) error {
		if len(p.Permissions) != 1 {
			t.Fatal("fixture principal needs a real grant")
		}
		p.Permissions[0] = "gogo_auth.delete_user"
		return nil
	}}
	immutable, err := auth.NewAccounts(immutableConfig)
	if err != nil {
		t.Fatal(err)
	}
	safe, err := immutable.ChangeOwnPassword(auth.WithPrincipal(ctx, current), "fixture final password", "fixture immutable principal password")
	if err != nil || safe.State != auth.PasswordChanged || len(safe.Principal.Permissions) != 1 || safe.Principal.Permissions[0] != "gogo_auth.view_user" {
		t.Fatal("password validator forged returned principal", safe.State, err, safe.Principal.Permissions)
	}
	// The common account writer must also reject nested hook writes, rather
	// than overwriting their credential/version with an earlier snapshot.
	entered := false
	store.BeforeSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
		if _, ok := event.Model.(*auth.User); !ok || entered {
			return nil
		}
		entered = true
		return accounts.ChangePassword(auth.WithPrincipal(ctx, auth.FromContext(operator)), user.ID, "fixture hook replacement")
	}}
	err = accounts.ChangePassword(operator, user.ID, "fixture outer replacement")
	store.BeforeSave = nil
	if !errors.Is(err, auth.ErrAccountChanged) {
		t.Fatal("general writer reused stale hook snapshot", err)
	}
	if p, err := authenticator.Authenticate(ctx, user.Identifier, "fixture immutable principal password"); err != nil || p.AuthVersion != safe.Principal.AuthVersion {
		t.Fatal("nested hook change escaped rollback", err)
	}
	postPolicyConfig := config
	checks = 0
	postPolicyConfig.Authorize = func(ctx context.Context, change auth.AccountChange) error {
		if err := permit(ctx, change); err != nil {
			return err
		}
		if change.Action == "change_password" {
			checks++
			if checks == 3 {
				return accounts.SetAccountFlags(ctx, user.ID, true, true, false)
			}
		}
		return nil
	}
	postPolicy, err := auth.NewAccounts(postPolicyConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := postPolicy.ChangePassword(operator, user.ID, "fixture policy outer replacement"); !errors.Is(err, auth.ErrAccountChanged) {
		t.Fatal("general writer reused pre-policy user", err)
	}
	if p, err := authenticator.Authenticate(ctx, user.Identifier, "fixture immutable principal password"); err != nil || p.AuthVersion != safe.Principal.AuthVersion || p.Staff {
		t.Fatal("post-lock policy change escaped rollback", err)
	}
}
