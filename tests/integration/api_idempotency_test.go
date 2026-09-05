package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/orm"
)

type lostAPICommitBackend struct{ db.Backend }
type lostAPICommitTx struct{ db.Transaction }

func (b lostAPICommitBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return lostAPICommitTx{tx}, nil
}
func (tx lostAPICommitTx) Commit() error {
	if err := tx.Transaction.Commit(); err != nil {
		return err
	}
	return &db.Error{Code: db.UnknownCommit, Message: "synthetic lost commit acknowledgement"}
}

func TestPostgresAPIIdempotencyConcurrentReplayRollbackScopeAndExpiry(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "actor", Authenticated: true, Active: true, AuthVersion: 1})
	for _, schema := range append(api.IdempotencySchemas(), (&adminProduct{}).Schema()) {
		if err := backend.SchemaEditor().CreateModel(ctx, backend, schema); err != nil {
			t.Fatal(err)
		}
	}
	store := orm.New(backend, nil)
	var calls atomic.Int64
	var deny, hide, broaden, panicRedact atomic.Bool
	var nowOffset atomic.Int64
	now := time.Now().UTC()
	config := api.IdempotencyConfig{Backend: backend, Retention: time.Minute, Timeout: 10 * time.Second,
		Now: func() time.Time { return now.Add(time.Duration(nowOffset.Load())) },
		Authorize: func(ctx context.Context, scope, action string, key api.Values) error {
			if !db.InTransaction(ctx, backend.Alias()) {
				return errors.New("authorization outside transaction")
			}
			if deny.Load() || scope == "forbidden" {
				return auth.ErrPermissionDenied
			}
			// Mutating the isolated key must not retarget the stored operation.
			if key != nil {
				key["id"] = "callback-mutation"
			}
			return nil
		}, Redact: func(_ context.Context, _, _ string, _ api.Values, body api.Values) (api.Values, error) {
			if panicRedact.Load() {
				panic("private provider value")
			}
			if hide.Load() {
				delete(body, "name")
			}
			if broaden.Load() {
				body["injected"] = true
			}
			return body, nil
		}}
	service, err := api.NewIdempotency(config)
	if err != nil {
		t.Fatal(err)
	}
	operation := api.Operation{Scope: "tenant-one", Action: "shop.product.create.v1", Key: "client-operation-1", Input: api.Values{"name": "product", "number": json.Number("9007199254740993")}}
	mutate := func(ctx context.Context) (api.MutationResponse, error) {
		calls.Add(1)
		row := &adminProduct{Tenant: "one", Name: "product", Secret: "not serialized"}
		if err := store.Save(ctx, row, orm.SaveOptions{ForceInsert: true}); err != nil {
			return api.MutationResponse{}, err
		}
		return api.MutationResponse{Status: 201, Headers: map[string]string{"Location": fmt.Sprintf("/products/%d/", row.ID)}, Body: api.Values{"id": row.ID, "name": row.Name}, ObjectKey: api.Values{"id": row.ID}}, nil
	}
	results := make(chan api.IdempotencyResult, 12)
	errorsFound := make(chan error, 12)
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			result, err := service.Execute(ctx, operation, mutate)
			if err != nil {
				errorsFound <- err
			} else {
				results <- result
			}
		})
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal("concurrent idempotent request failed", err)
	}
	var location string
	replays := 0
	for result := range results {
		if result.Outcome != api.MutationCommitted || result.Response.Status != 201 {
			t.Fatal(result)
		}
		if result.Replayed {
			replays++
		}
		if location != "" && location != result.Response.Headers["Location"] {
			t.Fatal("different logical objects returned")
		}
		location = result.Response.Headers["Location"]
	}
	if calls.Load() != 1 || replays != 11 {
		t.Fatal("mutation repeated", calls.Load(), replays)
	}
	count := func() int64 {
		t.Helper()
		n, err := orm.For(store, func() *adminProduct { return &adminProduct{} }).Count(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 1 {
		t.Fatal("business row count")
	}
	// Numeric equivalence does not lose large integer precision.
	equivalent := operation
	equivalent.Input = api.Values{"number": json.Number("9007199254740993.00"), "name": "product"}
	if result, err := service.Execute(ctx, equivalent, mutate); err != nil || !result.Replayed {
		t.Fatal(result, err)
	}
	conflicting := operation
	conflicting.Input = api.Values{"number": json.Number("9007199254740992"), "name": "product"}
	if result, err := service.Execute(ctx, conflicting, mutate); ghttp.PublicError(err).Status != 409 || result.Outcome != api.MutationUnchanged || calls.Load() != 1 {
		t.Fatal("body conflict mutated", result, err)
	}
	deny.Store(true)
	if result, err := service.Execute(ctx, operation, mutate); !errors.Is(err, auth.ErrPermissionDenied) || result.Response.Body != nil {
		t.Fatal("denied replay disclosed data", result, err)
	}
	deny.Store(false)
	hide.Store(true)
	p := auth.FromContext(ctx)
	p.AuthVersion++
	p.Permissions = []string{"new-current-grant"}
	if result, err := service.Execute(auth.WithPrincipal(ctx, p), operation, mutate); err != nil || !result.Replayed || result.Response.Body["name"] != nil || calls.Load() != 1 {
		t.Fatal("current redaction/version did not retain original operation", result, err)
	}
	hide.Store(false)
	if result, err := service.Execute(ctx, operation, mutate); err != nil || result.Response.Body["name"] != "product" {
		t.Fatal("replay redaction changed stored response", result, err)
	}
	broaden.Store(true)
	if result, err := service.Execute(ctx, operation, mutate); err == nil || result.Response.Body != nil {
		t.Fatal("redactor broadened receipt", result, err)
	}
	broaden.Store(false)
	// Failed writes and invalid serialization roll back both records.
	for _, mode := range []string{"callback_error", "bad_response", "panic"} {
		candidate := operation
		candidate.Key = mode
		_, err := service.Execute(ctx, candidate, func(ctx context.Context) (api.MutationResponse, error) {
			response, err := mutate(ctx)
			if err != nil {
				return response, err
			}
			switch mode {
			case "callback_error":
				return response, errors.New("synthetic failure")
			case "bad_response":
				response.Body["bad"] = make(chan int)
			case "panic":
				panic("private callback panic")
			}
			return response, nil
		})
		if err == nil || count() != 1 {
			t.Fatal("failed mutation survived", mode, err)
		}
		if strings.Contains(err.Error(), "private") {
			t.Fatal("callback panic leaked")
		}
		if n, err := orm.For(store, func() *api.IdempotencyRecord { return &api.IdempotencyRecord{} }).Count(ctx); err != nil || n != 1 {
			t.Fatal("failed operation receipt survived", n, err)
		}
	}
	// One key intentionally creates independent operations across stable scopes.
	other := operation
	other.Scope = "tenant-two"
	if result, err := service.Execute(ctx, other, mutate); err != nil || result.Replayed {
		t.Fatal(result, err)
	}
	if count() != 2 {
		t.Fatal("scope identity not separated")
	}
	otherActor := auth.FromContext(ctx)
	otherActor.ID = "another-actor"
	if result, err := service.Execute(auth.WithPrincipal(ctx, otherActor), operation, mutate); err != nil || result.Replayed {
		t.Fatal(result, err)
	}
	if count() != 3 {
		t.Fatal("principal identity not separated")
	}
	nowOffset.Store(int64(2 * time.Minute))
	if result, err := service.Execute(ctx, operation, mutate); err != nil || result.Replayed {
		t.Fatal("expired key did not start documented new operation", result, err)
	}
	if count() != 4 {
		t.Fatal("expired reuse did not create new row")
	}
}

func TestPostgresAPIIdempotencyUnknownCommitAndCallbackFailureKeepSameOperation(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "actor", Authenticated: true, Active: true})
	for _, schema := range append(api.IdempotencySchemas(), (&adminProduct{}).Schema()) {
		if err := backend.SchemaEditor().CreateModel(ctx, backend, schema); err != nil {
			t.Fatal(err)
		}
	}
	store := orm.New(backend, nil)
	config := api.IdempotencyConfig{Backend: backend, Authorize: func(context.Context, string, string, api.Values) error { return nil }, Redact: func(_ context.Context, _, _ string, _ api.Values, body api.Values) (api.Values, error) {
		return body, nil
	}}
	service, err := api.NewIdempotency(config)
	if err != nil {
		t.Fatal(err)
	}
	uncertainConfig := config
	uncertainConfig.Backend = lostAPICommitBackend{backend}
	uncertain, err := api.NewIdempotency(uncertainConfig)
	if err != nil {
		t.Fatal(err)
	}
	operation := api.Operation{Scope: "tenant", Action: "create", Key: "uncertain", Input: api.Values{}}
	calls := 0
	mutate := func(ctx context.Context) (api.MutationResponse, error) {
		calls++
		row := &adminProduct{Tenant: "one", Name: "product", Secret: "private"}
		if err := store.Save(ctx, row, orm.SaveOptions{ForceInsert: true}); err != nil {
			return api.MutationResponse{}, err
		}
		return api.MutationResponse{Status: 201, Body: api.Values{"id": row.ID}, ObjectKey: api.Values{"id": row.ID}}, nil
	}
	result, err := uncertain.Execute(ctx, operation, mutate)
	if !db.IsCode(err, db.UnknownCommit) || result.Outcome != api.MutationUnknown || result.Response.Body != nil {
		t.Fatal("unknown commit reported as confirmed", result, err)
	}
	result, err = service.Execute(ctx, operation, mutate)
	if err != nil || !result.Replayed || result.Outcome != api.MutationCommitted || calls != 1 {
		t.Fatal("lost ack repeated business mutation", result, err, calls)
	}
	operation.Key = "committed-callback"
	result, err = service.Execute(ctx, operation, func(ctx context.Context) (api.MutationResponse, error) {
		if err := db.OnCommit(ctx, backend.Alias(), func(context.Context) error { return errors.New("synthetic post-commit failure") }, false); err != nil {
			return api.MutationResponse{}, err
		}
		return mutate(ctx)
	})
	var committed *db.CommittedCallbackError
	if !errors.As(err, &committed) || result.Outcome != api.MutationCommitted || result.Response.Status != 201 {
		t.Fatal("confirmed commit misreported", result, err)
	}
	if result, err := service.Execute(ctx, operation, mutate); err != nil || !result.Replayed || calls != 2 {
		t.Fatal("post-commit callback repeated mutation", result, err, calls)
	}
	if err := db.Atomic(ctx, backend, db.AtomicOptions{}, func(tx context.Context) error {
		_, err := service.Execute(tx, operation, mutate)
		if err == nil {
			t.Fatal("nested idempotency declared durable response")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A callback error may come from another transaction. The typed wrapper
	// does not confirm the outer mutation; never return its unsealed payload.
	operation.Key = "foreign-callback-error"
	result, err = service.Execute(ctx, operation, func(ctx context.Context) (api.MutationResponse, error) {
		if _, err := mutate(ctx); err != nil {
			return api.MutationResponse{}, err
		}
		return api.MutationResponse{Status: 201, Headers: map[string]string{"Set-Cookie": "private"}, Body: api.Values{"secret": "private"}}, &db.CommittedCallbackError{Errors: []error{errors.New("foreign callback failed")}}
	})
	if err == nil || result.Outcome != api.MutationUnchanged || result.Response.Body != nil || len(result.Response.Headers) != 0 {
		t.Fatal("foreign error confirmed rolled-back unsealed operation", result, err)
	}
	if n, err := orm.For(store, func() *adminProduct { return &adminProduct{} }).Count(ctx); err != nil || n != 2 {
		t.Fatal("foreign callback error escaped rollback", n, err)
	}
	operation.Key = "post-commit-panic"
	result, err = service.Execute(ctx, operation, func(ctx context.Context) (api.MutationResponse, error) {
		if err := db.OnCommit(ctx, backend.Alias(), func(context.Context) error { panic("private postcommit panic") }, false); err != nil {
			return api.MutationResponse{}, err
		}
		return mutate(ctx)
	})
	if result.Outcome != api.MutationCommitted || result.Response.Status != 201 || err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("postcommit panic confirmation lost", result, err)
	}
	if result, err := service.Execute(ctx, operation, mutate); err != nil || !result.Replayed || calls != 4 {
		t.Fatal("postcommit panic repeated business mutation", result, err, calls)
	}
	operation.Key = "numeric-response"
	result, err = service.Execute(ctx, operation, func(context.Context) (api.MutationResponse, error) {
		return api.MutationResponse{Status: 200, ObjectKey: api.Values{"id": json.Number("1e3")}, Body: api.Values{"integer": json.Number("1e3"), "decimal": json.Number("1.0000"), "large": json.Number("9007199254740993.0")}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(result.Response.Body)
	firstKey, _ := json.Marshal(result.Response.ObjectKey)
	result, err = service.Execute(ctx, operation, mutate)
	if err != nil || !result.Replayed {
		t.Fatal(result, err)
	}
	secondJSON, _ := json.Marshal(result.Response.Body)
	secondKey, _ := json.Marshal(result.Response.ObjectKey)
	if string(firstKey) != string(secondKey) || string(firstKey) != `{"id":1000}` {
		t.Fatal("JSONB changed operation identity", string(firstKey), string(secondKey))
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("JSONB changed sealed response", string(firstJSON), string(secondJSON))
	}
}

func TestPostgresAPIIdempotencyBoundsContendedLockWait(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "actor", Authenticated: true, Active: true})
	if err := backend.SchemaEditor().CreateModel(ctx, backend, api.IdempotencySchemas()[0]); err != nil {
		t.Fatal(err)
	}
	config := api.IdempotencyConfig{Backend: backend, Timeout: 100 * time.Millisecond, Authorize: func(context.Context, string, string, api.Values) error { return nil }, Redact: func(_ context.Context, _, _ string, _ api.Values, body api.Values) (api.Values, error) {
		return body, nil
	}}
	service, err := api.NewIdempotency(config)
	if err != nil {
		t.Fatal(err)
	}
	operation := api.Operation{Scope: "tenant", Action: "change", Key: "contended", Input: api.Values{}}
	mutate := func(context.Context) (api.MutationResponse, error) {
		return api.MutationResponse{Status: 200, ObjectKey: api.Values{"id": 1}, Body: api.Values{"id": 1}}, nil
	}
	if _, err := service.Execute(ctx, operation, mutate); err != nil {
		t.Fatal(err)
	}
	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- db.Atomic(ctx, backend, db.AtomicOptions{}, func(ctx context.Context) error {
			if _, err := orm.For(orm.New(backend, nil), func() *api.IdempotencyRecord { return &api.IdempotencyRecord{} }).SelectForUpdate(false, false).Get(ctx); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("lock fixture failed")
	}
	started := time.Now()
	result, err := service.Execute(ctx, operation, func(context.Context) (api.MutationResponse, error) {
		t.Error("lock timeout ran mutation")
		return api.MutationResponse{}, nil
	})
	if err == nil || result.Outcome != api.MutationUnchanged || result.Response.Body != nil || time.Since(started) > 3*time.Second {
		t.Fatal("lock wait unbounded or response disclosed", result, err)
	}
}
