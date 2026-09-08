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
// triggers automatic replay. Only an observed commit retains the matched count
// on error; savepoint failures and uncertain commits return zero.
func (q Query[T]) Update(ctx context.Context, values map[string]any) (int64, error) {
	if q.store.routed() {
		owned := make(map[string]any, len(values))
		candidate := q.clone()
		for name, value := range values {
			owned[name] = cloneQueryValue(value)
			expression, ok := owned[name].(db.Expression)
			if !ok {
				expression = Value(owned[name])
			}
			candidate.selectAST.Projections = append(candidate.selectAST.Projections, db.Projection{Expression: expression})
		}
		values = owned
		if e := candidate.checkRoutingShape(); e != nil {
			return 0, e
		}
	}
	q, ctx, routeErr := q.route(ctx, db.RouteWrite)
	if routeErr != nil {
		return 0, routeErr
	}
	if q.err != nil {
		return 0, q.err
	}
	routed := q.store.routeBinding != nil
	if !routed {
		// Capture the selected provider before context/scope callbacks can
		// replace the caller's Store.Backend. Retain its concrete interface for
		// compiler capabilities; only the Atomic handoff gets an alias adapter.
		store := *q.store
		q.store = &store
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
	backend := q.store.Backend
	atomic := q.store.operationAtomic
	var legacy *updateAtomicBackend
	if !routed {
		// Compilation already snapshots assignments before trusted callbacks.
		// Read Alias only once, on that same captured provider, and do not hide
		// any optional compiler metadata interfaces behind this adapter.
		legacy = &updateAtomicBackend{Backend: backend, alias: backend.Alias()}
		backend = legacy
		atomic = func(ctx context.Context, options db.AtomicOptions, fn func(context.Context) error) error {
			return db.Atomic(ctx, legacy, options, fn)
		}
	}
	ownedRouted := routed && !db.InTransaction(ctx, backend.Alias())
	observedCommit := false
	err = atomic(ctx, db.AtomicOptions{}, func(ctx context.Context) error {
		// Atomic's child context contains the actual selected frame. Resolve
		// its executor once through the fixed alias, retaining the original
		// transaction and its optional interfaces without wrapping/copying it.
		executor := db.ExecutorFor(ctx, backend)
		if ownedRouted || (legacy != nil && legacy.began) {
			// Register first, before Exec/RowsAffected can add application
			// callbacks. Only this owned Atomic's actual Commit(nil) fires the
			// receipt. A provider-supplied error type is not commit evidence.
			if err := db.OnCommit(ctx, backend.Alias(), func(context.Context) error { observedCommit = true; return nil }, false); err != nil {
				return err
			}
		}
		result, err := executor.Exec(ctx, statement, args...)
		if err != nil {
			return err
		}
		matched, err = result.RowsAffected()
		if err == nil && matched < 0 {
			err = errors.New("orm: backend returned an invalid update count")
		}
		return err
	})
	if err != nil && !observedCommit {
		// A nested savepoint has no durable commit to observe. Do not keep
		// a receipt in its parent or retain counts on a nested error.
		matched = 0
	}
	return matched, err
}

// updateAtomicBackend changes only the legacy Atomic handoff. A preliminary
// InTransaction check could observe a different cooperative context than Atomic
// itself. Only a successful BeginTx call proves this operation owns a root;
// same-alias savepoints (including A -> B -> A) never set began or keep receipts.
type updateAtomicBackend struct {
	db.Backend
	alias string
	began bool
}

func (b *updateAtomicBackend) Alias() string { return b.alias }
func (b *updateAtomicBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, options)
	if err == nil {
		b.began = true
	}
	return tx, err
}

func (q Query[T]) updateSQL(ctx context.Context, values map[string]any) (string, []any, error) {
	if q.schema.Parent != "" || q.schema.Abstract || q.schema.Proxy || q.schema.Unmanaged {
		return "", nil, errors.New("orm: Update requires a managed single-table concrete model")
	}
	ast := q.selectAST
	if ast.Limit != nil || ast.Offset != nil || ast.Distinct || len(ast.DistinctOn) > 0 || len(ast.Joins) > 0 || len(q.relatedPaths) > 0 || len(ast.GroupBy) > 0 || len(ast.Projections) > 0 || len(ast.Aliases) > 0 || ast.ForUpdate || ast.Having.Field != "" || ast.Having.Expression != nil || len(ast.Having.Children) > 0 {
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
			if err := sqlcompiler.ValidateUpdateExpression(Value(value)); err != nil {
				return "", nil, err
			}
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
