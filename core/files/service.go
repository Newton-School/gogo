package files

import (
	"context"
	"database/sql"
	"io"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type serviceBackend struct {
	db.Backend
	alias string
	tx    *serviceTransaction
}

func (b *serviceBackend) Alias() string { return b.alias }
func (b *serviceBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil {
		if !missingValue(tx) {
			_ = safeServiceCall(tx.Rollback)
		}
		return nil, ErrUnavailable
	}
	if missingValue(tx) {
		return nil, ErrUnavailable
	}
	b.tx = &serviceTransaction{Transaction: tx}
	return b.tx, nil
}

type serviceTransaction struct {
	db.Transaction
	commitAttempted, committed bool
	commitErr                  error
}

func (tx *serviceTransaction) Commit() error {
	tx.commitAttempted = true
	tx.commitErr = tx.Transaction.Commit()
	tx.committed = tx.commitErr == nil
	return tx.commitErr
}

func (s serviceState) operation() (*serviceBackend, *orm.Store) {
	backend := &serviceBackend{Backend: s.backend, alias: s.alias}
	return backend, orm.New(backend, s.registry)
}

// StoreValidated binds already-approved bytes to an existing scoped owner.
// It never retries, overwrites an object, or deletes a possible orphan. The
// caller must retain input.Identity before this operation and reconcile any
// StoreFailure before considering retry or cleanup. No raw-upload validation,
// automatic form binding, owner creation, or public access grant occurs here.
func (s *Service) StoreValidated(ctx context.Context, input StoreInput, source io.Reader) (info Info, err error) {
	if s == nil || s.state == nil {
		return Info{}, ErrConfiguration
	}
	state := *s.state
	var backend *serviceBackend
	var publication SaveResult
	publicationUnknown := false
	defer func() {
		panicValue := recover()
		if backend != nil && backend.tx != nil && backend.tx.committed {
			// The observed commit is authoritative, not any error type returned
			// by an application callback. Actual after-commit failures remain
			// visible; late cancellation alone cannot undo confirmed success.
			if panicValue != nil || err != nil {
				info = copyInfo(info)
				err = &StoreFailure{Identity: input.Identity, Published: publication.Published, Committed: true, kind: ErrCommittedCallback}
			}
			return
		}
		if backend != nil && backend.tx != nil && backend.tx.commitAttempted && (panicValue != nil || !serviceRejectedCommit(backend.tx.commitErr)) {
			err = ErrOutcomeUnknown
		} else {
			if panicValue != nil {
				err = ErrUnavailable
			}
			if canceled := storageContext(ctx); canceled != nil && !publicationUnknown {
				err = canceled
			}
		}
		if err != nil {
			info = Info{}
			err = &StoreFailure{Identity: input.Identity, Published: publication.Published, PublicationUnknown: publicationUnknown, kind: serviceFailure(err)}
		}
	}()
	// Only immutable builtins survive this input boundary, before Context,
	// scope, provider, authorization, or source callbacks can run.
	id, e := metadataID(input.Identity.ID)
	if e != nil || id != input.Identity.ID || !validKey(input.Identity.Key) || missingValue(source) {
		return Info{}, ErrInvalidOwner
	}
	owner, e := state.selectOwner(input.Owner)
	if e != nil {
		return Info{}, e
	}
	if _, e := canonicalContentType(input.ContentType); e != nil {
		return Info{}, e
	}
	if e := storageContext(ctx); e != nil {
		return Info{}, e
	}
	if db.InTransaction(ctx, state.alias) {
		return Info{}, ErrTransaction
	}
	if e := state.preflightStore(ctx, owner); e != nil {
		return Info{}, e
	}
	publication, publicationUnknown, err = saveServiceBlob(ctx, state.storage, input.Identity.Key, source)
	if err != nil {
		return Info{}, err
	}
	if e := storageContext(ctx); e != nil {
		return Info{}, e
	}
	var store *orm.Store
	backend, store = state.operation()
	created := serviceNow()
	file := File{ID: input.Identity.ID, StorageAlias: state.storageAlias, ObjectKey: input.Identity.Key, OwnerRef: owner.reference, State: Ready, ContentType: input.ContentType, Bytes: publication.Bytes, Checksum: publication.SHA256, CreatedAt: created, FinalizedAt: &created}
	var outcome error
	err = db.Atomic(ctx, backend, db.AtomicOptions{Durable: true}, func(txctx context.Context) error {
		outcome = state.bindFile(txctx, store, backend.tx, owner, file)
		if outcome == nil {
			info = fileInfo(file)
		}
		return outcome
	})
	if err != nil && err != outcome {
		// Mixed transaction/cleanup failures must not be presented as a pure
		// policy denial or a caller-correctable metadata collision.
		err = ErrUnavailable
	}
	return info, err
}

func (s serviceState) preflightStore(ctx context.Context, selection ownerSelection) error {
	backend, store := s.operation()
	var outcome error
	err := db.Atomic(ctx, backend, db.AtomicOptions{TxOptions: db.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, Durable: true}, func(txctx context.Context) error {
		outcome = func() error {
			read, e := compileOwner(txctx, store, selection, false)
			if e != nil {
				return e
			}
			values, found, e := read.load(txctx, backend.tx)
			if e != nil {
				return e
			}
			if !found {
				return ErrNotFound
			}
			if e := authorizeOwner(txctx, selection, values, StoreFile, nil); e != nil {
				return e
			}
			return read.verify(txctx, backend.tx, values)
		}()
		return outcome
	})
	if err != nil && err != outcome {
		return ErrUnavailable
	}
	return err
}

func (s serviceState) bindFile(ctx context.Context, store *orm.Store, executor db.Executor, selection ownerSelection, file File) error {
	read, err := compileOwner(ctx, store, selection, true)
	if err != nil {
		return err
	}
	values, found, err := read.load(ctx, executor)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	key := ownerObjectKey(values, selection.binding.field)
	var old File
	var oldRead metadataRead
	var previous *Info
	if key != "" {
		oldRead, err = compileMetadata(ctx, store, db.Predicate{Connector: "AND", Children: []db.Predicate{orm.Q("storage_alias", s.storageAlias), orm.Q("object_key", key)}}, true)
		if err != nil {
			return err
		}
		old, found, err = oldRead.load(ctx, executor)
		if err != nil {
			return err
		}
		if !found || old.StorageAlias != s.storageAlias || old.ObjectKey != key || old.OwnerRef != selection.reference || old.State != Ready || old.ID == file.ID || key == file.ObjectKey {
			return ErrMetadataConflict
		}
		value := fileInfo(old)
		previous = &value
	}
	if err := authorizeOwner(ctx, selection, values, StoreFile, previous); err != nil {
		return err
	}
	if previous != nil {
		if err := authorizeOwner(ctx, selection, values, ReplaceFile, previous); err != nil {
			return err
		}
	}
	// Finish all application callbacks before the exact prewrite fence. ACL
	// concurrency is the policy owner's lock/transaction responsibility.
	if err := read.verify(ctx, executor, values); err != nil {
		return err
	}
	if previous != nil {
		if err := oldRead.verify(ctx, executor, old); err != nil {
			return err
		}
	}
	write := copyFile(file)
	if err := store.Save(ctx, &write, orm.SaveOptions{ForceInsert: true}); err != nil {
		return serviceWriteFailure(err)
	}
	record, err := models.NewRecord(selection.binding.schema)
	if err != nil {
		return ErrUnavailable
	}
	for name, value := range values {
		if err := record.Set(name, value); err != nil {
			return ErrUnavailable
		}
	}
	if err := record.Set(selection.binding.field, file.ObjectKey); err != nil {
		return ErrUnavailable
	}
	if err := store.Save(ctx, record, orm.SaveOptions{ForceUpdate: true, UpdateFields: []string{selection.binding.field}}); err != nil {
		return serviceWriteFailure(err)
	}
	if previous != nil {
		old.State = Deleting
		write := copyFile(old)
		if err := store.Save(ctx, &write, orm.SaveOptions{ForceUpdate: true, UpdateFields: []string{"state"}}); err != nil {
			return serviceWriteFailure(err)
		}
	}
	newRead, err := compileMetadata(ctx, store, orm.Q("id", file.ID), false)
	if err != nil {
		return err
	}
	expected := copyOwnerValues(values)
	expected[selection.binding.field] = file.ObjectKey
	if err := read.verify(ctx, executor, expected); err != nil {
		return err
	}
	if err := newRead.verify(ctx, executor, file); err != nil {
		return err
	}
	if previous != nil {
		if err := oldRead.verify(ctx, executor, old); err != nil {
			return err
		}
	}
	return storageContext(ctx)
}

func saveServiceBlob(ctx context.Context, storage Storage, key string, source io.Reader) (result SaveResult, unknown bool, err error) {
	defer func() {
		if recover() != nil {
			unknown, err = true, ErrPublicationUnknown
		}
	}()
	result, err = storage.Save(ctx, key, source)
	if !result.Published {
		if err == nil || result != (SaveResult{}) {
			return result, true, ErrPublicationUnknown
		}
		return result, false, serviceFailure(err)
	}
	if result.Key != key || result.Bytes < 0 || result.ModifiedTime.IsZero() || len(result.SHA256) != 64 || !validKey(result.SHA256[:32]) || !validKey(result.SHA256[32:]) {
		return result, true, ErrPublicationUnknown
	}
	if err != nil {
		return result, false, serviceFailure(err)
	}
	return result, false, nil
}

func serviceFailure(err error) error {
	switch err {
	case nil, ErrConfiguration, ErrInvalidOwner, ErrInvalidKey, ErrLimit, ErrNotFound, ErrCollision, ErrClosed, ErrUnavailable, ErrForbidden, ErrMetadataConflict, ErrTransaction, ErrOutcomeUnknown, ErrPublicationUnknown, context.Canceled, context.DeadlineExceeded:
		return err
	default:
		return ErrUnavailable
	}
}

func serviceRejectedCommit(err error) bool {
	value, ok := err.(*db.Error)
	if !ok || value == nil || value.Cause != nil {
		return false
	}
	switch value.Code {
	case db.UniqueViolation, db.ForeignKeyViolation, db.CheckViolation, db.NotNullViolation, db.SerializationFailure, db.Deadlock:
		return true
	}
	return false
}

func serviceWriteFailure(err error) error {
	if value, ok := err.(*db.Error); ok && value != nil && value.Cause == nil && value.Code == db.UniqueViolation && value.Constraint == StorageKeyConstraint {
		return ErrMetadataConflict
	}
	if err == context.Canceled {
		return context.Canceled
	}
	if err == context.DeadlineExceeded {
		return context.DeadlineExceeded
	}
	return ErrUnavailable
}
