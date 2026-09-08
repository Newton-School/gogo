package files

import (
	"context"
	"database/sql"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/orm"
)

// Open authorizes an exact ready metadata/owner link in its own read-only,
// repeatable-read snapshot, then opens private storage. It releases the database
// transaction before returning a reader. A concurrent replacement or ACL change
// after that snapshot can prevent later opens, but cannot retroactively revoke
// bytes from an already-started reader. Independently mutable ACLs require the
// application's explicit policy coordination; a snapshot is not fresh forever.
func (s *Service) Open(ctx context.Context, fileID string) (info Info, reader Reader, err error) {
	if s == nil || s.state == nil {
		return Info{}, nil, ErrConfiguration
	}
	state := *s.state
	var opened Reader
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if canceled := storageContext(ctx); canceled != nil {
			err = canceled
		}
		if err != nil {
			if !missingValue(opened) {
				if safeServiceCall(opened.Close) != nil {
					err = ErrUnavailable
				}
			}
			info, reader, err = Info{}, nil, serviceFailure(err)
		}
	}()
	id, err := metadataID(fileID)
	if err != nil || id != fileID {
		return Info{}, nil, ErrInvalidOwner
	}
	if err := storageContext(ctx); err != nil {
		return Info{}, nil, err
	}
	if db.InTransaction(ctx, state.alias) {
		return Info{}, nil, ErrTransaction
	}
	backend, store := state.operation()
	var outcome error
	err = db.Atomic(ctx, backend, db.AtomicOptions{TxOptions: db.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, Durable: true}, func(txctx context.Context) error {
		outcome = func() error {
			metadata, e := compileMetadata(txctx, store, orm.Q("id", id), false)
			if e != nil {
				return e
			}
			file, found, e := metadata.load(txctx, backend.tx)
			if e != nil {
				return e
			}
			if !found || file.State != Ready || file.StorageAlias != state.storageAlias {
				return ErrNotFound
			}
			if file.ID != id {
				return ErrUnavailable
			}
			owner, e := state.parseOwner(file.OwnerRef)
			if e != nil {
				return ErrUnavailable
			}
			read, e := compileOwner(txctx, store, owner, false)
			if e != nil {
				return e
			}
			values, found, e := read.load(txctx, backend.tx)
			if e != nil {
				return e
			}
			if !found || ownerObjectKey(values, owner.binding.field) != file.ObjectKey {
				return ErrNotFound
			}
			current := fileInfo(file)
			if e := authorizeOwner(txctx, owner, values, ReadFile, &current); e != nil {
				return e
			}
			if e := read.verify(txctx, backend.tx, values); e != nil {
				return e
			}
			if e := metadata.verify(txctx, backend.tx, file); e != nil {
				return e
			}
			// The storage service sees no key until all current read checks pass.
			opened, e = state.storage.Open(txctx, file.ObjectKey)
			if e != nil || missingValue(opened) {
				return ErrUnavailable
			}
			if e := authorizeOwner(txctx, owner, values, ReadFile, &current); e != nil {
				return e
			}
			if e := read.verify(txctx, backend.tx, values); e != nil {
				return e
			}
			if e := metadata.verify(txctx, backend.tx, file); e != nil {
				return e
			}
			if e := storageContext(txctx); e != nil {
				return e
			}
			info = current
			return nil
		}()
		return outcome
	})
	if err != nil {
		if err != outcome {
			err = ErrUnavailable
		}
		return Info{}, nil, err
	}
	return info, opened, nil
}
