package orm

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"

	"github.com/Newton-School/gogo/core/db"
)

// readSaveReturning finishes at most one row before handing values to model
// hydration. Statement success inside an explicit transaction still does not
// imply its caller's later commit; Save's existing transaction timing remains.
func readSaveReturning(ctx context.Context, executor db.Executor, query string, args []any, columns int) (values []any, found bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	rows, err := executor.Query(ctx, query, args...)
	if !nilSaveRows(rows) {
		defer func() {
			// Closing first also observes errors surfaced while a driver drains
			// the result. Preserve both provider errors and cancellation.
			closeErr := rows.Close()
			err = errors.Join(err, closeErr, rows.Err(), ctx.Err())
			if err != nil {
				values, found = nil, false
			}
		}()
	}
	if err != nil {
		return nil, false, err
	}
	if nilSaveRows(rows) {
		return nil, false, errors.New("orm: backend returned no row reader")
	}
	if !rows.Next() {
		return nil, false, nil
	}
	values = make([]any, columns)
	dest := make([]any, columns)
	for i := range values {
		dest[i] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, false, err
	}
	// A driver may recycle byte buffers at Next or Close. Custom driver values
	// retain their existing types and trusted ownership contract; do not copy
	// arbitrary object graphs or invoke codecs before terminal success.
	for i := range values {
		switch value := values[i].(type) {
		case []byte:
			values[i] = slices.Clone(value)
		case sql.RawBytes:
			values[i] = slices.Clone(value)
		}
	}
	if rows.Next() {
		return nil, false, errors.New("orm: save returned multiple rows")
	}
	return values, true, nil
}

func nilSaveRows(rows db.Rows) bool {
	if rows == nil {
		return true
	}
	v := reflect.ValueOf(rows)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return v.IsNil()
	}
	return false
}
