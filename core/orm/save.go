package orm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"strings"
	"time"
)

type SaveOptions struct {
	ForceInsert, ForceUpdate bool
	UpdateFields             []string
	Raw                      bool
	// Guard is a read-only final application policy check after hooks/defaults
	// and field preparation, immediately before each UPDATE/INSERT statement.
	// It receives the full model record and may run again for fallback/parent
	// writes. Empty UpdateFields remains a no-op; bulk methods bypass Save.
	Guard       func(context.Context, models.Record) error
	guardRecord models.Record
}
type SaveEvent struct {
	Model        models.Model
	Record       models.Record
	Created      bool
	UpdateFields []string
	Raw          bool
}
type SaveReceiver func(context.Context, SaveEvent) error

func (s *Store) Save(ctx context.Context, model models.Model, options SaveOptions) error {
	if s == nil || s.Backend == nil {
		return errors.New("orm: backend required")
	}
	record, err := models.Bind(model)
	if err != nil {
		return err
	}
	schema := record.Schema()
	options.guardRecord = record
	if options.ForceInsert && (options.ForceUpdate || len(options.UpdateFields) > 0) {
		return errors.New("orm: cannot force both insert and update")
	}
	keys := map[string]bool{}
	for _, f := range schema.PKFields() {
		keys[f.Name] = true
	}
	for _, name := range options.UpdateFields {
		f, ok := schema.Field(name)
		if !ok || !f.IsStored() || keys[name] || f.Kind == models.Generated {
			return fmt.Errorf("orm: invalid update field %s", name)
		}
	}
	if options.UpdateFields != nil && len(options.UpdateFields) == 0 {
		return nil
	}
	if options.UpdateFields == nil && record.State().Persisted && len(record.State().Deferred) > 0 && !options.ForceInsert {
		options.UpdateFields = []string{}
		for _, f := range schema.Fields {
			if f.IsStored() && !keys[f.Name] && !record.State().Deferred[f.Name] && f.Kind != models.Generated {
				options.UpdateFields = append(options.UpdateFields, f.Name)
			}
		}
		if len(options.UpdateFields) == 0 {
			return nil
		}
	}
	event := SaveEvent{Model: model, Record: record, UpdateFields: append([]string(nil), options.UpdateFields...), Raw: options.Raw}
	for _, receiver := range s.BeforeSave {
		if err := receiver(ctx, event); err != nil {
			return err
		}
	}
	if !options.Raw {
		if err := models.ApplyDefaults(record); err != nil {
			return err
		}
	}
	var created bool
	write := func(txCtx context.Context) error {
		var err error
		if schema.Parent != "" {
			if s.Registry == nil {
				return errors.New("orm: multi-table model requires registry")
			}
			parent, ok := s.Registry.Get(schema.Parent)
			if !ok {
				return errors.New("orm: unknown concrete parent")
			}
			view := schemaRecord{Record: record, schema: parent}
			if _, err = s.saveTable(txCtx, view, options); err != nil {
				return err
			}
		}
		created, err = s.saveTable(txCtx, record, options)
		return err
	}
	if schema.Parent != "" {
		err = db.Atomic(ctx, s.Backend, db.AtomicOptions{}, write)
	} else {
		err = write(ctx)
	}
	if err != nil {
		return err
	}
	record.State().Persisted = true
	record.State().Database = s.Backend.Alias()
	record.State().Related = nil
	event.Created = created
	for _, receiver := range s.AfterSave {
		if err := receiver(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

type schemaRecord struct {
	models.Record
	schema models.Schema
}

func (r schemaRecord) Schema() models.Schema { return r.schema }
func (s *Store) saveTable(ctx context.Context, record models.Record, options SaveOptions) (bool, error) {
	schema := record.Schema()
	pkSet := true
	defaultPK := true
	for _, f := range schema.PKFields() {
		value, err := record.Get(f.Name)
		if err != nil {
			return false, err
		}
		if models.IsEmptyValue(value) {
			pkSet = false
		}
		defaultPK = defaultPK && f.HasDefault()
	}
	forced := options.ForceUpdate || options.UpdateFields != nil
	if forced && !pkSet {
		return false, errors.New("orm: cannot force update without primary key")
	}
	insert := options.ForceInsert || !pkSet || (record.State().Adding() && defaultPK && !forced)
	if !insert {
		updated, err := s.update(ctx, record, options)
		if err != nil {
			return false, err
		}
		if updated {
			return false, nil
		}
		if forced {
			return false, ErrNotUpdated
		}
	}
	return true, s.insert(ctx, record, options)
}
func (s *Store) prepare(record models.Record, field models.Field, add, raw bool) (any, error) {
	value, err := record.Get(field.Name)
	if err != nil {
		return nil, err
	}
	if !raw && (field.AutoNow || (field.AutoNowAdd && add)) {
		value = time.Now().UTC()
		if field.Kind == models.Date {
			value = value.(time.Time).Truncate(24 * time.Hour)
		}
		if err := record.Set(field.Name, value); err != nil {
			return nil, err
		}
	}
	return encodeField(field, value)
}
func (s *Store) update(ctx context.Context, record models.Record, options SaveOptions) (bool, error) {
	schema := record.Schema()
	dialect := s.Backend.Dialect()
	table, err := dialect.QuoteIdentifier(schema.DBTable())
	if err != nil {
		return false, err
	}
	pkNames := map[string]bool{}
	for _, f := range schema.PKFields() {
		pkNames[f.Name] = true
	}
	selected := map[string]bool{}
	for _, name := range options.UpdateFields {
		selected[name] = true
	}
	parts := []string{}
	args := []any{}
	for _, f := range schema.Fields {
		if !f.IsStored() || f.Kind == models.Generated || pkNames[f.Name] || (options.UpdateFields != nil && !selected[f.Name]) {
			continue
		}
		value, err := s.prepare(record, f, false, options.Raw)
		if err != nil {
			return false, err
		}
		column, err := dialect.QuoteIdentifier(f.DBColumn())
		if err != nil {
			return false, err
		}
		args = append(args, value)
		parts = append(parts, column+" = "+dialect.Placeholder(len(args)))
	}
	where, keys, err := primaryWhere(dialect, record, len(args))
	if err != nil {
		return false, err
	}
	args = append(args, keys...)
	if options.Guard != nil {
		if err := options.Guard(ctx, options.guardRecord); err != nil {
			return false, err
		}
	}
	if len(parts) == 0 {
		var count int
		err := db.QueryRow(ctx, db.ExecutorFor(ctx, s.Backend), "SELECT 1 FROM "+table+" WHERE "+where, args, &count)
		if errors.Is(err, db.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	}
	returning, names, err := returningFields(dialect, schema)
	if err != nil {
		return false, err
	}
	query := "UPDATE " + table + " SET " + strings.Join(parts, ", ") + " WHERE " + where + " RETURNING " + returning
	return s.returning(ctx, record, query, args, names)
}
func (s *Store) insert(ctx context.Context, record models.Record, options SaveOptions) error {
	schema := record.Schema()
	dialect := s.Backend.Dialect()
	table, err := dialect.QuoteIdentifier(schema.DBTable())
	if err != nil {
		return err
	}
	columns, values := []string{}, []string{}
	args := []any{}
	for _, f := range schema.Fields {
		if !f.IsStored() || f.Kind == models.Generated {
			continue
		}
		value, err := record.Get(f.Name)
		if err != nil {
			return err
		}
		if (f.IsAuto() || f.DBDefault != "") && models.IsEmptyValue(value) {
			continue
		}
		value, err = s.prepare(record, f, true, options.Raw)
		if err != nil {
			return err
		}
		column, err := dialect.QuoteIdentifier(f.DBColumn())
		if err != nil {
			return err
		}
		columns = append(columns, column)
		args = append(args, value)
		values = append(values, dialect.Placeholder(len(args)))
	}
	returning, names, err := returningFields(dialect, schema)
	if err != nil {
		return err
	}
	query := "INSERT INTO " + table
	if len(columns) == 0 {
		query += " DEFAULT VALUES"
	} else {
		query += " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(values, ", ") + ")"
	}
	query += " RETURNING " + returning
	if options.Guard != nil {
		if err := options.Guard(ctx, options.guardRecord); err != nil {
			return err
		}
	}
	_, err = s.returning(ctx, record, query, args, names)
	return err
}
func (s *Store) returning(ctx context.Context, record models.Record, query string, args []any, names []string) (bool, error) {
	rows, err := db.ExecutorFor(ctx, s.Backend).Query(ctx, query, args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	values := make([]any, len(names))
	dest := make([]any, len(names))
	for i := range values {
		dest[i] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return false, err
	}
	for i, name := range names {
		field, _ := record.Schema().Field(name)
		value, err := decodeField(field, values[i])
		if err == nil {
			err = record.Set(name, value)
		}
		if err != nil {
			return false, err
		}
	}
	return true, rows.Err()
}
func returningFields(dialect db.Dialect, schema models.Schema) (string, []string, error) {
	columns, names := []string{}, []string{}
	for _, f := range schema.Fields {
		if !f.IsStored() {
			continue
		}
		column, err := dialect.QuoteIdentifier(f.DBColumn())
		if err != nil {
			return "", nil, err
		}
		columns = append(columns, column)
		names = append(names, f.Name)
	}
	return strings.Join(columns, ", "), names, nil
}
func primaryWhere(dialect db.Dialect, record models.Record, start int) (string, []any, error) {
	parts := []string{}
	args := []any{}
	for _, field := range record.Schema().PKFields() {
		column, err := dialect.QuoteIdentifier(field.DBColumn())
		if err != nil {
			return "", nil, err
		}
		value, err := record.Get(field.Name)
		if err != nil {
			return "", nil, err
		}
		value, err = encodeField(field, value)
		if err != nil {
			return "", nil, err
		}
		args = append(args, value)
		parts = append(parts, column+" = "+dialect.Placeholder(start+len(args)))
	}
	if len(parts) == 0 {
		return "", nil, errors.New("orm: missing primary key")
	}
	return strings.Join(parts, " AND "), args, nil
}
func encodeField(field models.Field, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if field.Codec != nil {
		return field.Codec.Encode(value)
	}
	switch field.Kind {
	case models.JSON:
		if raw, ok := value.(json.RawMessage); ok && raw == nil {
			return nil, nil
		}
		b, err := json.Marshal(value)
		return string(b), err
	case models.Duration:
		if duration, ok := value.(time.Duration); ok {
			return fmt.Sprintf("%d microseconds", duration/time.Microsecond), nil
		}
	}
	return value, nil
}
func decodeField(field models.Field, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if field.Codec != nil {
		return field.Codec.Decode(value)
	}
	if field.Kind == models.JSON {
		var data []byte
		switch v := value.(type) {
		case []byte:
			data = v
		case string:
			data = []byte(v)
		default:
			return value, nil
		}
		var result any
		if !json.Valid(data) {
			return nil, errors.New("orm: invalid JSON returned by database")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&result); err != nil {
			return nil, err
		}
		if result == nil {
			return models.JSONNull, nil
		}
		return result, nil
	}
	return value, nil
}
func (s *Store) RefreshFromDB(ctx context.Context, model models.Model, fields ...string) error {
	record, err := models.Bind(model)
	if err != nil {
		return err
	}
	dialect := s.Backend.Dialect()
	table, err := dialect.QuoteIdentifier(record.Schema().DBTable())
	if err != nil {
		return err
	}
	names := fields
	if len(names) == 0 {
		for _, f := range record.Schema().Fields {
			if f.IsStored() {
				names = append(names, f.Name)
			}
		}
	}
	columns := []string{}
	for _, name := range names {
		f, ok := record.Schema().Field(name)
		if !ok {
			return fmt.Errorf("orm: unknown field %s", name)
		}
		column, err := dialect.QuoteIdentifier(f.DBColumn())
		if err != nil {
			return err
		}
		columns = append(columns, column)
	}
	where, args, err := primaryWhere(dialect, record, 0)
	if err != nil {
		return err
	}
	found, err := s.returning(ctx, record, "SELECT "+strings.Join(columns, ", ")+" FROM "+table+" WHERE "+where, args, names)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	record.State().Related = nil
	return nil
}
