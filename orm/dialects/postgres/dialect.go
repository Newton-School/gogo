package postgres

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/cybersaksham/gogo/orm/dialects"
)

// Dialect renders PostgreSQL SQL syntax.
type Dialect struct{}

// New returns a PostgreSQL dialect.
func New() Dialect {
	return Dialect{}
}

func (Dialect) Name() string {
	return "postgres"
}

func (Dialect) Placeholder(position int) string {
	return "$" + strconv.Itoa(position)
}

func (Dialect) QuoteIdent(identifier string) string {
	return dialects.QuoteQualifiedIdent(identifier)
}

func (Dialect) ColumnType(kind string) (string, bool) {
	columnTypes := map[string]string{
		"auto":       "bigserial",
		"integer":    "integer",
		"bigint":     "bigint",
		"decimal":    "numeric",
		"float":      "double precision",
		"boolean":    "boolean",
		"char":       "varchar",
		"text":       "text",
		"date":       "date",
		"datetime":   "timestamp with time zone",
		"time":       "time",
		"duration":   "interval",
		"binary":     "bytea",
		"json":       "jsonb",
		"uuid":       "uuid",
		"ip_address": "inet",
	}
	value, ok := columnTypes[kind]
	return value, ok
}

func (Dialect) SupportsReturning() bool {
	return true
}

func (Dialect) SupportsUpsert() bool {
	return true
}

func (Dialect) JSONLookup(column string, path []string) (string, error) {
	if err := dialects.ValidateJSONPath(path); err != nil {
		return "", err
	}
	return column + " #> '{" + strings.Join(path, ",") + "}'", nil
}

func (Dialect) DateExtract(part, expression string) (string, error) {
	normalized, err := dialects.ValidateDatePart(part)
	if err != nil {
		return "", err
	}
	if normalized == "weekday" {
		normalized = "dow"
	}
	return "EXTRACT(" + strings.ToUpper(normalized) + " FROM " + expression + ")", nil
}

func (d Dialect) LockClause(options dialects.LockOptions) (string, error) {
	if !options.ForUpdate {
		return "", nil
	}
	if options.NoWait && options.SkipLocked {
		return "", fmt.Errorf("%w: NOWAIT and SKIP LOCKED cannot be combined", dialects.ErrInvalidInput)
	}
	parts := []string{"FOR UPDATE"}
	if len(options.Of) > 0 {
		quoted := make([]string, len(options.Of))
		for i, identifier := range options.Of {
			quoted[i] = d.QuoteIdent(identifier)
		}
		parts = append(parts, "OF "+strings.Join(quoted, ", "))
	}
	if options.NoWait {
		parts = append(parts, "NOWAIT")
	}
	if options.SkipLocked {
		parts = append(parts, "SKIP LOCKED")
	}
	return strings.Join(parts, " "), nil
}

func (Dialect) LimitOffset(options dialects.LimitOffset) string {
	return dialects.RenderLimitOffset(options)
}

func (d Dialect) SavepointSQL(name string) string {
	return "SAVEPOINT " + d.QuoteIdent(name)
}

func (d Dialect) RollbackToSavepointSQL(name string) string {
	return "ROLLBACK TO SAVEPOINT " + d.QuoteIdent(name)
}

func (d Dialect) ReleaseSavepointSQL(name string) string {
	return "RELEASE SAVEPOINT " + d.QuoteIdent(name)
}

func (Dialect) SchemaIntrospection() dialects.SchemaIntrospection {
	return dialects.SchemaIntrospection{
		TablesSQL: "SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema')",
		ColumnsSQL: `SELECT
	n.nspname AS table_schema,
	cls.relname AS table_name,
	a.attname AS column_name,
	format_type(a.atttypid, a.atttypmod) AS formatted_type,
	t.typname AS udt_name,
	pg_get_expr(ad.adbin, ad.adrelid) AS column_default,
	coll.collname AS collation_name,
	a.attidentity <> '' AS identity,
	NOT a.attnotnull AS nullable,
	COALESCE(pk.primary_key, false) AS primary_key,
	a.attnum AS ordinal_position
FROM pg_catalog.pg_attribute a
JOIN pg_catalog.pg_class cls ON cls.oid = a.attrelid
JOIN pg_catalog.pg_namespace n ON n.oid = cls.relnamespace
JOIN pg_catalog.pg_type t ON t.oid = a.atttypid
LEFT JOIN pg_catalog.pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
LEFT JOIN pg_catalog.pg_collation coll ON coll.oid = a.attcollation AND a.attcollation <> t.typcollation
LEFT JOIN (
	SELECT conrelid, unnest(conkey) AS attnum, true AS primary_key
	FROM pg_catalog.pg_constraint
	WHERE contype = 'p'
) pk ON pk.conrelid = a.attrelid AND pk.attnum = a.attnum
WHERE a.attnum > 0
	AND NOT a.attisdropped
	AND cls.relkind IN ('r', 'p')
	AND n.nspname NOT IN ('pg_catalog', 'information_schema')
ORDER BY cls.relname, a.attnum`,
		ConstraintsSQL: `SELECT
	n.nspname AS table_schema,
	tbl.relname AS table_name,
	con.conname AS constraint_name,
	CASE con.contype
		WHEN 'u' THEN 'unique'
		WHEN 'c' THEN 'check'
		WHEN 'f' THEN 'foreign_key'
		WHEN 'x' THEN 'exclusion'
		WHEN 'p' THEN 'primary_key'
		ELSE con.contype::text
	END AS constraint_type,
	COALESCE(array_to_string(ARRAY(
		SELECT att.attname
		FROM unnest(con.conkey) WITH ORDINALITY AS key(attnum, ord)
		JOIN pg_catalog.pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = key.attnum
		ORDER BY key.ord
	), E'\x1f'), '') AS column_names,
	pg_get_constraintdef(con.oid, true) AS definition_sql,
	CASE WHEN con.contype = 'c' THEN pg_get_constraintdef(con.oid, true) END AS check_sql,
	NULL::text AS condition_sql,
	ref.relname AS referenced_table,
	COALESCE(array_to_string(ARRAY(
		SELECT att.attname
		FROM unnest(con.confkey) WITH ORDINALITY AS key(attnum, ord)
		JOIN pg_catalog.pg_attribute att ON att.attrelid = con.confrelid AND att.attnum = key.attnum
		ORDER BY key.ord
	), E'\x1f'), '') AS referenced_columns,
	CASE con.confdeltype
		WHEN 'a' THEN 'NO ACTION'
		WHEN 'r' THEN 'RESTRICT'
		WHEN 'c' THEN 'CASCADE'
		WHEN 'n' THEN 'SET NULL'
		WHEN 'd' THEN 'SET DEFAULT'
		ELSE NULL
	END AS on_delete,
	con.condeferrable AS deferrable,
	con.condeferred AS initially_deferred
FROM pg_catalog.pg_constraint con
JOIN pg_catalog.pg_class tbl ON tbl.oid = con.conrelid
JOIN pg_catalog.pg_namespace n ON n.oid = tbl.relnamespace
LEFT JOIN pg_catalog.pg_class ref ON ref.oid = con.confrelid
WHERE con.contype IN ('u', 'c', 'f', 'x', 'p')
	AND n.nspname NOT IN ('pg_catalog', 'information_schema')
ORDER BY tbl.relname, con.conname`,
		IndexesSQL: `SELECT
	n.nspname AS table_schema,
	tbl.relname AS table_name,
	idx.relname AS index_name,
	am.amname AS method,
	ix.indisunique AS is_unique,
	ix.indisprimary AS is_primary,
	pg_get_expr(ix.indpred, ix.indrelid) AS condition_sql,
	pg_get_indexdef(ix.indexrelid) AS definition_sql,
	COALESCE(array_to_string(ARRAY(
		SELECT att.attname
		FROM unnest(ix.indkey) WITH ORDINALITY AS key(attnum, ord)
		JOIN pg_catalog.pg_attribute att ON att.attrelid = tbl.oid AND att.attnum = key.attnum
		WHERE key.attnum > 0 AND key.ord <= ix.indnkeyatts
		ORDER BY key.ord
	), E'\x1f'), '') AS column_names,
	COALESCE(array_to_string(ARRAY(
		SELECT pg_get_indexdef(ix.indexrelid, key.ord::int, true)
		FROM unnest(ix.indkey) WITH ORDINALITY AS key(attnum, ord)
		WHERE key.attnum = 0 AND key.ord <= ix.indnkeyatts
		ORDER BY key.ord
	), E'\x1f'), '') AS expression_sql,
	COALESCE(array_to_string(ARRAY(
		SELECT opc.opcname
		FROM unnest(ix.indclass) WITH ORDINALITY AS cls(opcoid, ord)
		JOIN pg_catalog.pg_opclass opc ON opc.oid = cls.opcoid
		WHERE cls.ord <= ix.indnkeyatts
		ORDER BY cls.ord
	), E'\x1f'), '') AS opclass_names,
	COALESCE(array_to_string(ARRAY(
		SELECT att.attname
		FROM unnest(ix.indkey) WITH ORDINALITY AS key(attnum, ord)
		JOIN pg_catalog.pg_attribute att ON att.attrelid = tbl.oid AND att.attnum = key.attnum
		WHERE key.attnum > 0 AND key.ord > ix.indnkeyatts
		ORDER BY key.ord
	), E'\x1f'), '') AS include_columns
FROM pg_catalog.pg_index ix
JOIN pg_catalog.pg_class idx ON idx.oid = ix.indexrelid
JOIN pg_catalog.pg_class tbl ON tbl.oid = ix.indrelid
JOIN pg_catalog.pg_namespace n ON n.oid = tbl.relnamespace
JOIN pg_catalog.pg_am am ON am.oid = idx.relam
WHERE tbl.relkind IN ('r', 'p')
	AND n.nspname NOT IN ('pg_catalog', 'information_schema')
ORDER BY tbl.relname, idx.relname`,
	}
}
