package orm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/internal/sqlcompiler"
)

// Update writes the root query's matching rows in one database statement and
// returns the number matched, including rows whose values did not change.
// Values are native field values or database expressions such as Add(F("count"),
// Value(1)); arithmetic never reads model instances into application memory.
//
// Like BulkUpdate, Update does not call FullClean, field PreSave, defaults or
// Save receivers, and it does not refresh previously loaded model instances.
// Root scope is applied at execution. It scopes the rows selected for mutation,
// not proposed values or referenced foreign-key targets; callers own write
// authorization. Sliced, joined, grouped and inherited writes are unsupported.
// Ordinary ordering/projection do not affect which unsliced rows are updated.
// The operation uses an owned transaction or an ambient savepoint. No error
// triggers automatic replay. A committed-callback error retains the matched
// count; all other errors return zero, including an uncertain commit.
func (q Query[T]) Update(ctx context.Context, values map[string]any) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := q.store.Backend.Capabilities().Require("transactions", "update_matched_rows"); err != nil {
		return 0, err
	}
	statement, args, err := q.updateSQL(ctx, values)
	if err != nil || statement == "" {
		return 0, err
	}
	var matched int64
	err = db.Atomic(ctx, q.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		result, err := db.ExecutorFor(ctx, q.store.Backend).Exec(ctx, statement, args...)
		if err != nil {
			return err
		}
		matched, err = result.RowsAffected()
		if err == nil && matched < 0 {
			err = errors.New("orm: backend returned an invalid update count")
		}
		return err
	})
	if err != nil {
		var committed *db.CommittedCallbackError
		if !errors.As(err, &committed) {
			matched = 0
		}
	}
	return matched, err
}

func (q Query[T]) updateSQL(ctx context.Context, values map[string]any) (string, []any, error) {
	if q.schema.Parent != "" || q.schema.Abstract || q.schema.Proxy || q.schema.Unmanaged {
		return "", nil, errors.New("orm: Update requires a managed single-table concrete model")
	}
	ast := q.selectAST
	if ast.Limit != nil || ast.Offset != nil || ast.Distinct || len(ast.DistinctOn) > 0 || len(ast.Joins) > 0 || len(q.relatedPaths) > 0 || len(ast.GroupBy) > 0 || len(ast.Projections) > 0 || ast.ForUpdate || ast.Having.Field != "" || ast.Having.Expression != nil || len(ast.Having.Children) > 0 {
		return "", nil, errors.New("orm: Update does not support slicing, distinct, joins, grouping or read-lock options")
	}
	// Snapshot assignments before trusted scope/codec callbacks execute. Plain
	// data obeys Query's ownership contract; opaque provider values remain owned
	// by the application and must not be mutated during execution.
	names := make([]string, 0, len(values))
	assignments := make(map[string]any, len(values))
	for name, value := range values {
		if expression, ok := value.(db.Expression); ok {
			if err := sqlcompiler.ValidateUpdateExpression(expression); err != nil {
				return "", nil, err
			}
			value = cloneExpression(expression)
		} else {
			value = cloneQueryValue(value)
		}
		names = append(names, name)
		assignments[name] = value
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", nil, nil
	}
	prepared, err := q.prepareRelated(ctx)
	if err != nil {
		return "", nil, err
	}
	encoded := make([]sqlcompiler.Assignment, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		field, ok := q.schema.Field(name)
		if !ok || !field.IsStored() || field.Kind == models.Generated || field.GeneratedExpression != "" {
			return "", nil, errors.New("orm: Update requires concrete writable fields")
		}
		value := assignments[name]
		expression, isExpression := value.(db.Expression)
		if !isExpression || expression.Kind == "value" {
			if isExpression {
				value = expression.Value
			}
			// A nil pointer is an absent Go field, matching BoundRecord.Get;
			// in particular a nil *RawMessage is SQL NULL, not JSON null.
			if value != nil {
				pointer := reflect.ValueOf(value)
				if pointer.Kind() == reflect.Pointer && pointer.IsNil() {
					value = nil
				}
			}
			value, err = encodeField(field, value)
			if err != nil {
				return "", nil, fmt.Errorf("orm: encode update field %s: %w", name, err)
			}
			expression = Value(value)
		}
		encoded = append(encoded, sqlcompiler.Assignment{Field: name, Expression: expression})
	}
	statement, args, err := sqlcompiler.Update(q.store.Backend.Dialect(), q.schema, prepared.selectAST.Where, encoded)
	if err != nil {
		return "", nil, err
	}
	maximum := 500
	if limiter, ok := q.store.Backend.(db.ParameterLimiter); ok {
		maximum = limiter.MaxParameters()
	}
	if maximum < 1 || len(args) > maximum {
		return "", nil, errors.New("orm: Update exceeds backend parameter limit")
	}
	return statement, args, ctx.Err()
}
