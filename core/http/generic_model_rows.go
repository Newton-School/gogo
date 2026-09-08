package http

import (
	"context"
	"net/http"
	"reflect"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// A row owns canonical, bounded values, not a driver buffer or a policy record.
// The map stays private for the lifetime of one request.
type genericModelRow struct {
	values   map[string]any
	database string
}

func (m *genericModel) readOptions(options ReadViewOptions) ReadViewOptions {
	authorize := options.Authorize
	if authorize == nil {
		return options // The common constructor rejects this configuration.
	}
	options.Authorize = func(request *http.Request) error {
		ctx := request.Context()
		if err := authorize(request); err != nil {
			return err
		}
		// Request mutation cannot replace the verified identity's context.
		return m.authorize(ctx)
	}
	return options
}

func (m *genericModel) readRows(ctx context.Context, query orm.Query[*models.MapRecord], limit int) (rows []genericModelRow, err error) {
	if limit < 1 || limit > 201 || !genericContextOK(ctx) {
		return nil, ErrUnavailable
	}
	iterator, err := query.Iterator(ctx)
	if err != nil || iterator == nil {
		return nil, ErrUnavailable
	}
	defer func() {
		// Close is required even after local size/type refusal. An error from
		// completion invalidates the entire page, including its decoded prefix.
		if closeErr := iterator.Close(); closeErr != nil || !genericContextOK(ctx) {
			rows, err = nil, ErrUnavailable
		}
	}()
	budget := templateContextBudget{remainingValues: templateContextMaxValues, remainingBytes: templateContextMaxBytes}
	jsonBudget := &genericModelJSONBudget{values: templateContextMaxValues, text: templateContextMaxBytes, raw: templateContextMaxBytes}
	identities := make(map[string]bool, len(m.schema.PKFields()))
	for _, field := range m.schema.PKFields() {
		identities[field.Name] = true
	}
	rows = make([]genericModelRow, 0, min(limit, 16))
	for iterator.Next() {
		if len(rows) >= limit || !genericContextOK(ctx) {
			return nil, ErrUnavailable
		}
		record := iterator.Value()
		if record == nil || !budget.visit(0) {
			return nil, ErrUnavailable
		}
		row := genericModelRow{values: make(map[string]any, len(m.selected)), database: record.State().Database}
		for _, name := range m.selected {
			field, _ := m.schema.Field(name)
			raw, readErr := record.Get(name)
			if readErr != nil || !budget.key(name, 1) {
				return nil, ErrUnavailable
			}
			value, readErr := normalizeGenericModelValueWithJSONBudget(field, raw, jsonBudget)
			if readErr != nil || identities[name] && value == nil {
				return nil, ErrUnavailable
			}
			// Charge all loaded policy data too. The budget's projection is used
			// only for accounting; canonical JSON-null/number identity is retained.
			if _, ok := budget.copy(reflect.ValueOf(value), 1); !ok {
				return nil, ErrUnavailable
			}
			row.values[name] = value
		}
		rows = append(rows, row)
	}
	if iterator.Err() != nil || !genericContextOK(ctx) {
		return nil, ErrUnavailable
	}
	return rows, nil
}

// Each invocation gets its own mutable record. Even a policy which changes its
// argument cannot rewrite a later field decision, identity or HTML projection.
func (m *genericModel) policyRecord(row genericModelRow) (*models.MapRecord, error) {
	record, err := models.NewRecord(m.schema)
	if err != nil {
		return nil, ErrUnavailable
	}
	record.State().Persisted = true
	record.State().Database = row.database
	record.State().Deferred = make(map[string]bool, len(m.schema.Fields))
	for _, field := range m.schema.Fields {
		if field.IsStored() {
			record.State().Deferred[field.Name] = true
		}
	}
	for _, name := range m.selected {
		field, _ := m.schema.Field(name)
		value, err := normalizeGenericModelValue(field, row.values[name])
		if err != nil || record.Set(name, value) != nil {
			return nil, ErrUnavailable
		}
	}
	return record, nil
}

func (m *genericModel) objectGrant(ctx context.Context, row genericModelRow, detail bool) error {
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	record, err := m.policyRecord(row)
	if err != nil {
		return err
	}
	identity := make(map[string]any, len(m.schema.PKFields()))
	for _, field := range m.schema.PKFields() {
		value, err := normalizeGenericModelValue(field, row.values[field.Name])
		if err != nil {
			return ErrUnavailable
		}
		identity[field.Name] = value
	}
	err = m.policy.Authorize(ctx, auth.FromContext(ctx), "view", auth.Resource{
		App: m.schema.AppLabel, Model: m.schema.Name, ID: identity, Object: record,
	})
	if !genericContextOK(ctx) {
		return ErrUnavailable
	}
	if detail && (err == auth.ErrPermissionDenied || err == ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func (m *genericModel) fieldGrant(ctx context.Context, row genericModelRow, name string) (bool, error) {
	if !genericContextOK(ctx) {
		return false, ErrUnavailable
	}
	if m.allowField == nil {
		return true, nil
	}
	record, err := m.policyRecord(row)
	if err != nil {
		return false, err
	}
	allowed, err := m.allowField(ctx, auth.FromContext(ctx), record, name)
	if err != nil || !genericContextOK(ctx) {
		return false, ErrUnavailable
	}
	return allowed, nil
}

func (m *genericModel) projectRows(ctx context.Context, rows []genericModelRow, detail bool) ([]map[string]any, func(*readViewCall) error, error) {
	projected := make([]map[string]any, 0, len(rows))
	granted := make([][]string, 0, len(rows))
	for _, row := range rows {
		if err := m.objectGrant(ctx, row, detail); err != nil {
			return nil, nil, err
		}
		output := make(map[string]any, len(m.fields))
		fields := make([]string, 0, len(m.fields))
		for _, name := range m.fields {
			allowed, err := m.fieldGrant(ctx, row, name)
			if err != nil {
				return nil, nil, err
			}
			if !allowed {
				continue
			}
			field, _ := m.schema.Field(name)
			value, err := projectGenericModelValue(field, row.values[name])
			if err != nil {
				return nil, nil, ErrUnavailable
			}
			output[name] = value
			fields = append(fields, name)
		}
		projected = append(projected, output)
		granted = append(granted, fields)
	}
	finalize := func(call *readViewCall) error {
		ctx := call.base.Context()
		for i, row := range rows {
			if err := m.objectGrant(ctx, row, detail); err != nil {
				return err
			}
			for _, name := range granted[i] {
				allowed, err := m.fieldGrant(ctx, row, name)
				if err != nil {
					return err
				}
				if !allowed {
					if detail {
						return ErrNotFound
					}
					return auth.ErrPermissionDenied
				}
			}
		}
		return nil
	}
	return projected, finalize, nil
}
