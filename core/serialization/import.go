package serialization

import (
	"context"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"io"
)

// Load inserts complete, caller-identified non-auto rows in one owned durable
// transaction. It never upserts, applies defaults, runs model/save hooks, resets
// sequences or retries. DryRun executes the same constraint and final fences,
// then confirms rollback. Retain input identities to reconcile an unknown commit.
func (f *Fixtures) Load(ctx context.Context, r io.Reader, o LoadOptions) (result LoadResult, err error) {
	var b *operationBackend
	defer func() {
		panicValue := recover()
		if b != nil && b.tx != nil && b.tx.committed {
			result.Committed = true
			result.DryRun = false
			if panicValue != nil || err != nil {
				err = ErrCommittedCallback
			}
			return
		}
		if b != nil && b.tx != nil && b.tx.attempted && (panicValue != nil || !rejectedCommit(b.tx.commitErr)) {
			err = ErrOutcomeUnknown
		} else if panicValue != nil {
			err = ErrUnavailable
		}
		if err != nil {
			result = LoadResult{}
			err = safeError(err)
		}
	}()
	if f == nil || f.state == nil {
		return result, ErrConfiguration
	}
	s := *f.state
	selected, e := s.selectModels(o.Format, o.Models, true)
	if e != nil {
		return result, e
	}
	format, dry := o.Format, o.DryRun
	if nilValue(r) {
		return result, ErrInvalid
	}
	if e = contextError(ctx); e != nil {
		return result, e
	}
	if db.InTransaction(ctx, s.alias) {
		return result, ErrTransaction
	}
	raw, e := readInput(ctx, r, s.limits.MaxBytes)
	if e != nil {
		return result, e
	}
	records, e := s.parse(raw, format, selected)
	if e != nil {
		return result, e
	}
	var store *orm.Store
	b, store = s.operation(true)
	var bodyErr error
	err = db.Atomic(ctx, b, atomicOptions(true), func(txctx context.Context) (operationErr error) {
		defer func() { bodyErr = operationErr }()
		queries := map[string]orm.Query[*models.MapRecord]{}
		for _, p := range selected {
			q, e := query(txctx, store, p)
			if e != nil {
				return e
			}
			queries[p.schema.Key()] = q
		}
		// Every input grant is evaluated before the first INSERT. Copies ensure
		// later callbacks cannot rewrite a previously validated input snapshot.
		for _, record := range records {
			if e := authorize(txctx, record.p, ImportRecord, record.record); e != nil {
				return &Error{Record: record.index, kind: e}
			}
		}
		for _, record := range records {
			if e := contextError(txctx); e != nil {
				return e
			}
			// The fixture witness must not share mutable argument buffers with
			// the model or trusted SQL provider during INSERT/RETURNING.
			input, e := cloneRecord(record.p, record.record)
			if e != nil {
				return ErrUnavailable
			}
			model, e := models.NewRecord(record.p.schema)
			if e != nil {
				return ErrUnavailable
			}
			for _, field := range record.p.schema.Fields {
				v := input.Fields[field.Name]
				if isPK(record.p, field.Name) {
					v = input.PK[field.Name]
				}
				if model.Set(field.Name, v) != nil {
					return ErrUnavailable
				}
			}
			if e = store.Save(txctx, model, orm.SaveOptions{Raw: true, ForceInsert: true}); e != nil {
				return &Error{Record: record.index, kind: ErrUnavailable}
			}
		}
		// Deferred constraint triggers can mutate inserted rows. Run them BEFORE
		// exact final reads/grants, leaving constraints immediate through Commit.
		if e := b.checker.CheckConstraints(txctx); e != nil {
			return ErrUnavailable
		}
		for _, record := range records {
			current, e := verify(txctx, b, record.p, queries[record.p.schema.Key()], record.record)
			if e != nil {
				return &Error{Record: record.index, kind: e}
			}
			if e = authorize(txctx, record.p, ImportRecord, current); e != nil {
				return &Error{Record: record.index, kind: e}
			}
		}
		// A later policy must not invalidate an earlier row's completed fence.
		for _, record := range records {
			if _, e := verify(txctx, b, record.p, queries[record.p.schema.Key()], record.record); e != nil {
				return &Error{Record: record.index, kind: e}
			}
		}
		if e := contextError(txctx); e != nil {
			return e
		}
		result.Records = len(records)
		if dry {
			return dryRollback
		}
		return nil
	})
	if dry && err == dryRollback {
		result.DryRun = true
		err = nil
	} else if err != nil && err != bodyErr {
		// Only this operation's unchanged body diagnostic may expose its
		// registered location. Provider errors (including forged *Error) and
		// mixed cleanup failures cannot manufacture fixture diagnostics.
		if err != ErrConfiguration && err != context.Canceled && err != context.DeadlineExceeded {
			err = ErrUnavailable
		}
	}
	return result, err
}
