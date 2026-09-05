package orm

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"strings"
)

// BulkUpdate writes explicit fields in bounded CASE batches. It never calls
// FullClean, field PreSave, or instance Save receivers. Missing database rows
// have Written=false; duplicate input primary keys are rejected before writes.
func BulkUpdate[T models.Model](ctx context.Context, store *Store, objects []T, names []string, options BulkOptions) ([]BulkOutcome[T], error) {
	if store == nil || store.Backend == nil {
		return nil, errors.New("orm: bulk backend required")
	}
	if len(names) == 0 {
		return nil, errors.New("orm: BulkUpdate requires explicit fields")
	}
	if options.IgnoreConflicts || options.ConflictConstraint != "" || len(options.UpdateFields) > 0 {
		return nil, errors.New("orm: BulkUpdate does not accept conflict options")
	}
	if len(objects) == 0 {
		return []BulkOutcome[T]{}, nil
	}
	if err := store.Backend.Capabilities().Require("returning", "transactions"); err != nil {
		return nil, err
	}
	if options.CommitEachBatch && db.InTransaction(ctx, store.Backend.Alias()) {
		return nil, errors.New("orm: per-batch commits require transaction ownership")
	}
	batchSize := options.BatchSize
	if batchSize == 0 {
		batchSize = 500
	}
	if batchSize < 1 || batchSize > 10000 {
		return nil, errors.New("orm: batch size must be between 1 and 10000")
	}
	records := make([]models.Record, len(objects))
	outcomes := make([]BulkOutcome[T], len(objects))
	identity := map[string]int{}
	var schema models.Schema
	fingerprint := ""
	for i, object := range objects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		record, err := models.Bind(object)
		if err != nil {
			return nil, err
		}
		sum, err := record.Schema().Fingerprint()
		if err != nil {
			return nil, err
		}
		if i == 0 {
			schema = record.Schema()
			fingerprint = sum
		} else if sum != fingerprint {
			return nil, errors.New("orm: batch requires one exact model schema")
		}
		if record.State().Database != "" && record.State().Database != store.Backend.Alias() {
			return nil, errors.New("orm: cross-database batch rejected")
		}
		for _, pk := range schema.PKFields() {
			value, err := record.Get(pk.Name)
			if err != nil {
				return nil, err
			}
			if value == nil || pk.IsAuto() && models.IsEmptyValue(value) {
				return nil, errors.New("orm: bulk update requires set primary keys")
			}
		}
		key, err := recordIdentity(record)
		if err != nil {
			return nil, err
		}
		if _, ok := identity[key]; ok {
			return nil, errors.New("orm: duplicate bulk-update primary key")
		}
		identity[key] = i
		records[i] = record
		outcomes[i] = BulkOutcome[T]{Input: i, Model: object}
	}
	if schema.Parent != "" || schema.Abstract || schema.Proxy || schema.Unmanaged {
		return nil, errors.New("orm: BulkUpdate requires managed single-table model")
	}
	pkNames := map[string]bool{}
	for _, pk := range schema.PKFields() {
		pkNames[pk.Name] = true
	}
	fields := []models.Field{}
	seen := map[string]bool{}
	for _, name := range names {
		field, ok := schema.Field(name)
		if !ok || !field.IsStored() || pkNames[name] || field.Kind == models.Generated || seen[name] {
			return nil, errors.New("orm: invalid or duplicate bulk-update field")
		}
		seen[name] = true
		fields = append(fields, field)
	}
	if batchSize*((len(pkNames)+1)*len(fields)+len(pkNames)) > 60000 {
		return nil, errors.New("orm: reduce batch size to keep parameter count below 60000")
	}
	pending := []int{}
	completed := []int{}
	apply := func(start int) {
		for _, index := range pending[start:] {
			outcomes[index].Written = true
			records[index].State().Persisted = true
			records[index].State().Database = store.Backend.Alias()
		}
	}
	runBatch := func(ctx context.Context, start, end int) error {
		dialect := store.Backend.Dialect()
		table, err := dialect.QuoteIdentifier(schema.DBTable())
		if err != nil {
			return err
		}
		args := []any{}
		sets := []string{}
		for _, field := range fields {
			column, _ := dialect.QuoteIdentifier(field.DBColumn())
			cases := []string{}
			for i := start; i < end; i++ {
				where, keys, err := primaryWhere(dialect, records[i], len(args))
				if err != nil {
					return err
				}
				args = append(args, keys...)
				value, err := records[i].Get(field.Name)
				if err != nil {
					return err
				}
				value, err = encodeField(field, value)
				if err != nil {
					return err
				}
				args = append(args, value)
				cases = append(cases, "WHEN "+where+" THEN "+dialect.Placeholder(len(args)))
			}
			sets = append(sets, column+" = CASE "+strings.Join(cases, " ")+" ELSE "+column+" END")
		}
		where := []string{}
		for i := start; i < end; i++ {
			clause, keys, err := primaryWhere(dialect, records[i], len(args))
			if err != nil {
				return err
			}
			args = append(args, keys...)
			where = append(where, "("+clause+")")
		}
		returning, returnNames, err := returningFields(dialect, schema)
		if err != nil {
			return err
		}
		rows, err := db.ExecutorFor(ctx, store.Backend).Query(ctx, "UPDATE "+table+" SET "+strings.Join(sets, ", ")+" WHERE "+strings.Join(where, " OR ")+" RETURNING "+returning, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			values := make([]any, len(returnNames))
			dest := make([]any, len(values))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				return err
			}
			record, err := models.NewRecord(schema)
			if err != nil {
				return err
			}
			for i, name := range returnNames {
				field, _ := schema.Field(name)
				value, err := store.decodeField(field, values[i])
				if err != nil {
					return err
				}
				if err := record.Set(name, value); err != nil {
					return err
				}
			}
			key, err := recordIdentity(record)
			if err != nil {
				return err
			}
			index, ok := identity[key]
			if !ok || index < start || index >= end {
				return errors.New("orm: unexpected bulk-update result identity")
			}
			for _, name := range returnNames {
				value, _ := record.Get(name)
				if err := records[index].Set(name, value); err != nil {
					return err
				}
			}
			pending = append(pending, index)
		}
		return rows.Err()
	}
	run := func(ctx context.Context) error {
		for start := 0; start < len(records); start += batchSize {
			end := min(start+batchSize, len(records))
			if err := runBatch(ctx, start, end); err != nil {
				return err
			}
		}
		return nil
	}
	if options.CommitEachBatch {
		for start := 0; start < len(records); start += batchSize {
			end := min(start+batchSize, len(records))
			before := len(pending)
			if err := db.Atomic(ctx, store.Backend, db.AtomicOptions{}, func(ctx context.Context) error { return runBatch(ctx, start, end) }); err != nil {
				return outcomes, &BulkError{Cause: err, CommittedBatches: append([]int(nil), completed...)}
			}
			apply(before)
			completed = append(completed, start/batchSize)
		}
	} else {
		if err := db.Atomic(ctx, store.Backend, db.AtomicOptions{}, run); err != nil {
			return outcomes, &BulkError{Cause: err}
		}
		apply(0)
	}
	if options.AfterCommit != nil {
		summary := BulkSummary{Inputs: len(objects), Written: len(pending), CommittedBatches: append([]int(nil), completed...)}
		if err := db.OnCommit(ctx, store.Backend.Alias(), func(ctx context.Context) error { return options.AfterCommit(ctx, summary) }, false); err != nil {
			return outcomes, &db.CommittedCallbackError{Errors: []error{err}}
		}
	}
	return outcomes, nil
}
