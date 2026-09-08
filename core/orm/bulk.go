package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"strings"
)

type BulkOptions struct {
	BatchSize          int
	IgnoreConflicts    bool
	ConflictConstraint string
	UpdateFields       []string
	CommitEachBatch    bool
	AfterCommit        func(context.Context, BulkSummary) error
}
type BulkSummary struct {
	Inputs, Written  int
	CommittedBatches []int
}
type BulkOutcome[T models.Model] struct {
	Input   int
	Model   T
	Written bool
}
type BulkError struct {
	Cause            error
	CommittedBatches []int
}

func (e *BulkError) Error() string { return "orm: bulk operation failed" }
func (e *BulkError) Unwrap() error { return e.Cause }

// BulkCreate skips Model.Clean and all instance Save receivers. Field default,
// insert-time and codec preparation still apply. Results are keyed by explicit
// primary/unique identity; ignored rows never receive fabricated generated IDs.
func BulkCreate[T models.Model](ctx context.Context, store *Store, objects []T, options BulkOptions) ([]BulkOutcome[T], error) {
	if e := store.refuseRouting(); e != nil {
		return nil, e
	}
	if store == nil || store.Backend == nil {
		return nil, errors.New("orm: bulk backend required")
	}
	if len(objects) == 0 {
		return []BulkOutcome[T]{}, nil
	}
	if err := store.Backend.Capabilities().Require("returning", "transactions"); err != nil {
		return nil, err
	}
	if options.IgnoreConflicts && len(options.UpdateFields) > 0 {
		return nil, errors.New("orm: ignore and update conflicts are mutually exclusive")
	}
	if options.IgnoreConflicts || len(options.UpdateFields) > 0 {
		if err := store.Backend.Capabilities().Require("on_conflict"); err != nil {
			return nil, err
		}
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
		records[i] = record
		outcomes[i] = BulkOutcome[T]{Input: i, Model: object}
	}
	if schema.Parent != "" || schema.Abstract || schema.Proxy || schema.Unmanaged {
		return nil, errors.New("orm: bulk insert requires a managed single-table concrete model")
	}
	fields := []models.Field{}
	pk := schema.PKFields()
	for _, field := range schema.Fields {
		if field.IsStored() && field.Kind != models.Generated {
			fields = append(fields, field)
		}
	}
	if len(fields) == 0 || batchSize*len(fields) > 60000 {
		return nil, errors.New("orm: reduce batch size to keep parameter count below 60000")
	}
	keyFields := []string{}
	for _, field := range pk {
		keyFields = append(keyFields, field.Name)
	}
	if len(options.UpdateFields) > 0 {
		matched := false
		for _, constraint := range schema.Constraints {
			if constraint.Name == options.ConflictConstraint && strings.EqualFold(constraint.Kind, "unique") && constraint.Condition == "" && !constraint.Deferrable {
				keyFields = constraint.Fields
				matched = true
			}
		}
		if !matched {
			return nil, errors.New("orm: bulk conflict updates require a named immediate unique constraint")
		}
		values := map[string]any{}
		for _, name := range keyFields {
			values[name] = true
		}
		if err := checkUniqueKey(schema, UniqueKey{Constraint: options.ConflictConstraint, Values: values}); err != nil {
			return nil, err
		}
		for _, name := range options.UpdateFields {
			field, ok := schema.Field(name)
			if !ok || !field.IsStored() || field.PrimaryKey || field.Kind == models.Generated {
				return nil, errors.New("orm: invalid conflict update field")
			}
			for _, key := range keyFields {
				if name == key {
					return nil, errors.New("orm: conflict key cannot be updated")
				}
			}
		}
	}
	type pendingRow struct {
		index  int
		values map[string]any
	}
	pending := []pendingRow{}
	completed := []int{}
	applyPending := func(start int) {
		for _, row := range pending[start:] {
			records[row.index].State().Persisted = true
			records[row.index].State().Database = store.Backend.Alias()
			outcomes[row.index].Written = true
		}
	}
	runBatch := func(ctx context.Context, start, end int) error {
		executor := db.ExecutorFor(ctx, store.Backend)
		values := make([]map[string]any, end-start)
		for i := start; i < end; i++ {
			if err := models.ApplyDefaults(records[i]); err != nil {
				return err
			}
			values[i-start] = map[string]any{}
			for _, field := range fields {
				value, err := store.prepare(records[i], field, true, false)
				if err != nil {
					return err
				}
				values[i-start][field.Name] = value
			}
		}
		if len(pk) == 1 && pk[0].IsAuto() {
			missing := []int{}
			for i, row := range values {
				if models.IsEmptyValue(row[pk[0].Name]) {
					missing = append(missing, i)
				}
			}
			if len(missing) > 0 {
				allocator, ok := store.Backend.(db.AutoKeyAllocator)
				if !ok {
					return &db.Error{Code: db.UnsupportedFeature, Message: "Stable bulk auto-key allocation is required"}
				}
				keys, err := allocator.ReserveAutoKeys(ctx, executor, schema, pk[0], len(missing))
				if err != nil {
					return err
				}
				for i, index := range missing {
					values[index][pk[0].Name] = keys[i]
				}
			}
		}
		identities := map[string]int{}
		for i, row := range values {
			key, err := bulkIdentity(row, keyFields)
			if err != nil {
				return err
			}
			if _, exists := identities[key]; exists {
				return errors.New("orm: duplicate input batch identity")
			}
			identities[key] = start + i
		}
		dialect := store.Backend.Dialect()
		table, err := dialect.QuoteIdentifier(schema.DBTable())
		if err != nil {
			return err
		}
		columns := []string{}
		for _, field := range fields {
			column, err := dialect.QuoteIdentifier(field.DBColumn())
			if err != nil {
				return err
			}
			columns = append(columns, column)
		}
		args := []any{}
		tuples := []string{}
		for _, row := range values {
			bound := []string{}
			for _, field := range fields {
				value := row[field.Name]
				if field.DBDefault != "" && models.IsEmptyValue(value) {
					bound = append(bound, "DEFAULT")
					continue
				}
				args = append(args, value)
				bound = append(bound, dialect.Placeholder(len(args)))
			}
			tuples = append(tuples, "("+strings.Join(bound, ",")+")")
		}
		statement := "INSERT INTO " + table + " (" + strings.Join(columns, ",") + ") VALUES " + strings.Join(tuples, ",")
		if options.IgnoreConflicts {
			statement += " ON CONFLICT DO NOTHING"
		} else if len(options.UpdateFields) > 0 {
			constraint, err := dialect.QuoteIdentifier(options.ConflictConstraint)
			if err != nil {
				return err
			}
			updates := []string{}
			for _, name := range options.UpdateFields {
				field, _ := schema.Field(name)
				column, _ := dialect.QuoteIdentifier(field.DBColumn())
				updates = append(updates, column+"=EXCLUDED."+column)
			}
			statement += " ON CONFLICT ON CONSTRAINT " + constraint + " DO UPDATE SET " + strings.Join(updates, ",")
		}
		returning, names, err := returningFields(dialect, schema)
		if err != nil {
			return err
		}
		rows, err := executor.Query(ctx, statement+" RETURNING "+returning, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			raw := make([]any, len(names))
			dest := make([]any, len(raw))
			for i := range raw {
				dest[i] = &raw[i]
			}
			if err := rows.Scan(dest...); err != nil {
				return err
			}
			returned := map[string]any{}
			for i, name := range names {
				field, _ := schema.Field(name)
				value, err := store.decodeField(field, raw[i])
				if err != nil {
					return err
				}
				returned[name] = value
			}
			key, err := bulkIdentity(returned, keyFields)
			if err != nil {
				return err
			}
			index, ok := identities[key]
			if !ok {
				return errors.New("orm: returned batch identity did not match an input")
			}
			delete(identities, key)
			for _, name := range names {
				if err := records[index].Set(name, returned[name]); err != nil {
					return err
				}
			}
			pending = append(pending, pendingRow{index, returned})
		}
		return rows.Err()
	}
	run := func(ctx context.Context) error {
		for start := 0; start < len(records); start += batchSize {
			end := start + batchSize
			if end > len(records) {
				end = len(records)
			}
			if err := runBatch(ctx, start, end); err != nil {
				return err
			}
		}
		return nil
	}
	if options.CommitEachBatch {
		for start := 0; start < len(records); start += batchSize {
			end := start + batchSize
			if end > len(records) {
				end = len(records)
			}
			before := len(pending)
			if err := db.Atomic(ctx, store.Backend, db.AtomicOptions{}, func(ctx context.Context) error { return runBatch(ctx, start, end) }); err != nil {
				return outcomes, &BulkError{Cause: err, CommittedBatches: append([]int(nil), completed...)}
			}
			applyPending(before)
			completed = append(completed, start/batchSize)
		}
	} else {
		if err := db.Atomic(ctx, store.Backend, db.AtomicOptions{}, run); err != nil {
			return outcomes, &BulkError{Cause: err}
		}
		applyPending(0)
	}
	if options.AfterCommit != nil {
		summary := BulkSummary{Inputs: len(objects), Written: len(pending), CommittedBatches: append([]int(nil), completed...)}
		if err := db.OnCommit(ctx, store.Backend.Alias(), func(ctx context.Context) error { return options.AfterCommit(ctx, summary) }, false); err != nil {
			return outcomes, &db.CommittedCallbackError{Errors: []error{err}}
		}
	}
	return outcomes, nil
}

func bulkIdentity(values map[string]any, fields []string) (string, error) {
	key := []any{}
	for _, name := range fields {
		value, ok := values[name]
		if !ok || value == nil {
			return "", errors.New("orm: bulk mapping requires non-null stable input keys")
		}
		key = append(key, value)
	}
	if len(key) == 0 {
		return "", errors.New("orm: bulk mapping key missing")
	}
	data, err := json.Marshal(key)
	if err != nil {
		return "", fmt.Errorf("orm: invalid batch identity: %w", err)
	}
	return string(data), nil
}
