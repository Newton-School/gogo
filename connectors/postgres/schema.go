package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"strconv"
	"strings"
)

type SchemaEditor struct {
	Dialect Dialect
	schemas map[string]models.Schema
}

func (e SchemaEditor) WithSchemas(schemas []models.Schema) (db.SchemaEditor, error) {
	e.schemas = map[string]models.Schema{}
	for _, schema := range schemas {
		if err := schema.Validate(); err != nil {
			return nil, err
		}
		e.schemas[schema.Key()] = schema.Clone()
	}
	return e, nil
}

func (e SchemaEditor) relationTarget(field models.Field) (models.Schema, models.Field, error) {
	if field.Relation == nil {
		return models.Schema{}, models.Field{}, errors.New("postgres: relation metadata missing")
	}
	target, ok := e.schemas[field.Relation.Target]
	if !ok {
		return target, models.Field{}, errors.New("postgres: historical relation target schema missing")
	}
	keys := field.Relation.TargetFields
	if len(keys) == 0 {
		for _, key := range target.PKFields() {
			keys = append(keys, key.Name)
		}
	}
	if len(keys) != 1 {
		return target, models.Field{}, errors.New("postgres: scalar relation requires exactly one target field")
	}
	value, ok := target.Field(keys[0])
	if !ok {
		return target, value, errors.New("postgres: unknown relation target field")
	}
	return target, value, nil
}

func (e SchemaEditor) column(field models.Field) (string, error) {
	name, err := e.Dialect.QuoteIdentifier(field.DBColumn())
	if err != nil {
		return "", err
	}
	typeField := field
	var reference string
	if field.Kind == models.ForeignKey || field.Kind == models.OneToOne {
		target, targetField, err := e.relationTarget(field)
		if err != nil {
			return "", err
		}
		typeField = targetField
		switch typeField.Kind {
		case models.SmallAuto:
			typeField.Kind = models.SmallInteger
		case models.Auto:
			typeField.Kind = models.Integer
		case models.BigAuto:
			typeField.Kind = models.BigInteger
		}
		if !field.Relation.NoConstraint {
			table, err := e.Dialect.QuoteIdentifier(target.DBTable())
			if err != nil {
				return "", err
			}
			column, err := e.Dialect.QuoteIdentifier(targetField.DBColumn())
			if err != nil {
				return "", err
			}
			// Cascades are owned by the ORM collector. Database NO ACTION prevents
			// a concurrent or invisible child from bypassing its scope and hooks.
			reference = " REFERENCES " + table + " (" + column + ") DEFERRABLE INITIALLY DEFERRED"
		}
	}
	kind, err := e.Dialect.FieldType(typeField)
	if err != nil {
		return "", err
	}
	sql := name + " " + kind
	if !field.Null {
		sql += " NOT NULL"
	}
	if field.Unique || field.Kind == models.OneToOne {
		sql += " UNIQUE"
	}
	if field.DBDefault != "" {
		if strings.ContainsAny(field.DBDefault, ";\x00") {
			return "", errors.New("postgres: invalid default expression")
		}
		sql += " DEFAULT " + field.DBDefault
	}
	if field.Kind == models.PositiveSmallInteger || field.Kind == models.PositiveInteger || field.Kind == models.PositiveBigInteger {
		sql += " CHECK (" + name + " >= 0)"
	}
	return sql + reference, nil
}
func (e SchemaEditor) CreateModel(ctx context.Context, executor db.Executor, schema models.Schema) error {
	if schema.Unmanaged || schema.Proxy || schema.Abstract {
		return nil
	}
	if err := schema.Validate(); err != nil {
		return err
	}
	table, err := e.Dialect.QuoteIdentifier(schema.DBTable())
	if err != nil {
		return err
	}
	parts := []string{}
	for _, field := range schema.Fields {
		if !field.IsStored() {
			continue
		}
		column, err := e.column(field)
		if err != nil {
			return err
		}
		parts = append(parts, column)
	}
	pks := []string{}
	for _, field := range schema.PKFields() {
		name, _ := e.Dialect.QuoteIdentifier(field.DBColumn())
		pks = append(pks, name)
	}
	parts = append(parts, "PRIMARY KEY ("+strings.Join(pks, ", ")+")")
	for _, constraint := range schema.Constraints {
		if strings.EqualFold(constraint.Kind, "unique") && constraint.Condition != "" {
			continue
		}
		sql, err := e.constraint(schema, constraint)
		if err != nil {
			return err
		}
		parts = append(parts, sql)
	}
	if _, err := executor.Exec(ctx, "CREATE TABLE "+table+" ("+strings.Join(parts, ", ")+")"); err != nil {
		return err
	}
	for _, index := range schema.Indexes {
		if err := e.AddIndex(ctx, executor, schema, index); err != nil {
			return err
		}
	}
	for _, constraint := range schema.Constraints {
		if strings.EqualFold(constraint.Kind, "unique") && constraint.Condition != "" {
			if constraint.Deferrable {
				return errors.New("postgres: conditional unique constraints cannot be deferred")
			}
			if err := e.AddIndex(ctx, executor, schema, models.Index{Name: constraint.Name, Fields: constraint.Fields, Unique: true, Condition: constraint.Condition, NullsDistinct: constraint.NullsDistinct}); err != nil {
				return err
			}
		}
	}
	return nil
}
func (e SchemaEditor) constraint(schema models.Schema, constraint models.Constraint) (string, error) {
	name, err := e.Dialect.QuoteIdentifier(constraint.Name)
	if err != nil {
		return "", err
	}
	sql := "CONSTRAINT " + name + " "
	switch strings.ToLower(constraint.Kind) {
	case "unique":
		fields := []string{}
		for _, key := range constraint.Fields {
			field, ok := schema.Field(key)
			if !ok {
				return "", fmt.Errorf("postgres: unknown constraint field %s", key)
			}
			column, _ := e.Dialect.QuoteIdentifier(field.DBColumn())
			fields = append(fields, column)
		}
		if len(fields) == 0 {
			return "", errors.New("postgres: empty unique constraint")
		}
		sql += "UNIQUE "
		if constraint.NullsDistinct != nil && !*constraint.NullsDistinct {
			sql += "NULLS NOT DISTINCT "
		}
		sql += "(" + strings.Join(fields, ", ") + ")"
		if constraint.Deferrable {
			sql += " DEFERRABLE INITIALLY DEFERRED"
		}
	case "check":
		if constraint.Expression == "" || strings.ContainsAny(constraint.Expression, ";\x00") {
			return "", errors.New("postgres: invalid check expression")
		}
		sql += "CHECK (" + constraint.Expression + ")"
	default:
		return "", &db.Error{Code: db.UnsupportedFeature, Message: "Unsupported constraint kind"}
	}
	return sql, nil
}
func (e SchemaEditor) DeleteModel(ctx context.Context, executor db.Executor, schema models.Schema) error {
	table, err := e.Dialect.QuoteIdentifier(schema.DBTable())
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, "DROP TABLE "+table)
	return err
}
func (e SchemaEditor) AddField(ctx context.Context, executor db.Executor, schema models.Schema, field models.Field) error {
	table, err := e.Dialect.QuoteIdentifier(schema.DBTable())
	if err != nil {
		return err
	}
	column, err := e.column(field)
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column)
	return err
}
func (e SchemaEditor) RemoveField(ctx context.Context, executor db.Executor, schema models.Schema, field models.Field) error {
	table, err := e.Dialect.QuoteIdentifier(schema.DBTable())
	if err != nil {
		return err
	}
	column, err := e.Dialect.QuoteIdentifier(field.DBColumn())
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, "ALTER TABLE "+table+" DROP COLUMN "+column)
	return err
}
func (e SchemaEditor) RenameField(ctx context.Context, executor db.Executor, schema models.Schema, old, new string) error {
	table, err := e.Dialect.QuoteIdentifier(schema.DBTable())
	if err != nil {
		return err
	}
	oldQ, err := e.Dialect.QuoteIdentifier(old)
	if err != nil {
		return err
	}
	newQ, err := e.Dialect.QuoteIdentifier(new)
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, "ALTER TABLE "+table+" RENAME COLUMN "+oldQ+" TO "+newQ)
	return err
}
func (e SchemaEditor) AlterField(ctx context.Context, executor db.Executor, schema models.Schema, old, new models.Field) error {
	table, err := e.Dialect.QuoteIdentifier(schema.DBTable())
	if err != nil {
		return err
	}
	column, err := e.Dialect.QuoteIdentifier(old.DBColumn())
	if err != nil {
		return err
	}
	kind, err := e.Dialect.FieldType(new)
	if err != nil {
		return err
	}
	if old.IsAuto() || new.IsAuto() {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Identity alteration requires explicit migration"}
	}
	clauses := []string{"ALTER COLUMN " + column + " TYPE " + kind}
	nullOp := " SET NOT NULL"
	if new.Null {
		nullOp = " DROP NOT NULL"
	}
	clauses = append(clauses, "ALTER COLUMN "+column+nullOp)
	if new.DBDefault == "" {
		clauses = append(clauses, "ALTER COLUMN "+column+" DROP DEFAULT")
	} else {
		if strings.ContainsAny(new.DBDefault, ";\x00") {
			return errors.New("postgres: invalid default expression")
		}
		clauses = append(clauses, "ALTER COLUMN "+column+" SET DEFAULT "+new.DBDefault)
	}
	_, err = executor.Exec(ctx, "ALTER TABLE "+table+" "+strings.Join(clauses, ", "))
	return err
}
func (e SchemaEditor) AddIndex(ctx context.Context, executor db.Executor, schema models.Schema, index models.Index) error {
	name, err := e.Dialect.QuoteIdentifier(index.Name)
	if err != nil {
		return err
	}
	table, err := e.Dialect.QuoteIdentifier(schema.DBTable())
	if err != nil {
		return err
	}
	prefix := "CREATE "
	if index.Unique {
		prefix += "UNIQUE "
	}
	prefix += "INDEX "
	if index.Concurrent {
		prefix += "CONCURRENTLY "
	}
	prefix += name + " ON " + table
	method := strings.ToLower(index.Method)
	if method != "" {
		switch method {
		case "btree", "hash", "gin", "gist", "spgist", "brin":
			prefix += " USING " + method
		default:
			return errors.New("postgres: unsupported index method")
		}
	}
	columns := []string{}
	for _, key := range index.Fields {
		field, ok := schema.Field(key)
		if !ok {
			return fmt.Errorf("postgres: unknown index field %s", key)
		}
		column, _ := e.Dialect.QuoteIdentifier(field.DBColumn())
		columns = append(columns, column)
	}
	if len(columns) == 0 {
		return errors.New("postgres: index needs fields")
	}
	prefix += " (" + strings.Join(columns, ", ") + ")"
	if len(index.Include) > 0 {
		columns := []string{}
		for _, name := range index.Include {
			field, ok := schema.Field(name)
			if !ok {
				return errors.New("postgres: unknown included index field")
			}
			quoted, err := e.Dialect.QuoteIdentifier(field.DBColumn())
			if err != nil {
				return err
			}
			columns = append(columns, quoted)
		}
		prefix += " INCLUDE (" + strings.Join(columns, ", ") + ")"
	}
	if index.NullsDistinct != nil {
		if !index.Unique {
			return errors.New("postgres: NULLS DISTINCT option requires a unique index")
		}
		if !*index.NullsDistinct {
			prefix += " NULLS NOT DISTINCT"
		}
	}
	if index.Condition != "" {
		if strings.ContainsAny(index.Condition, ";\x00") {
			return errors.New("postgres: invalid index predicate")
		}
		prefix += " WHERE " + index.Condition
	}
	_, err = executor.Exec(ctx, prefix)
	return err
}
func (e SchemaEditor) RemoveIndex(ctx context.Context, executor db.Executor, name string) error {
	quoted, err := e.Dialect.QuoteIdentifier(name)
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, "DROP INDEX "+quoted)
	return err
}

type Introspector struct{}

func (Introspector) Tables(ctx context.Context, executor db.Executor) ([]string, error) {
	rows, err := executor.Query(ctx, "SELECT tablename FROM pg_tables WHERE schemaname=current_schema() ORDER BY tablename")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}
func (Introspector) Introspect(ctx context.Context, executor db.Executor, table string) (models.Schema, error) {
	schema := models.Schema{AppLabel: "legacy", Name: table, Table: table, Unmanaged: true}
	if !models.ValidIdentifier(table) {
		return schema, errors.New("postgres: invalid table")
	}
	rows, err := executor.Query(ctx, `SELECT a.attname, format_type(a.atttypid,a.atttypmod), a.attnotnull, a.attidentity, COALESCE(pg_get_expr(d.adbin,d.adrelid),''), EXISTS(SELECT 1 FROM pg_index i WHERE i.indrelid=a.attrelid AND i.indisprimary AND a.attnum=ANY(i.indkey)) FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE c.relname=$1 AND n.nspname=current_schema() AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, table)
	if err != nil {
		return schema, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, kind, identity, def string
		var notNull, pk bool
		if err := rows.Scan(&name, &kind, &notNull, &identity, &def, &pk); err != nil {
			return schema, err
		}
		field := models.NewField(name, models.Custom)
		field.Null = !notNull
		field.PrimaryKey = pk
		field.DBDefault = def
		switch {
		case kind == "smallint":
			field.Kind = models.SmallInteger
		case kind == "integer":
			field.Kind = models.Integer
		case kind == "bigint":
			field.Kind = models.BigInteger
		case kind == "text":
			field.Kind = models.Text
		case strings.HasPrefix(kind, "character varying"):
			field.Kind = models.Char
			_, _ = fmt.Sscanf(kind, "character varying(%d)", &field.MaxLength)
		case strings.HasPrefix(kind, "numeric"):
			field.Kind = models.Decimal
			_, _ = fmt.Sscanf(kind, "numeric(%d,%d)", &field.MaxDigits, &field.DecimalPlaces)
		case kind == "boolean":
			field.Kind = models.Boolean
		case kind == "uuid":
			field.Kind = models.UUID
		case kind == "date":
			field.Kind = models.Date
		case strings.HasPrefix(kind, "timestamp"):
			field.Kind = models.DateTime
		case strings.HasPrefix(kind, "time "):
			field.Kind = models.Time
		case kind == "jsonb":
			field.Kind = models.JSON
		case kind == "bytea":
			field.Kind = models.Binary
		case kind == "double precision":
			field.Kind = models.Float
		case kind == "interval":
			field.Kind = models.Duration
		}
		if identity != "" {
			switch field.Kind {
			case models.SmallInteger:
				field.Kind = models.SmallAuto
			case models.Integer:
				field.Kind = models.Auto
			case models.BigInteger:
				field.Kind = models.BigAuto
			}
			field.Editable = false
		}
		schema.Fields = append(schema.Fields, field)
		if pk {
			schema.PrimaryKey = append(schema.PrimaryKey, name)
		}
	}
	if err := rows.Err(); err != nil {
		return schema, err
	}
	if len(schema.Fields) == 0 {
		return schema, db.ErrNoRows
	}
	return schema, nil
}

var _ = strconv.IntSize
