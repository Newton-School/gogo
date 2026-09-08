package serialization

import (
	"context"
	"database/sql"
	"errors"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/orm"
)

func contextError(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	if nilValue(ctx) {
		return ErrInvalid
	}
	e := ctx.Err()
	if e == nil || e == context.Canceled || e == context.DeadlineExceeded {
		return e
	}
	return ErrUnavailable
}
func safeCall(f func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	return f()
}
func safeError(e error) error {
	if e == nil {
		return nil
	}
	if v, ok := e.(*Error); ok {
		return v
	}
	switch e {
	case ErrConfiguration, ErrInvalid, ErrForbidden, ErrUnavailable, ErrLimit, ErrTransaction, ErrOutcomeUnknown, ErrCommittedCallback, context.Canceled, context.DeadlineExceeded:
		return e
	}
	return ErrUnavailable
}

type operationBackend struct {
	db.Backend
	alias        string
	dialect      db.Dialect
	tx           *observedTransaction
	checker      db.ConstraintCheckTransaction
	requireCheck bool
}

func (b *operationBackend) Alias() string       { return b.alias }
func (b *operationBackend) Dialect() db.Dialect { return b.dialect }
func (b *operationBackend) BeginTx(ctx context.Context, o db.TxOptions) (db.Transaction, error) {
	tx, e := b.Backend.BeginTx(ctx, o)
	if e != nil || nilValue(tx) {
		if !nilValue(tx) {
			_ = safeCall(tx.Rollback)
		}
		return nil, ErrUnavailable
	}
	// Preserve the actual provider capability, not an interface accidentally
	// dropped (or fabricated) by the outcome-observing wrapper.
	b.checker, _ = tx.(db.ConstraintCheckTransaction)
	if b.requireCheck && nilValue(b.checker) {
		if safeCall(tx.Rollback) != nil {
			return nil, ErrUnavailable
		}
		return nil, ErrConfiguration
	}
	b.tx = &observedTransaction{Transaction: tx}
	return b.tx, nil
}

type observedTransaction struct {
	db.Transaction
	attempted, committed bool
	commitErr            error
}

func (t *observedTransaction) Commit() error {
	t.attempted = true
	t.commitErr = t.Transaction.Commit()
	t.committed = t.commitErr == nil
	return t.commitErr
}
func rejectedCommit(e error) bool {
	v, ok := e.(*db.Error)
	if !ok || v == nil || v.Cause != nil {
		return false
	}
	switch v.Code {
	case db.UniqueViolation, db.ForeignKeyViolation, db.CheckViolation, db.NotNullViolation, db.SerializationFailure, db.Deadlock:
		return true
	}
	return false
}
func (s fixtureState) operation(load bool) (*operationBackend, *orm.Store) {
	b := &operationBackend{Backend: s.backend, alias: s.alias, dialect: s.dialect, requireCheck: load}
	return b, orm.New(b, s.registry)
}
func atomicOptions(load bool) db.AtomicOptions {
	o := db.AtomicOptions{Durable: true}
	if !load {
		o.TxOptions = db.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead}
	}
	return o
}

var dryRollback = errors.New("serialization: private dry-run rollback")

func authorize(ctx context.Context, p profile, a Action, r Record) error {
	copy, e := cloneRecord(p, r)
	if e != nil {
		return e
	}
	if e = contextError(ctx); e != nil {
		return e
	}
	e = p.authorize(ctx, a, copy)
	if canceled := contextError(ctx); canceled != nil {
		return canceled
	}
	if e == nil || e == ErrForbidden {
		return e
	}
	return ErrUnavailable
}
