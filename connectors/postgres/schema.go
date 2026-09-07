package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type SchemaEditor struct {
	Dialect  Dialect
	schemas  map[string]models.Schema
	deferred map[string]models.Schema
	dropped  map[string]bool
}

func (e SchemaEditor) WithSchemas(schemas []models.Schema) (db.SchemaEditor, error) {
	e.schemas = map[string]models.Schema{}
	e.deferred = map[string]models.Schema{}
	e.dropped = map[string]bool{}
	for _, schema := range schemas {
		if err := schema.Validate(); err != nil {
			return nil, err
		}
		e.schemas[schema.Key()] = schema.Clone()
	}
	for _, source := range schemas {
		for _, field := range source.Fields {
			if field.Kind != models.ManyToMany || field.Relation == nil || field.Relation.Through != "" {
				continue
			}
			target, ok := e.schemas[field.Relation.Target]
			if !ok {
				return nil, errors.New("postgres: historical many-to-many target schema missing")
			}
			through, err := models.ImplicitThrough(source, field, target)
			if err != nil {
				return nil, err
			}
			if existing, ok := e.schemas[through.Key()]; ok && (existing.AutoCreatedBy != source.Key() || existing.AutoCreatedField != field.Name) {
				return nil, errors.New("postgres: intermediary schema collision")
			}
			e.schemas[through.Key()] = through
		}
	}
	return e, nil
}

// FlushDeferred creates automatic intermediary tables after all ordinary tables
// in this migration, including targets declared after their referring model.
func (e SchemaEditor) FlushDeferred(ctx context.Context, executor db.Executor) error {
	keys := make([]string, 0, len(e.deferred))
	for key := range e.deferred {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := e.CreateModel(ctx, executor, e.deferred[key]); err != nil {
			return err
		}
		delete(e.deferred, key)
	}
	return nil
}
func (e SchemaEditor) queueThrough(source models.Schema, field models.Field) error {
	if field.Relation == nil {
		return errors.New("postgres: many-to-many metadata missing")
	}
	if field.Relation.Through != "" {
		return nil
	}
	if e.deferred == nil {
		return errors.New("postgres: automatic intermediary requires WithSchemas and FlushDeferred")
	}
	target, ok := e.schemas[field.Relation.Target]
	if !ok {
		return errors.New("postgres: historical many-to-many target schema missing")
	}
	through, err := models.ImplicitThrough(source, field, target)
	if err != nil {
		return err
	}
	e.deferred[through.Key()] = through
	return nil
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
	if field.Collation != "" || field.Tablespace != "" {
		return "", &db.Error{Code: db.UnsupportedFeature, Message: "Field collation and tablespace require explicit supported schema operations"}
	}
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if schema.Unmanaged || schema.Proxy || schema.Abstract {
		return nil
	}
	if err := schema.Validate(); err != nil {
		return err
	}
	if schema.Tablespace != "" {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Model tablespace requires explicit supported schema operations"}
	}
	indexes, err := e.fieldIndexes(schema)
	if err != nil {
		return err
	}
	for _, field := range schema.Fields {
		if field.Kind == models.ManyToMany {
			if err := e.queueThrough(schema, field); err != nil {
				return err
			}
		}
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
	for _, index := range indexes {
		if _, err := executor.Exec(ctx, index.createSQL()); err != nil {
			return err
		}
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
	if schema.Unmanaged || schema.Proxy || schema.Abstract {
		return nil
	}
	keys := []string{}
	for key, through := range e.schemas {
		if through.AutoCreatedBy == "" {
			continue
		}
		for _, field := range through.Fields {
			if field.Relation != nil && field.Relation.Target == schema.Key() {
				keys = append(keys, key)
				break
			}
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := e.dropThrough(ctx, executor, e.schemas[key]); err != nil {
			return err
		}
	}
	table, err := e.Dialect.QuoteIdentifier(schema.DBTable())
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, "DROP TABLE "+table)
	return err
}
func (e SchemaEditor) dropThrough(ctx context.Context, executor db.Executor, through models.Schema) error {
	if e.dropped[through.Key()] {
		return nil
	}
	if _, pending := e.deferred[through.Key()]; pending {
		delete(e.deferred, through.Key())
		return nil
	}
	table, err := e.Dialect.QuoteIdentifier(through.DBTable())
	if err != nil {
		return err
	}
	if _, err := executor.Exec(ctx, "DROP TABLE "+table); err != nil {
		return err
	}
	if e.dropped != nil {
		e.dropped[through.Key()] = true
	}
	return nil
}
func (e SchemaEditor) AddField(ctx context.Context, executor db.Executor, schema models.Schema, field models.Field) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	prospective := replaceSchemaField(schema, field.Name, field)
	if _, err := e.fieldIndexes(prospective); err != nil {
		return err
	}
	if field.Kind == models.ManyToMany {
		return e.queueThrough(schema, field)
	}
	table, err := e.Dialect.QuoteIdentifier(schema.DBTable())
	if err != nil {
		return err
	}
	column, err := e.column(field)
	if err != nil {
		return err
	}
	if _, err = executor.Exec(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column); err != nil {
		return err
	}
	index, err := implicitFieldIndex(prospective, field)
	if err != nil || index == nil {
		return err
	}
	_, err = executor.Exec(ctx, index.createSQL())
	return err
}
func (e SchemaEditor) RemoveField(ctx context.Context, executor db.Executor, schema models.Schema, field models.Field) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if field.Kind == models.ManyToMany {
		if field.Relation == nil {
			return errors.New("postgres: many-to-many metadata missing")
		}
		if field.Relation.Through != "" {
			return nil
		}
		target, ok := e.schemas[field.Relation.Target]
		if !ok {
			return errors.New("postgres: historical many-to-many target schema missing")
		}
		through, err := models.ImplicitThrough(schema, field, target)
		if err != nil {
			return err
		}
		return e.dropThrough(ctx, executor, through)
	}
	for _, index := range schema.Indexes {
		if index.Condition != "" || slices.Contains(index.Fields, field.Name) || slices.Contains(index.Include, field.Name) {
			return &db.Error{Code: db.UnsupportedFeature, Message: "Remove dependent named indexes before removing a field"}
		}
	}
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
	field, ok := schema.Field(old)
	if !ok || !models.ValidIdentifier(new) {
		return errors.New("postgres: rename requires an existing logical field and valid new name")
	}
	if _, exists := schema.Field(new); exists {
		return errors.New("postgres: renamed field already exists")
	}
	next := field
	next.Name = new
	return e.AlterField(ctx, executor, schema, field, next)
}
func (e SchemaEditor) AlterField(ctx context.Context, executor db.Executor, schema models.Schema, old, new models.Field) error {
	return e.alterIndexedField(ctx, executor, schema, old, new)
}

func (e SchemaEditor) resolvedType(field models.Field) (string, error) {
	if field.Kind == models.ForeignKey || field.Kind == models.OneToOne {
		_, target, err := e.relationTarget(field)
		if err != nil {
			return "", err
		}
		field = target
		switch field.Kind {
		case models.SmallAuto:
			field.Kind = models.SmallInteger
		case models.Auto:
			field.Kind = models.Integer
		case models.BigAuto:
			field.Kind = models.BigInteger
		}
	}
	return e.Dialect.FieldType(field)
}

func (e SchemaEditor) alterIndexedField(ctx context.Context, executor db.Executor, schema models.Schema, old, new models.Field) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	prospective := replaceSchemaField(schema, old.Name, new)
	for i, key := range prospective.PrimaryKey {
		if key == old.Name {
			prospective.PrimaryKey[i] = new.Name
		}
	}
	if _, err := e.fieldIndexes(prospective); err != nil {
		return err
	}
	if !old.IsStored() || !new.IsStored() || old.PrimaryKey != new.PrimaryKey || old.Unique != new.Unique || old.GeneratedExpression != new.GeneratedExpression || old.Collation != "" || new.Collation != "" || old.Tablespace != "" || new.Tablespace != "" {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Constraint, generated, collation or tablespace alterations require explicit supported schema operations"}
	}
	if !sameRelationStorage(old.Relation, new.Relation) || old.Kind != new.Kind && (positiveKind(old.Kind) || positiveKind(new.Kind) || old.Kind == models.OneToOne || new.Kind == models.OneToOne) {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Relation or positive-value constraint alteration requires an explicit migration"}
	}
	oldIndex, err := implicitFieldIndex(schema, old)
	if err != nil {
		return err
	}
	newIndex, err := implicitFieldIndex(prospective, new)
	if err != nil {
		return err
	}
	table, err := e.Dialect.QuoteIdentifier(schema.DBTable())
	if err != nil {
		return err
	}
	column, err := e.Dialect.QuoteIdentifier(old.DBColumn())
	if err != nil {
		return err
	}
	newColumn, err := e.Dialect.QuoteIdentifier(new.DBColumn())
	if err != nil {
		return err
	}
	// A relation's unchanged endpoint keeps its storage type. Logical rename
	// metadata may name the historical endpoint while the editor registry already
	// contains its new name; do not resolve a type that this operation never alters.
	kind, oldKind := "", ""
	if old.Kind != new.Kind || old.Kind != models.ForeignKey && old.Kind != models.OneToOne {
		kind, err = e.resolvedType(new)
		if err != nil {
			return err
		}
		oldKind, err = e.resolvedType(old)
		if err != nil {
			return err
		}
	}
	if (old.IsAuto() || new.IsAuto()) && oldKind != kind {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Identity alteration requires explicit migration"}
	}
	clauses := []string{}
	if oldKind != kind {
		clauses = append(clauses, "ALTER COLUMN "+column+" TYPE "+kind)
	}
	if old.Null != new.Null {
		nullOp := " SET NOT NULL"
		if new.Null {
			nullOp = " DROP NOT NULL"
		}
		clauses = append(clauses, "ALTER COLUMN "+column+nullOp)
	}
	if old.DBDefault != new.DBDefault {
		if new.DBDefault == "" {
			clauses = append(clauses, "ALTER COLUMN "+column+" DROP DEFAULT")
		} else {
			if strings.ContainsAny(new.DBDefault, ";\x00") || strings.Contains(new.DBDefault, "$gogo_field_index$") {
				return errors.New("postgres: invalid default expression")
			}
			clauses = append(clauses, "ALTER COLUMN "+column+" SET DEFAULT "+new.DBDefault)
		}
	}
	actions := []string{}
	var namedRefresh []string
	if column != newColumn {
		// Trusted SQL fragments may refer to a column only in their predicate,
		// not in an index's declared key/include fields. Do not guess dependencies
		// or let PostgreSQL rewrite storage while historical SQL stays unchanged.
		for _, index := range schema.Indexes {
			if index.Condition != "" {
				return &db.Error{Code: db.UnsupportedFeature, Message: "Field-column rename with conditional indexes requires an explicit migration"}
			}
		}
		for _, constraint := range schema.Constraints {
			if constraint.Condition != "" || constraint.Expression != "" {
				return &db.Error{Code: db.UnsupportedFeature, Message: "Field-column rename with SQL constraints requires an explicit migration"}
			}
		}
		for _, index := range schema.Indexes {
			dependent := false
			next := (models.Schema{Indexes: []models.Index{index}}).Clone().Indexes[0]
			for _, names := range [][]string{next.Fields, next.Include} {
				for i, name := range names {
					if name == old.Name {
						dependent, names[i] = true, new.Name
					}
				}
			}
			if !dependent {
				continue
			}
			if index.Concurrent || index.Condition != "" {
				return &db.Error{Code: db.UnsupportedFeature, Message: "Field-column rename with online or conditional indexes requires an explicit migration"}
			}
			before, err := e.namedIndex(schema, index)
			if err != nil {
				return err
			}
			after, err := e.namedIndex(prospective, next)
			if err != nil {
				return err
			}
			actions = append(actions, before.lockStatement(), before.guardStatement())
			namedRefresh = append(namedRefresh, after.commentStatement())
		}
	}
	if oldIndex != nil && newIndex == nil {
		actions = append(actions, oldIndex.dropStatement())
	}
	if len(clauses) > 0 {
		actions = append(actions, "ALTER TABLE "+table+" "+strings.Join(clauses, ", "))
	}
	if column != newColumn {
		actions = append(actions, "ALTER TABLE "+table+" RENAME COLUMN "+column+" TO "+newColumn)
	}
	actions = append(actions, namedRefresh...)
	if newIndex != nil {
		if oldIndex == nil {
			actions = append(actions, newIndex.createStatements())
		} else if oldIndex.name != newIndex.name {
			actions = append(actions, oldIndex.renameStatements(*newIndex))
		}
	}
	if len(actions) == 0 {
		return nil
	}
	statement := "DO $gogo_field_index$ BEGIN " + strings.Join(actions, "; ") + "; END $gogo_field_index$"
	if oldIndex != nil {
		statement = oldIndex.guardedSQL(strings.Join(actions, "; "))
	}
	_, err = executor.Exec(ctx, statement)
	return err
}
func (e SchemaEditor) addConcurrentIndex(ctx context.Context, executor db.Executor, schema models.Schema, index models.Index) error {
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
	_, err := e.Dialect.QuoteIdentifier(name)
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, "DO $gogo_named_index$ BEGIN EXECUTE format('DROP INDEX %I.%I', current_schema(), "+quoteIndexLiteral(name)+"); END $gogo_named_index$")
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
