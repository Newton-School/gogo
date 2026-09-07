package api

import (
	"context"
	"errors"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/internal/sqlcompiler"
)

// PruneResult contains no keys, actor identities or response data. Deleted is
// confirmed only for MutationCommitted, including after-commit callback errors.
// An unknown outcome deliberately reports zero, not an estimated deletion count.
type PruneResult struct {
	Deleted int64
	Outcome MutationOutcome
}

// Prune removes one bounded batch of expired sealed receipts. It never deletes
// business objects, unexpired receipts or unsealed rows. A zero limit means 100;
// otherwise the limit must be 1..1000 and fit the backend's parameter budget.
//
// This is explicit all-scope maintenance, disabled without AuthorizePrune.
// It requires a verified active principal and owns its outer transaction. Row
// locks serialize it with Execute; waits share the service's Timeout. A short
// batch is not proof that another transaction has no pending receipt. Rerunning
// after an unknown outcome is safe for expiry, but cannot recover that batch's
// exact count. Configure Now consistently with receipt creation/replay.
func (s *Idempotency) Prune(ctx context.Context, limit int) (result PruneResult, err error) {
	result.Outcome = MutationUnchanged
	var committed, prepared bool
	var deleted int64
	defer func() {
		if recover() != nil {
			switch {
			case committed && prepared:
				result = PruneResult{Deleted: deleted, Outcome: MutationCommitted}
			case prepared:
				result = PruneResult{Outcome: MutationUnknown}
			default:
				result = PruneResult{Outcome: MutationUnchanged}
			}
			err = mediaError(503, "MAINTENANCE_FAILED", "Receipt maintenance failed")
		}
	}()
	if ctx == nil || s == nil || s.store == nil {
		return result, errors.New("api: receipt maintenance requires a context and service")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if s.config.AuthorizePrune == nil {
		return result, auth.ErrPermissionDenied
	}
	p := auth.FromContext(ctx)
	if !p.Authenticated {
		return result, auth.ErrUnauthenticated
	}
	if !p.Active || !validOperationPart(p.ID, 1024) {
		return result, auth.ErrPermissionDenied
	}
	if limit == 0 {
		limit = 100
	}
	maximum := 500
	if provider, ok := s.store.Backend.(db.ParameterLimiter); ok {
		maximum = provider.MaxParameters()
	}
	// One cutoff and three sealed status parameters accompany the selected IDs.
	if limit < 1 || limit > 1000 || maximum < 5 || limit > maximum-4 {
		return result, errors.New("api: invalid receipt maintenance batch limit")
	}
	if db.InTransaction(ctx, s.store.Backend.Alias()) {
		return result, errors.New("api: receipt maintenance must own its transaction")
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	err = db.Atomic(ctx, s.store.Backend, db.AtomicOptions{Durable: true}, func(ctx context.Context) (err error) {
		defer rollbackMutationPanic(&err)
		if err := db.OnCommit(ctx, s.store.Backend.Alias(), func(context.Context) error { committed = true; return nil }, false); err != nil {
			return err
		}
		permit := func() error {
			err := s.config.AuthorizePrune(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if err := permit(); err != nil {
			return err
		}
		cutoff := s.config.Now().UTC().Truncate(time.Microsecond)
		if cutoff.IsZero() || cutoff.Year() < 1 || cutoff.Year() > 9999 {
			return errors.New("api: invalid receipt maintenance clock")
		}
		expired := orm.And(orm.Q("expires_at__lte", cutoff), orm.Q("response_status__in", []int{200, 201, 204}))
		// Select only metadata, never materialize private response JSON merely
		// to delete it. Keep a deterministic lock order across concurrent batches.
		rows, err := orm.For(s.store, func() *IdempotencyRecord { return &IdempotencyRecord{} }).
			Filter(expired).Only("id").OrderBy("expires_at", "id").
			SelectForUpdate(false, false).Limit(limit).All(ctx)
		if err != nil {
			return err
		}
		if err := permit(); err != nil {
			return err
		}
		if len(rows) > 0 {
			ids := make([]string, len(rows))
			for index, row := range rows {
				ids[index] = row.ID
			}
			// Infrastructure cleanup deliberately bypasses application delete
			// hooks/collectors, which would load bodies and invent business effects.
			// Recheck expiry/status in the DML even though these IDs remain locked.
			schema := (&IdempotencyRecord{}).Schema()
			compiler := sqlcompiler.Compiler{Dialect: s.store.Backend.Dialect(), Schema: schema}
			where, err := compiler.Predicate(orm.And(expired, orm.Q("id__in", ids)))
			if err != nil {
				return err
			}
			table, err := s.store.Backend.Dialect().QuoteIdentifier(schema.DBTable())
			if err != nil {
				return err
			}
			changed, err := db.ExecutorFor(ctx, s.store.Backend).Exec(ctx, "DELETE FROM "+table+" WHERE "+where, compiler.Args...)
			if err != nil {
				return err
			}
			deleted, err = changed.RowsAffected()
			if err != nil {
				return err
			}
			if deleted < 0 || deleted > int64(len(rows)) {
				return errors.New("api: invalid receipt maintenance result count")
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		prepared = true
		return nil
	})
	switch {
	case committed && prepared:
		result = PruneResult{Deleted: deleted, Outcome: MutationCommitted}
	case prepared && db.IsCode(err, db.UnknownCommit):
		result = PruneResult{Outcome: MutationUnknown}
	}
	return result, err
}
