package files

import (
	"context"
	"reflect"
	"slices"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// A compiled owner read is reused for callback-free fences: grants cannot
// retarget a second invocation of the root scope to a different owner.
type ownerRead struct {
	selection ownerSelection
	statement string
	args      []any
}

func compileOwner(ctx context.Context, store *orm.Store, owner ownerSelection, lock bool) (ownerRead, error) {
	factory := func() *models.MapRecord {
		record, _ := models.NewRecord(owner.binding.schema)
		return record
	}
	query := orm.For(store, factory).OrderBy().Limit(2).WithScope(func(ctx context.Context, schema models.Schema) (db.Predicate, error) {
		// ORM scope receives a schema value; detach its slices before crossing
		// the application's callback boundary, preserving the query descriptor.
		return owner.binding.scope(ctx, schema.Clone())
	})
	for _, name := range owner.binding.pk {
		query = query.Filter(orm.Q(name, owner.key[name]))
	}
	if lock {
		query = query.SelectForUpdate(false, false)
	}
	statement, args, err := query.SQLContext(ctx)
	if err != nil {
		return ownerRead{}, ErrUnavailable
	}
	return ownerRead{selection: owner, statement: statement, args: args}, nil
}

func (read ownerRead) load(ctx context.Context, executor db.Executor) (map[string]any, bool, error) {
	fields := read.selection.binding.schema.Fields
	values := make(map[string]any, len(fields))
	found, err := serviceRow(ctx, executor, read.statement, read.args, func(rows db.Rows) error {
		raw := make([]any, len(fields))
		pointers := make([]any, len(fields))
		for i := range raw {
			pointers[i] = &raw[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		for i, field := range fields {
			primary := slices.Contains(read.selection.binding.pk, field.Name)
			value, err := scalarOwnerValue(field, raw[i], primary)
			if err != nil || primary && value != read.selection.key[field.Name] {
				return ErrUnavailable
			}
			values[field.Name] = value
		}
		return nil
	})
	if err != nil || !found {
		return nil, false, err
	}
	return values, true, nil
}

func (read ownerRead) verify(ctx context.Context, executor db.Executor, expected map[string]any) error {
	values, found, err := read.load(ctx, executor)
	if err != nil {
		return err
	}
	if !found || !reflect.DeepEqual(values, expected) {
		return ErrMetadataConflict
	}
	return nil
}

type metadataRead struct {
	statement string
	args      []any
}

func compileMetadata(ctx context.Context, store *orm.Store, predicate db.Predicate, lock bool) (metadataRead, error) {
	query := orm.For(store, func() *File { return &File{} }).Filter(predicate).OrderBy().Limit(2)
	if lock {
		query = query.SelectForUpdate(false, false)
	}
	statement, args, err := query.SQLContext(ctx)
	if err != nil {
		return metadataRead{}, ErrUnavailable
	}
	return metadataRead{statement: statement, args: args}, nil
}

func (read metadataRead) load(ctx context.Context, executor db.Executor) (File, bool, error) {
	var file File
	found, err := serviceRow(ctx, executor, read.statement, read.args, func(rows db.Rows) error {
		if err := rows.Scan(&file.ID, &file.StorageAlias, &file.ObjectKey, &file.OwnerRef, &file.State, &file.ContentType, &file.Bytes, &file.Checksum, &file.CreatedAt, &file.FinalizedAt); err != nil {
			return err
		}
		// Detach provider pointers before terminal Next/Err/Close or context
		// callbacks, not merely before returning the completed row.
		file = copyFile(file)
		return nil
	})
	if err != nil || !found {
		return File{}, false, err
	}
	if file.Clean(context.Background()) != nil {
		return File{}, false, ErrUnavailable
	}
	return file, true, nil
}

func (read metadataRead) verify(ctx context.Context, executor db.Executor, expected File) error {
	file, found, err := read.load(ctx, executor)
	if err != nil {
		return err
	}
	if !found || file.OwnerRef != expected.OwnerRef || !reflect.DeepEqual(fileInfo(file), fileInfo(expected)) {
		return ErrMetadataConflict
	}
	return nil
}

func fileInfo(file File) Info {
	result := Info{ID: file.ID, StorageAlias: file.StorageAlias, ObjectKey: file.ObjectKey, State: file.State, ContentType: file.ContentType, Bytes: file.Bytes, Checksum: file.Checksum, CreatedAt: detachedFileTime(file.CreatedAt)}
	if file.FinalizedAt != nil {
		value := detachedFileTime(*file.FinalizedAt)
		result.FinalizedAt = &value
	}
	return result
}

func copyFile(file File) File {
	file.Base = models.Base{}
	file.CreatedAt = detachedFileTime(file.CreatedAt)
	if file.FinalizedAt != nil {
		value := detachedFileTime(*file.FinalizedAt)
		file.FinalizedAt = &value
	}
	return file
}

func copyInfo(info Info) Info {
	info.CreatedAt = detachedFileTime(info.CreatedAt)
	if info.FinalizedAt != nil {
		value := detachedFileTime(*info.FinalizedAt)
		info.FinalizedAt = &value
	}
	return info
}

func authorizeOwner(ctx context.Context, selection ownerSelection, values map[string]any, action OwnerAction, previous *Info) error {
	if err := storageContext(ctx); err != nil {
		return err
	}
	fields := make(map[string]any, len(selection.binding.policy))
	for _, name := range selection.binding.policy {
		fields[name] = values[name]
	}
	snapshot := OwnerSnapshot{Binding: selection.binding.name, Reference: selection.reference, Key: copyOwnerValues(selection.key), Fields: fields}
	if previous != nil {
		info := copyInfo(*previous)
		snapshot.Previous = &info
	}
	err := selection.binding.authorize(ctx, action, snapshot)
	if canceled := storageContext(ctx); canceled != nil {
		return canceled
	}
	if err == ErrForbidden {
		return ErrForbidden
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func ownerObjectKey(values map[string]any, field string) string {
	if values[field] == nil {
		return ""
	}
	return values[field].(string)
}

// serviceRow never exposes a prefix or decodes a second row. Completion and
// cleanup are checked even when a provider callback panics. Each callback is a
// trusted port, but its panic text and database/path errors never escape.
func serviceRow(ctx context.Context, executor db.Executor, statement string, args []any, scan func(db.Rows) error) (found bool, err error) {
	var rows db.Rows
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if !missingValue(rows) {
			for _, callback := range []func() error{rows.Err, rows.Close, rows.Err} {
				if safeServiceCall(callback) != nil {
					err = ErrUnavailable
				}
			}
		}
		if canceled := storageContext(ctx); canceled != nil {
			err = canceled
		}
		if err != nil {
			found = false
		}
	}()
	if err := storageContext(ctx); err != nil {
		return false, err
	}
	rows, err = executor.Query(ctx, statement, args...)
	if err != nil || missingValue(rows) {
		return false, ErrUnavailable
	}
	if !rows.Next() {
		return false, nil
	}
	if err := storageContext(ctx); err != nil {
		return false, err
	}
	if err := scan(rows); err != nil {
		return false, ErrUnavailable
	}
	if rows.Next() {
		return false, ErrUnavailable
	}
	return true, nil
}

func safeServiceCall(callback func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	return callback()
}

// PostgreSQL stores microseconds. A canonical UTC, microsecond timestamp avoids
// claiming nanosecond equality that the supported provider cannot retain.
func serviceNow() time.Time { return detachedFileTime(time.Now().Truncate(time.Microsecond)) }

// A named FixedZone is individually allocated, unlike time.UTC or the unnamed
// fixed-offset cache. Callers can replace *Time.Location() without mutating a
// provider, another callback, or future UTC observations.
func detachedFileTime(value time.Time) time.Time { return value.In(time.FixedZone("UTC", 0)) }
