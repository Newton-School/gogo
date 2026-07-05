package orm

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cybersaksham/gogo/models"
)

// MetadataFixtureRecord is one serialized row used by metadata-backed stores.
type MetadataFixtureRecord struct {
	Model  string
	PK     any
	Fields map[string]any
}

// MetadataStore persists model metadata rows through database/sql.
type MetadataStore struct {
	Database *Database
	models   map[string]models.Metadata
}

type primaryKeyCreateStrategy int

const (
	primaryKeyProvided primaryKeyCreateStrategy = iota
	primaryKeyManualInteger
	primaryKeyDatabaseGenerated
)

// NewMetadataStore creates a metadata-backed SQL row store.
func NewMetadataStore(database *Database, metas ...models.Metadata) *MetadataStore {
	store := &MetadataStore{Database: database, models: map[string]models.Metadata{}}
	for _, meta := range metas {
		if label := meta.Label(); label != "" {
			store.models[label] = meta.Clone()
		}
	}
	return store
}

// List returns all rows for a model ordered by primary key.
func (s *MetadataStore) List(ctx context.Context, meta models.Metadata) ([]map[string]any, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	meta = s.resolve(meta)
	fields := concreteFields(meta)
	query := "SELECT " + columnSelectList(s.Database, fields) + " FROM " + s.q(tableName(meta))
	if pk := primaryField(meta); pk.Name != "" {
		query += " ORDER BY " + s.q(columnName(pk)) + " ASC"
	}
	rows, err := s.Database.SQLDB().QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows, fields)
}

// Query returns filtered, ordered, and bounded rows for metadata-backed object stores.
func (s *MetadataStore) Query(ctx context.Context, meta models.Metadata, objectQuery models.ObjectQuery) (models.ObjectQueryResult, error) {
	if err := s.ready(); err != nil {
		return models.ObjectQueryResult{}, err
	}
	meta = s.resolve(meta)
	fields := concreteFields(meta)
	where, args, err := s.objectQueryWhere(meta, objectQuery)
	if err != nil {
		return models.ObjectQueryResult{}, err
	}

	result := models.ObjectQueryResult{}
	if objectQuery.IncludeTotal {
		countQuery := "SELECT COUNT(*) FROM " + s.q(tableName(meta)) + where
		if err := s.Database.SQLDB().QueryRowContext(ctx, countQuery, args...).Scan(&result.Total); err != nil {
			return models.ObjectQueryResult{}, err
		}
	}

	statement := "SELECT " + columnSelectList(s.Database, fields) + " FROM " + s.q(tableName(meta)) + where
	ordering, err := s.objectQueryOrdering(meta, objectQuery.Ordering)
	if err != nil {
		return models.ObjectQueryResult{}, err
	}
	if ordering != "" {
		statement += " ORDER BY " + ordering
	}
	queryArgs := append([]any(nil), args...)
	if objectQuery.Limit > 0 {
		statement += " LIMIT " + s.placeholder(len(queryArgs)+1)
		queryArgs = append(queryArgs, objectQuery.Limit)
		if objectQuery.Offset > 0 {
			statement += " OFFSET " + s.placeholder(len(queryArgs)+1)
			queryArgs = append(queryArgs, objectQuery.Offset)
		}
	} else if objectQuery.Offset > 0 {
		statement += " LIMIT -1 OFFSET " + s.placeholder(len(queryArgs)+1)
		queryArgs = append(queryArgs, objectQuery.Offset)
	}
	rows, err := s.Database.SQLDB().QueryContext(ctx, statement, queryArgs...)
	if err != nil {
		return models.ObjectQueryResult{}, err
	}
	defer rows.Close()
	result.Rows, err = scanRows(rows, fields)
	if err != nil {
		return models.ObjectQueryResult{}, err
	}
	if !objectQuery.IncludeTotal {
		result.Total = len(result.Rows)
	}
	return result, nil
}

// Get returns one row by primary key.
func (s *MetadataStore) Get(ctx context.Context, meta models.Metadata, pk string) (map[string]any, bool, error) {
	if err := s.ready(); err != nil {
		return nil, false, err
	}
	meta = s.resolve(meta)
	fields := concreteFields(meta)
	pkField := primaryField(meta)
	if pkField.Name == "" {
		return nil, false, fmt.Errorf("%w: primary key is required", ErrInvalidQuery)
	}
	query := "SELECT " + columnSelectList(s.Database, fields) + " FROM " + s.q(tableName(meta)) + " WHERE " + s.q(columnName(pkField)) + " = " + s.placeholder(1)
	row := s.Database.SQLDB().QueryRowContext(ctx, query, pk)
	values, err := scanRow(row, fields)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return values, true, nil
}

// Create inserts one row and returns the stored values.
func (s *MetadataStore) Create(ctx context.Context, meta models.Metadata, data map[string]any) (map[string]any, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	meta = s.resolve(meta)
	values := cloneMap(data)
	pkField := primaryField(meta)
	pkStrategy := primaryKeyStrategy(values, pkField)
	switch pkStrategy {
	case primaryKeyManualInteger:
		next, err := s.nextPrimaryKey(ctx, meta, pkField)
		if err != nil {
			return nil, err
		}
		values[pkField.Name] = next
	case primaryKeyDatabaseGenerated:
		delete(values, pkField.Name)
	}
	now := time.Now().UTC()
	applyTimestampDefaults(meta, values, now, true)
	fields := writableFields(meta, values, true)
	if len(fields) == 0 && pkStrategy != primaryKeyDatabaseGenerated {
		return nil, fmt.Errorf("%w: no writable fields", ErrInvalidQuery)
	}
	args := make([]any, len(fields))
	placeholders := make([]string, len(fields))
	for i, field := range fields {
		args[i] = values[field.Name]
		placeholders[i] = s.placeholder(i + 1)
	}
	statement := "INSERT INTO " + s.q(tableName(meta))
	if len(fields) == 0 {
		statement += " DEFAULT VALUES"
	} else {
		statement += " (" + columnNameList(s.Database, fields) + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	}
	if pkStrategy == primaryKeyDatabaseGenerated {
		if !supportsInsertReturning(s.Database) {
			return nil, fmt.Errorf("%w: database-generated primary key requires INSERT RETURNING support", ErrInvalidQuery)
		}
		return scanRow(s.Database.SQLDB().QueryRowContext(ctx, statement+" RETURNING "+columnSelectList(s.Database, concreteFields(meta)), args...), concreteFields(meta))
	}
	if _, err := s.Database.SQLDB().ExecContext(ctx, statement, args...); err != nil {
		return nil, err
	}
	if pkField.Name == "" {
		return values, nil
	}
	created, ok, err := s.Get(ctx, meta, fmt.Sprint(values[pkField.Name]))
	if err != nil || !ok {
		return values, err
	}
	return created, nil
}

// Update updates one row by primary key and returns the stored values.
func (s *MetadataStore) Update(ctx context.Context, meta models.Metadata, pk string, data map[string]any, partial bool) (map[string]any, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	meta = s.resolve(meta)
	existing, ok, err := s.Get(ctx, meta, pk)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrDoesNotExist
	}
	values := cloneMap(existing)
	if !partial {
		values = map[string]any{}
		if pkField := primaryField(meta); pkField.Name != "" {
			values[pkField.Name] = existing[pkField.Name]
		}
	}
	for key, value := range data {
		values[key] = value
	}
	applyTimestampDefaults(meta, values, time.Now().UTC(), false)
	fields := writableFields(meta, values, false)
	if len(fields) == 0 {
		return existing, nil
	}
	assignments := make([]string, len(fields))
	args := make([]any, 0, len(fields)+1)
	for i, field := range fields {
		assignments[i] = s.q(columnName(field)) + " = " + s.placeholder(i+1)
		args = append(args, values[field.Name])
	}
	pkField := primaryField(meta)
	args = append(args, pk)
	statement := "UPDATE " + s.q(tableName(meta)) + " SET " + strings.Join(assignments, ", ") + " WHERE " + s.q(columnName(pkField)) + " = " + s.placeholder(len(args))
	result, err := s.Database.SQLDB().ExecContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, ErrDoesNotExist
	}
	updated, ok, err := s.Get(ctx, meta, pk)
	if err != nil || !ok {
		return values, err
	}
	return updated, nil
}

// Delete removes one row by primary key.
func (s *MetadataStore) Delete(ctx context.Context, meta models.Metadata, pk string) error {
	if err := s.ready(); err != nil {
		return err
	}
	meta = s.resolve(meta)
	pkField := primaryField(meta)
	if pkField.Name == "" {
		return fmt.Errorf("%w: primary key is required", ErrInvalidQuery)
	}
	result, err := s.Database.SQLDB().ExecContext(ctx, "DELETE FROM "+s.q(tableName(meta))+" WHERE "+s.q(columnName(pkField))+" = "+s.placeholder(1), pk)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrDoesNotExist
	}
	return nil
}

// Dump serializes rows for selected model labels. Empty labels dump all known models.
func (s *MetadataStore) Dump(ctx context.Context, labels []string) ([]MetadataFixtureRecord, error) {
	selected := s.selectedModels(labels)
	records := []MetadataFixtureRecord{}
	for _, meta := range selected {
		rows, err := s.List(ctx, meta)
		if err != nil {
			return nil, err
		}
		pkField := primaryField(meta)
		for _, row := range rows {
			fields := cloneMap(row)
			var pk any
			if pkField.Name != "" {
				pk = fields[pkField.Name]
				delete(fields, pkField.Name)
			}
			records = append(records, MetadataFixtureRecord{Model: meta.Label(), PK: pk, Fields: fields})
		}
	}
	return records, nil
}

// Load inserts or replaces fixture records.
func (s *MetadataStore) Load(ctx context.Context, records []MetadataFixtureRecord) error {
	for _, record := range records {
		meta, ok := s.models[record.Model]
		if !ok {
			return fmt.Errorf("%w: unknown model %s", ErrInvalidQuery, record.Model)
		}
		values := cloneMap(record.Fields)
		if pkField := primaryField(meta); pkField.Name != "" && record.PK != nil {
			values[pkField.Name] = record.PK
		}
		if pkField := primaryField(meta); pkField.Name != "" && record.PK != nil {
			if _, ok, err := s.Get(ctx, meta, fmt.Sprint(record.PK)); err != nil {
				return err
			} else if ok {
				if _, err := s.Update(ctx, meta, fmt.Sprint(record.PK), values, true); err != nil {
					return err
				}
				continue
			}
		}
		if _, err := s.Create(ctx, meta, values); err != nil {
			return err
		}
	}
	return nil
}

func primaryKeyStrategy(values map[string]any, field models.FieldMeta) primaryKeyCreateStrategy {
	if field.Name == "" {
		return primaryKeyProvided
	}
	if !emptyValue(values[field.Name]) {
		return primaryKeyProvided
	}
	if hasDatabaseDefault(field) || isAutoPrimaryKey(field) || isIdentityPrimaryKey(field) {
		return primaryKeyDatabaseGenerated
	}
	return primaryKeyManualInteger
}

func hasDatabaseDefault(field models.FieldMeta) bool {
	defaultValue, err := models.NormalizeDatabaseDefault(field.DBDefault)
	return err == nil && defaultValue.Kind != models.DefaultNone
}

func isAutoPrimaryKey(field models.FieldMeta) bool {
	switch strings.ToLower(strings.TrimSpace(field.Kind)) {
	case "auto", "big_auto", "small_auto", "serial", "bigserial", "smallserial":
		return true
	default:
		return false
	}
}

func isIdentityPrimaryKey(field models.FieldMeta) bool {
	return strings.Contains(strings.ToLower(field.Kind), "identity")
}

func supportsInsertReturning(database *Database) bool {
	return database != nil && database.Dialect != nil && database.Dialect.SupportsReturning()
}

func (s *MetadataStore) ready() error {
	if s == nil || s.Database == nil || s.Database.SQLDB() == nil {
		return ErrDatabaseNotFound
	}
	return nil
}

func (s *MetadataStore) objectQueryWhere(meta models.Metadata, query models.ObjectQuery) (string, []any, error) {
	fields := fieldMetaByName(meta)
	var parts []string
	var args []any
	for key, values := range query.Filters {
		fieldName, lookup := objectQueryLookup(key)
		field, ok := fields[fieldName]
		if !ok {
			return "", nil, fmt.Errorf("%w: unknown filter field %s", ErrInvalidQuery, fieldName)
		}
		clause, clauseArgs, err := s.objectQueryPredicate(field, lookup, values, len(args)+1)
		if err != nil {
			return "", nil, err
		}
		if clause != "" {
			parts = append(parts, clause)
			args = append(args, clauseArgs...)
		}
	}
	if strings.TrimSpace(query.Search) != "" && len(query.SearchFields) > 0 {
		searchClause, searchArgs, err := s.objectQuerySearch(fields, query.SearchFields, query.Search, len(args)+1)
		if err != nil {
			return "", nil, err
		}
		if searchClause != "" {
			parts = append(parts, searchClause)
			args = append(args, searchArgs...)
		}
	}
	if len(parts) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(parts, " AND "), args, nil
}

func (s *MetadataStore) objectQueryPredicate(field models.FieldMeta, lookup string, values []string, placeholderStart int) (string, []any, error) {
	column := s.q(columnName(field))
	switch lookup {
	case "", "exact":
		if len(values) == 0 {
			return "", nil, nil
		}
		return column + " = " + s.placeholder(placeholderStart), []any{values[0]}, nil
	case "contains", "icontains":
		if len(values) == 0 {
			return "", nil, nil
		}
		return "LOWER(" + column + ") LIKE LOWER(" + s.placeholder(placeholderStart) + ")", []any{"%" + values[0] + "%"}, nil
	case "in":
		if len(values) == 0 {
			return "", nil, nil
		}
		placeholders := make([]string, len(values))
		args := make([]any, len(values))
		for i, value := range values {
			placeholders[i] = s.placeholder(placeholderStart + i)
			args[i] = value
		}
		return column + " IN (" + strings.Join(placeholders, ", ") + ")", args, nil
	default:
		return "", nil, fmt.Errorf("%w: unsupported lookup %s", ErrInvalidQuery, lookup)
	}
}

func (s *MetadataStore) objectQuerySearch(fields map[string]models.FieldMeta, searchFields []string, term string, placeholderStart int) (string, []any, error) {
	var parts []string
	var args []any
	for _, raw := range searchFields {
		lookup, fieldName := objectQuerySearchLookup(raw)
		field, ok := fields[fieldName]
		if !ok {
			return "", nil, fmt.Errorf("%w: unknown search field %s", ErrInvalidQuery, fieldName)
		}
		column := s.q(columnName(field))
		switch lookup {
		case "exact":
			parts = append(parts, column+" = "+s.placeholder(placeholderStart+len(args)))
			args = append(args, term)
		case "startswith":
			parts = append(parts, "LOWER("+column+") LIKE LOWER("+s.placeholder(placeholderStart+len(args))+")")
			args = append(args, term+"%")
		default:
			parts = append(parts, "LOWER("+column+") LIKE LOWER("+s.placeholder(placeholderStart+len(args))+")")
			args = append(args, "%"+term+"%")
		}
	}
	if len(parts) == 0 {
		return "", nil, nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", args, nil
}

func (s *MetadataStore) objectQueryOrdering(meta models.Metadata, ordering []string) (string, error) {
	fields := fieldMetaByName(meta)
	var parts []string
	for _, raw := range ordering {
		desc := strings.HasPrefix(raw, "-")
		fieldName := strings.TrimPrefix(raw, "-")
		field, ok := fields[fieldName]
		if !ok {
			return "", fmt.Errorf("%w: unknown ordering field %s", ErrInvalidQuery, fieldName)
		}
		part := s.q(columnName(field))
		if desc {
			part += " DESC"
		} else {
			part += " ASC"
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		if pk := primaryField(meta); pk.Name != "" {
			return s.q(columnName(pk)) + " ASC", nil
		}
	}
	return strings.Join(parts, ", "), nil
}

func objectQueryLookup(raw string) (string, string) {
	parts := strings.Split(raw, "__")
	if len(parts) == 1 {
		return raw, "exact"
	}
	return parts[0], parts[len(parts)-1]
}

func objectQuerySearchLookup(raw string) (string, string) {
	if raw == "" {
		return "contains", raw
	}
	switch raw[0] {
	case '=':
		return "exact", raw[1:]
	case '^':
		return "startswith", raw[1:]
	case '@':
		return "contains", raw[1:]
	default:
		return "contains", raw
	}
}

func fieldMetaByName(meta models.Metadata) map[string]models.FieldMeta {
	fields := make(map[string]models.FieldMeta, len(meta.Fields))
	for _, field := range meta.Fields {
		if field.Name != "" {
			fields[field.Name] = field
		}
	}
	return fields
}

func (s *MetadataStore) resolve(meta models.Metadata) models.Metadata {
	if stored, ok := s.models[meta.Label()]; ok {
		return stored.Clone()
	}
	return meta.Clone()
}

func (s *MetadataStore) selectedModels(labels []string) []models.Metadata {
	if len(labels) == 0 {
		labels = make([]string, 0, len(s.models))
		for label := range s.models {
			labels = append(labels, label)
		}
	}
	sort.Strings(labels)
	metas := make([]models.Metadata, 0, len(labels))
	for _, label := range labels {
		if meta, ok := s.models[label]; ok {
			metas = append(metas, meta.Clone())
		}
	}
	return metas
}

func (s *MetadataStore) nextPrimaryKey(ctx context.Context, meta models.Metadata, field models.FieldMeta) (int64, error) {
	query := "SELECT COALESCE(MAX(" + s.q(columnName(field)) + "), 0) + 1 FROM " + s.q(tableName(meta))
	var next int64
	if err := s.Database.SQLDB().QueryRowContext(ctx, query).Scan(&next); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *MetadataStore) q(identifier string) string {
	if s.Database != nil && s.Database.Dialect != nil {
		return s.Database.Dialect.QuoteIdent(identifier)
	}
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func (s *MetadataStore) placeholder(position int) string {
	if s.Database != nil && s.Database.Dialect != nil {
		return s.Database.Dialect.Placeholder(position)
	}
	return "?"
}

func scanRows(rows *sql.Rows, fields []models.FieldMeta) ([]map[string]any, error) {
	result := []map[string]any{}
	for rows.Next() {
		row, err := scanRow(rows, fields)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func scanRow(scanner interface{ Scan(...any) error }, fields []models.FieldMeta) (map[string]any, error) {
	values := make([]any, len(fields))
	dest := make([]any, len(fields))
	for i := range values {
		dest[i] = &values[i]
	}
	if err := scanner.Scan(dest...); err != nil {
		return nil, err
	}
	row := make(map[string]any, len(fields))
	for i, field := range fields {
		row[field.Name] = normalizeSQLValue(values[i])
	}
	return row, nil
}

func normalizeSQLValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return typed
	case string:
		if parsed, err := strconv.ParseInt(typed, 10, 64); err == nil {
			return parsed
		}
		return typed
	default:
		return typed
	}
}

func concreteFields(meta models.Metadata) []models.FieldMeta {
	fields := make([]models.FieldMeta, 0, len(meta.Fields))
	for _, field := range meta.Fields {
		if field.Name == "" {
			continue
		}
		fields = append(fields, field)
	}
	return fields
}

func writableFields(meta models.Metadata, values map[string]any, includePrimary bool) []models.FieldMeta {
	fields := []models.FieldMeta{}
	for _, field := range concreteFields(meta) {
		if field.PrimaryKey && !includePrimary {
			continue
		}
		if _, ok := values[field.Name]; !ok {
			continue
		}
		fields = append(fields, field)
	}
	return fields
}

func primaryField(meta models.Metadata) models.FieldMeta {
	for _, field := range meta.Fields {
		if field.PrimaryKey {
			return field
		}
	}
	return models.FieldMeta{}
}

func tableName(meta models.Metadata) string {
	if meta.DBTable != "" {
		return meta.DBTable
	}
	if meta.TableName != "" {
		return meta.TableName
	}
	return strings.ToLower(meta.AppLabel + "_" + meta.ModelName)
}

func columnName(field models.FieldMeta) string {
	if field.Column != "" {
		return field.Column
	}
	return field.Name
}

func columnSelectList(database *Database, fields []models.FieldMeta) string {
	return columnNameList(database, fields)
}

func columnNameList(database *Database, fields []models.FieldMeta) string {
	quoted := make([]string, len(fields))
	for i, field := range fields {
		if database != nil && database.Dialect != nil {
			quoted[i] = database.Dialect.QuoteIdent(columnName(field))
		} else {
			quoted[i] = `"` + strings.ReplaceAll(columnName(field), `"`, `""`) + `"`
		}
	}
	return strings.Join(quoted, ", ")
}

func applyTimestampDefaults(meta models.Metadata, values map[string]any, now time.Time, create bool) {
	for _, field := range meta.Fields {
		switch field.Name {
		case "created_at":
			if create && emptyValue(values[field.Name]) {
				values[field.Name] = now
			}
		case "updated_at":
			values[field.Name] = now
		}
	}
}

func emptyValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	default:
		return false
	}
}

func cloneMap(values map[string]any) map[string]any {
	copied := make(map[string]any, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}
