package integration_test

import (
	"context"
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
	"github.com/Newton-School/gogo/core/orm"
)

func pruneFixture(t *testing.T) (context.Context, db.Backend, api.IdempotencyConfig, func(int, time.Time, int), func() int64) {
	t.Helper()
	backend := testservice.Postgres(t)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "maintenance", Authenticated: true, Active: true})
	if err := backend.SchemaEditor().CreateModel(ctx, backend, api.IdempotencySchemas()[0]); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, nil)
	now := time.Now().UTC().Truncate(time.Microsecond)
	config := api.IdempotencyConfig{Backend: backend, Timeout: time.Second, Now: func() time.Time { return now },
		Authorize: func(context.Context, string, string, api.Values) error { return nil },
		Redact: func(_ context.Context, _, _ string, _ api.Values, body api.Values) (api.Values, error) {
			return body, nil
		},
		AuthorizePrune: func(ctx context.Context) error {
			if !db.InTransaction(ctx, backend.Alias()) || auth.FromContext(ctx).ID != "maintenance" {
				return auth.ErrPermissionDenied
			}
			return nil
		},
	}
	insert := func(id int, expires time.Time, status int) {
		t.Helper()
		row := &api.IdempotencyRecord{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", id),
			ScopeHash: fmt.Sprintf("%064d", id), Action: "fixture", KeyDigest: fmt.Sprintf("%064d", id), RequestDigest: strings.Repeat("0", 64),
			ResponseStatus: status, ResponseHeaders: map[string]string{}, ResponseBody: map[string]any{"private": "synthetic receipt"}, ObjectKey: map[string]any{"id": id},
			CreatedAt: expires.Add(-time.Hour), ExpiresAt: expires,
		}
		if err := store.Save(ctx, row, orm.SaveOptions{ForceInsert: true}); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int64 {
		t.Helper()
		n, err := orm.For(store, func() *api.IdempotencyRecord { return &api.IdempotencyRecord{} }).Count(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	return ctx, backend, config, insert, count
}

type metadataPruneBackend struct{ db.Backend }
type metadataPruneTx struct{ db.Transaction }

func (b metadataPruneBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return metadataPruneTx{tx}, nil
}
func (tx metadataPruneTx) Query(ctx context.Context, query string, args ...any) (db.Rows, error) {
	for _, private := range []string{"response_body", "response_headers", "object_key", "key_digest", "scope_hash", "request_digest"} {
		if strings.Contains(query, private) {
			return nil, errors.New("maintenance read private receipt data")
		}
	}
	return tx.Transaction.Query(ctx, query, args...)
}

type waitingPruneBackend struct {
	db.Backend
	pid chan int
}
type waitingPruneTx struct {
	db.Transaction
	pid chan int
}

func (b waitingPruneBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return waitingPruneTx{tx, b.pid}, nil
}
func (tx waitingPruneTx) Query(ctx context.Context, query string, args ...any) (db.Rows, error) {
	if strings.Contains(query, "gogo_idempotency") {
		var pid int
		if err := db.QueryRow(ctx, tx.Transaction, "SELECT pg_backend_pid()", nil, &pid); err != nil {
			return nil, err
		}
		select {
		case tx.pid <- pid:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return tx.Transaction.Query(ctx, query, args...)
}

func TestPostgresAPIReceiptPruneBoundsExpiryMetadataAndConcurrentBatches(t *testing.T) {
	ctx, backend, config, insert, count := pruneFixture(t)
	cutoff := config.Now()
	for id := 1; id <= 9; id++ {
		insert(id, cutoff.Add(-time.Duration(10-id)*time.Minute), 200)
	}
	insert(10, cutoff, 204)
	insert(11, cutoff.Add(time.Microsecond), 201)
	insert(12, cutoff.Add(-time.Hour), 0)
	insert(13, cutoff.Add(-time.Hour), 500)
	config.Backend = metadataPruneBackend{backend}
	service, err := api.NewIdempotency(config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Prune(ctx, 2)
	if err != nil || result.Outcome != api.MutationCommitted || result.Deleted != 2 || count() != 11 {
		t.Fatal(result, err)
	}
	// Stable oldest-first selection, with no private JSON projection.
	var oldest int64
	if err := db.QueryRow(ctx, backend, "SELECT count(*) FROM gogo_idempotency WHERE id IN ($1,$2)", []any{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"}, &oldest); err != nil || oldest != 0 {
		t.Fatal(oldest, err)
	}
	var total atomic.Int64
	errorsFound := make(chan error, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			result, err := service.Prune(ctx, 2)
			if err != nil {
				errorsFound <- err
				return
			}
			if result.Outcome != api.MutationCommitted || result.Deleted > 2 {
				errorsFound <- errors.New("invalid confirmed batch outcome")
				return
			}
			total.Add(result.Deleted)
		})
	}
	workers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	// Competing transactions can observe short batches; continue bounded passes.
	for count() > 3 {
		result, err := service.Prune(ctx, 2)
		if err != nil || result.Deleted == 0 {
			t.Fatal("expired rows failed to make progress", result, err)
		}
		total.Add(result.Deleted)
	}
	if total.Load() != 8 || count() != 3 {
		t.Fatal("duplicate counts or live/unsealed rows removed", total.Load(), count())
	}
	if result, err := service.Prune(ctx, 0); err != nil || result.Deleted != 0 || result.Outcome != api.MutationCommitted {
		t.Fatal(result, err)
	}
}

func TestPostgresAPIReceiptPruneAuthorityRollbackAndDurableOutcomes(t *testing.T) {
	for _, mode := range []string{"deny", "revoke_after_lock", "panic", "foreign_committed", "foreign_unknown", "canceled", "postcommit", "postcommit_panic", "lost_ack", "invalid_clock", "clock_panic", "nested"} {
		t.Run(mode, func(t *testing.T) {
			ctx, backend, config, insert, count := pruneFixture(t)
			insert(1, config.Now().Add(-time.Hour), 200)
			originalPermit := config.AuthorizePrune
			var checks int
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			config.AuthorizePrune = func(ctx context.Context) error {
				checks++
				if err := originalPermit(ctx); err != nil {
					return err
				}
				switch mode {
				case "deny":
					return auth.ErrPermissionDenied
				case "revoke_after_lock":
					if checks == 2 {
						return auth.ErrPermissionDenied
					}
				case "panic":
					panic("private maintenance detail")
				case "foreign_committed":
					return &db.CommittedCallbackError{Errors: []error{errors.New("foreign failure")}}
				case "foreign_unknown":
					return &db.Error{Code: db.UnknownCommit, Message: "foreign failure"}
				case "canceled":
					cancel()
				case "postcommit", "postcommit_panic":
					if checks == 1 {
						return db.OnCommit(ctx, backend.Alias(), func(context.Context) error {
							if mode == "postcommit_panic" {
								panic("private postcommit detail")
							}
							return errors.New("synthetic after-commit failure")
						}, false)
					}
				}
				return nil
			}
			if mode == "lost_ack" {
				config.Backend = lostAPICommitBackend{backend}
			}
			if mode == "invalid_clock" {
				config.Now = func() time.Time { return time.Time{} }
			}
			if mode == "clock_panic" {
				config.Now = func() time.Time { panic("private clock detail") }
			}
			service, err := api.NewIdempotency(config)
			if err != nil {
				t.Fatal(err)
			}
			var result api.PruneResult
			if mode == "nested" {
				err = db.Atomic(ctx, backend, db.AtomicOptions{}, func(ctx context.Context) error { result, err = service.Prune(ctx, 1); return err })
			} else {
				result, err = service.Prune(ctx, 1)
			}
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("failure lost or panic detail exposed", err)
			}
			switch mode {
			case "postcommit", "postcommit_panic":
				if result.Outcome != api.MutationCommitted || result.Deleted != 1 || count() != 0 {
					t.Fatal(result, count())
				}
			case "lost_ack":
				if result.Outcome != api.MutationUnknown || result.Deleted != 0 || count() != 0 {
					t.Fatal(result, count())
				}
				config.Backend, config.AuthorizePrune = backend, originalPermit
				service, _ = api.NewIdempotency(config)
				if retry, err := service.Prune(ctx, 1); err != nil || retry.Deleted != 0 || retry.Outcome != api.MutationCommitted {
					t.Fatal(retry, err)
				}
			default:
				if result.Outcome != api.MutationUnchanged || result.Deleted != 0 || count() != 1 {
					t.Fatal(result, count())
				}
			}
		})
	}
}

func TestPostgresAPIReceiptPruneWaitsForRenewalAndPreservesUnexpiredReplay(t *testing.T) {
	ctx, backend, config, _, count := pruneFixture(t)
	base := config.Now()
	var offset atomic.Int64
	offset.Store(-int64(2 * time.Minute))
	config.Retention = time.Minute
	config.Timeout = 5 * time.Second
	config.Now = func() time.Time { return base.Add(time.Duration(offset.Load())) }
	service, err := api.NewIdempotency(config)
	if err != nil {
		t.Fatal(err)
	}
	operation := api.Operation{Scope: "tenant", Action: "create", Key: "expired-then-renewed", Input: api.Values{}}
	var mutations atomic.Int64
	mutate := func(context.Context) (api.MutationResponse, error) {
		id := mutations.Add(1)
		return api.MutationResponse{Status: 200, ObjectKey: api.Values{"id": id}, Body: api.Values{"id": id}}, nil
	}
	if _, err := service.Execute(ctx, operation, mutate); err != nil {
		t.Fatal(err)
	}
	offset.Store(0)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := service.Execute(ctx, operation, func(ctx context.Context) (api.MutationResponse, error) {
			close(started)
			select {
			case <-release:
				return mutate(ctx)
			case <-ctx.Done():
				return api.MutationResponse{}, ctx.Err()
			}
		})
		done <- err
	}()
	var once sync.Once
	releaseRenewal := func() { once.Do(func() { close(release) }) }
	t.Cleanup(releaseRenewal)
	select {
	case <-started:
	case err := <-done:
		t.Fatal("renewal did not lock receipt", err)
	case <-time.After(3 * time.Second):
		t.Fatal("renewal did not start")
	}
	short := config
	short.Timeout = 50 * time.Millisecond
	shortService, _ := api.NewIdempotency(short)
	result, err := shortService.Prune(ctx, 1)
	if err == nil || result.Outcome != api.MutationUnchanged || result.Deleted != 0 {
		t.Fatal("contended maintenance was not bounded", result, err)
	}
	// Start another real SELECT FOR UPDATE and prove it is waiting on the
	// renewal before releasing that transaction. Its expired predicate must be
	// reevaluated against the renewed tuple, not delete the earlier snapshot.
	pid := make(chan int, 1)
	waiting := config
	waiting.Backend = waitingPruneBackend{backend, pid}
	waitingService, err := api.NewIdempotency(waiting)
	if err != nil {
		t.Fatal(err)
	}
	type pruneResponse struct {
		result api.PruneResult
		err    error
	}
	pruned := make(chan pruneResponse, 1)
	go func() { result, err := waitingService.Prune(ctx, 1); pruned <- pruneResponse{result, err} }()
	var waitingPID int
	select {
	case waitingPID = <-pid:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance query did not start")
	}
	waitCtx, stopWaiting := context.WithTimeout(ctx, 3*time.Second)
	defer stopWaiting()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var locked bool
		if err := db.QueryRow(waitCtx, backend, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')", []any{waitingPID}, &locked); err != nil {
			t.Fatal(err)
		}
		if locked {
			break
		}
		select {
		case <-ticker.C:
		case <-waitCtx.Done():
			t.Fatal("maintenance did not wait on the renewed receipt")
		}
	}
	releaseRenewal()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	completed := <-pruned
	if completed.err != nil || completed.result.Deleted != 0 || completed.result.Outcome != api.MutationCommitted {
		t.Fatal("waiting maintenance removed renewed receipt", completed.result, completed.err)
	}
	if result, err := service.Prune(ctx, 1); err != nil || result.Deleted != 0 || count() != 1 {
		t.Fatal("renewed receipt was erased", result, err)
	}
	if replay, err := service.Execute(ctx, operation, mutate); err != nil || !replay.Replayed || mutations.Load() != 2 {
		t.Fatal("cleanup broke live replay", replay.Outcome, err, mutations.Load())
	}
	// Physical expiry removal cannot promise replay outside retention.
	offset.Store(int64(2 * time.Minute))
	if result, err := service.Prune(ctx, 1); err != nil || result.Deleted != 1 || count() != 0 {
		t.Fatal(result, err)
	}
	if fresh, err := service.Execute(ctx, operation, mutate); err != nil || fresh.Replayed || mutations.Load() != 3 {
		t.Fatal("expired key did not begin a fresh operation", fresh.Outcome, err, mutations.Load())
	}
}
