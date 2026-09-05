package integration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestPostgresAPITokensScopeRevocationAndCommitOutcomes(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	registry := &models.Registry{}
	for _, schema := range append(append(auth.Schemas(), auth.TokenSchemas()...), (&contenttypes.ContentType{}).Schema(), (&genericAsset{}).Schema()) {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, registry)
	runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: append(append(contenttypes.Migrations(), auth.Migrations()...), auth.TokenMigrations()...)}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := contenttypes.Sync(ctx, store, registry, nil); err != nil {
		t.Fatal(err)
	}
	if err := auth.SyncPermissions(ctx, store, registry, nil); err != nil {
		t.Fatal(err)
	}
	operator := auth.WithPrincipal(ctx, auth.Principal{ID: "fixture-operator", Authenticated: true, Active: true})
	accountConfig := auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, _ auth.AccountChange) error {
		if auth.FromContext(ctx).ID != "fixture-operator" {
			return auth.ErrPermissionDenied
		}
		return nil
	}}
	accounts, err := auth.NewAccounts(accountConfig)
	if err != nil {
		t.Fatal(err)
	}
	user, err := accounts.CreateUser(operator, "token-identity", "fixture password value", auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	permission, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).Filter(orm.Q("codename", "view_asset")).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetUserPermissions(operator, user.ID, []int64{permission.ID}); err != nil {
		t.Fatal(err)
	}
	config := auth.TokensConfig{Accounts: accounts, Authorize: func(ctx context.Context, _ auth.TokenChange) error {
		if auth.FromContext(ctx).ID != "fixture-operator" {
			return auth.ErrPermissionDenied
		}
		return nil
	}}
	service, err := auth.NewTokens(config)
	if err != nil {
		t.Fatal(err)
	}
	issue := func() auth.TokenIssue {
		t.Helper()
		result, err := service.Issue(operator, user.ID, []string{"catalog.view_asset"})
		if err != nil || result.State != auth.TokenChanged || result.Secret.Reveal() == "" {
			t.Fatal("token issuance failed", result.State, err)
		}
		return result
	}
	first := issue()
	row, err := orm.For(store, func() *auth.APITokenRecord { return &auth.APITokenRecord{} }).Filter(orm.Q("id", first.ID)).Get(ctx)
	if err != nil || row.AuthVersion != 2 || row.RevokedAt != nil || len(row.SecretDigest) != 64 || strings.Contains(row.SecretDigest, first.Secret.Reveal()[42:]) || row.ExpiresAt.Sub(row.CreatedAt) != 24*time.Hour {
		t.Fatal("stored token invariant", err)
	}
	principal, err := service.AuthenticateToken(ctx, first.Secret.Reveal())
	if err != nil || principal.ID != user.ID {
		t.Fatal("issued token authentication", err)
	}
	policy := auth.ModelPolicy{AllowSuperuser: true}
	resource := auth.Resource{App: "catalog", Model: "Asset"}
	if policy.Authorize(ctx, principal, "view", resource) != nil || policy.Authorize(ctx, principal, "delete", resource) == nil {
		t.Fatal("scope/grant intersection wrong")
	}
	for _, attempt := range []struct {
		ctx    context.Context
		scopes []string
	}{{ctx, []string{"catalog.view_asset"}}, {operator, nil}, {operator, []string{"catalog.delete_asset"}}} {
		result, err := service.Issue(attempt.ctx, user.ID, attempt.scopes)
		if err == nil || result.State != auth.TokenUnchanged || result.Secret.Reveal() != "" {
			t.Fatal("unauthorized token issued")
		}
	}
	if err := db.Atomic(ctx, backend, db.AtomicOptions{}, func(ctx context.Context) error {
		if result, err := service.Issue(ctx, user.ID, []string{"catalog.view_asset"}); err == nil || result.Secret.Reveal() != "" {
			t.Fatal("secret escaped uncommitted outer transaction")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The HTTP boundary never falls back to a supplied cookie identity.
	middleware, err := auth.BearerMiddleware(service)
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := policy.Authorize(r.Context(), auth.FromContext(r.Context()), "view", resource); err != nil {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(204)
	}))
	call := func(token string) int {
		r := httptest.NewRequest("GET", "https://example.test/assets", nil).WithContext(auth.WithPrincipal(ctx, auth.Principal{ID: "cookie-root", Active: true, Authenticated: true, Superuser: true}))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if call(first.Secret.Reveal()) != 204 || call("malformed") != 401 {
		t.Fatal("bearer HTTP routing failure")
	}
	if state, err := service.Revoke(ctx, first.ID); !errors.Is(err, auth.ErrPermissionDenied) || state != auth.TokenUnchanged {
		t.Fatal("unauthorized revocation", state, err)
	}
	// Concurrent exact-row revocations have one changed outcome and one no-op.
	var wait sync.WaitGroup
	states := make(chan auth.TokenMutationState, 2)
	errs := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			state, err := service.Revoke(operator, first.ID)
			states <- state
			errs <- err
		}()
	}
	wait.Wait()
	close(states)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	changed := 0
	for state := range states {
		if state == auth.TokenChanged {
			changed++
		} else if state != auth.TokenUnchanged {
			t.Fatal("unexpected revocation state")
		}
	}
	if changed != 1 || call(first.Secret.Reveal()) != 401 {
		t.Fatal("revocation replay or HTTP fallback")
	}
	noOpConfig := config
	noOpConfig.Authorize = func(ctx context.Context, change auth.TokenChange) error {
		if err := config.Authorize(ctx, change); err != nil {
			return err
		}
		return db.OnCommit(ctx, backend.Alias(), func(context.Context) error { return errors.New("fixture no-op callback failed") }, false)
	}
	noOpService, err := auth.NewTokens(noOpConfig)
	if err != nil {
		t.Fatal(err)
	}
	state, noOpErr := noOpService.Revoke(operator, first.ID)
	var noOpCallback *db.CommittedCallbackError
	if state != auth.TokenUnchanged || !errors.As(noOpErr, &noOpCallback) {
		t.Fatal("no-op callback failure claimed a token mutation", state, noOpErr)
	}
	second := issue()
	if err := accounts.SetUserPermissions(operator, user.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateToken(ctx, second.Secret.Reveal()); !errors.Is(err, auth.ErrToken) {
		t.Fatal("grant change failed to invalidate token", err)
	}
	if err := accounts.SetUserPermissions(operator, user.ID, []int64{permission.ID}); err != nil {
		t.Fatal(err)
	}
	// Unusable password is not a reason to reject a separate API credential.
	if err := accounts.SetUnusablePassword(operator, user.ID); err != nil {
		t.Fatal(err)
	}
	third := issue()
	if _, err := service.AuthenticateToken(ctx, third.Secret.Reveal()); err != nil {
		t.Fatal("API-only identity rejected", err)
	}
	if _, err := backend.Exec(ctx, "UPDATE gogo_api_tokens SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", third.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateToken(ctx, third.Secret.Reveal()); !errors.Is(err, auth.ErrToken) {
		t.Fatal("expired token accepted", err)
	}
	// Hook mutation of the approved token fields or account rolls back issuance.
	for _, phase := range []string{"before_scopes", "after_scopes", "before_user", "after_user"} {
		t.Run(phase, func(t *testing.T) {
			before, _ := orm.For(store, func() *auth.APITokenRecord { return &auth.APITokenRecord{} }).Count(ctx)
			prior, err := accounts.LoadPrincipal(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			hook := func(ctx context.Context, event orm.SaveEvent) error {
				record, ok := event.Model.(*auth.APITokenRecord)
				if !ok {
					return nil
				}
				if strings.HasSuffix(phase, "scopes") {
					record.Scopes[0] = "catalog.delete_asset"
					return nil
				}
				return accounts.SetAccountFlags(ctx, user.ID, true, true, false)
			}
			if strings.HasPrefix(phase, "before") {
				store.BeforeSave = []orm.SaveReceiver{hook}
			} else {
				store.AfterSave = []orm.SaveReceiver{hook}
			}
			result, err := service.Issue(operator, user.ID, []string{"catalog.view_asset"})
			store.BeforeSave, store.AfterSave = nil, nil
			if err == nil || result.State != auth.TokenUnchanged || result.Secret.Reveal() != "" {
				t.Fatal("hook altered issued authority", result.State, err)
			}
			after, _ := orm.For(store, func() *auth.APITokenRecord { return &auth.APITokenRecord{} }).Count(ctx)
			current, loadErr := accounts.LoadPrincipal(ctx, user.ID)
			if before != after || loadErr != nil || current.AuthVersion != prior.AuthVersion {
				t.Fatal("rejected hook escaped rollback", loadErr)
			}
		})
	}
	for _, phase := range []string{"before", "after"} {
		t.Run(phase+"_authority", func(t *testing.T) {
			allowed := true
			restrictedConfig := config
			restrictedConfig.Authorize = func(ctx context.Context, change auth.TokenChange) error {
				if !allowed {
					return auth.ErrPermissionDenied
				}
				return config.Authorize(ctx, change)
			}
			restricted, err := auth.NewTokens(restrictedConfig)
			if err != nil {
				t.Fatal(err)
			}
			before, err := orm.For(store, func() *auth.APITokenRecord { return &auth.APITokenRecord{} }).Count(ctx)
			if err != nil {
				t.Fatal(err)
			}
			hook := func(_ context.Context, event orm.SaveEvent) error {
				if _, ok := event.Model.(*auth.APITokenRecord); ok {
					allowed = false
				}
				return nil
			}
			if phase == "before" {
				store.BeforeSave = []orm.SaveReceiver{hook}
			} else {
				store.AfterSave = []orm.SaveReceiver{hook}
			}
			result, err := restricted.Issue(operator, user.ID, []string{"catalog.view_asset"})
			store.BeforeSave, store.AfterSave = nil, nil
			if !errors.Is(err, auth.ErrPermissionDenied) || result.State != auth.TokenUnchanged || result.Secret.Reveal() != "" {
				t.Fatal("revoked issuance authority was ignored", phase, result.State, err)
			}
			after, err := orm.For(store, func() *auth.APITokenRecord { return &auth.APITokenRecord{} }).Count(ctx)
			if err != nil || after != before {
				t.Fatal("rejected authority retained issued token", err)
			}
		})
	}
	store.AfterSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
		if _, ok := event.Model.(*auth.APITokenRecord); !ok {
			return nil
		}
		return db.OnCommit(ctx, backend.Alias(), func(context.Context) error { return errors.New("fixture post-commit failure") }, false)
	}}
	committed, err := service.Issue(operator, user.ID, []string{"catalog.view_asset"})
	store.AfterSave = nil
	var callback *db.CommittedCallbackError
	if !errors.As(err, &callback) || committed.State != auth.TokenChanged || committed.ID == "" || committed.Secret.Reveal() != "" {
		t.Fatal("committed error leaked credential or claimed rollback", committed.State, err)
	}
	uncertainConfig := accountConfig
	uncertainConfig.Store = orm.New(unknownAccountCommitBackend{backend}, registry)
	uncertainAccounts, err := auth.NewAccounts(uncertainConfig)
	if err != nil {
		t.Fatal(err)
	}
	unknownConfig := config
	unknownConfig.Accounts = uncertainAccounts
	uncertain, err := auth.NewTokens(unknownConfig)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := uncertain.Issue(operator, user.ID, []string{"catalog.view_asset"})
	if !db.IsCode(err, db.UnknownCommit) || unknown.State != auth.TokenChangeUnknown || unknown.Secret.Reveal() != "" {
		t.Fatal("unknown commit leaked credential or claimed rollback", unknown.State, err)
	}
	state, err = uncertain.Revoke(operator, first.ID)
	if !db.IsCode(err, db.UnknownCommit) || state != auth.TokenUnchanged {
		t.Fatal("uncertain no-op claimed token mutation", state, err)
	}
}
