package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func inspectColumns(ctx context.Context, executor db.Executor, schema, table string, budget *catalogBudget) ([]db.CatalogColumn, error) {
	rows, err := executor.Query(ctx, `SELECT a.attname, pg_catalog.format_type(a.atttypid,a.atttypmod), tn.nspname, t.typname, a.attnotnull, a.attidentity::pg_catalog.text, a.attgenerated::pg_catalog.text,
COALESCE(pg_catalog.left(pg_catalog.pg_get_expr(d.adbin,d.adrelid,false),65537),''), CASE WHEN co.oid IS NULL THEN '' ELSE pg_catalog.format('%I.%I',cn.nspname,co.collname) END,
EXISTS(SELECT 1 FROM pg_catalog.pg_index i WHERE i.indrelid OPERATOR(pg_catalog.=) a.attrelid AND i.indisprimary AND a.attnum OPERATOR(pg_catalog.=) ANY(i.indkey)),
COALESCE(pg_catalog.pg_get_serial_sequence(pg_catalog.format('%I.%I',n.nspname,c.relname),a.attname),''), COALESCE(etn.nspname,''), COALESCE(et.typname,''), a.attndims
FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid OPERATOR(pg_catalog.=) a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid OPERATOR(pg_catalog.=) c.relnamespace
JOIN pg_catalog.pg_type t ON t.oid OPERATOR(pg_catalog.=) a.atttypid JOIN pg_catalog.pg_namespace tn ON tn.oid OPERATOR(pg_catalog.=) t.typnamespace
LEFT JOIN pg_catalog.pg_type et ON et.oid OPERATOR(pg_catalog.=) t.typelem LEFT JOIN pg_catalog.pg_namespace etn ON etn.oid OPERATOR(pg_catalog.=) et.typnamespace
LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid OPERATOR(pg_catalog.=) a.attrelid AND d.adnum OPERATOR(pg_catalog.=) a.attnum
LEFT JOIN pg_catalog.pg_collation co ON co.oid OPERATOR(pg_catalog.=) a.attcollation LEFT JOIN pg_catalog.pg_namespace cn ON cn.oid OPERATOR(pg_catalog.=) co.collnamespace
WHERE n.nspname OPERATOR(pg_catalog.=) $1 AND c.relname OPERATOR(pg_catalog.=) $2 AND a.attnum OPERATOR(pg_catalog.>) 0 AND NOT a.attisdropped ORDER BY a.attnum LIMIT 1601`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []db.CatalogColumn{}
	for rows.Next() {
		var column db.CatalogColumn
		var notNull, primary bool
		var expression, sequence, elementSchema, elementType string
		var dimensions int
		if err := rows.Scan(&column.Name, &column.DatabaseType, &column.TypeSchema, &column.TypeName, &notNull, &column.Identity, &column.Generated, &expression, &column.Collation, &primary, &sequence, &elementSchema, &elementType, &dimensions); err != nil {
			return nil, err
		}
		if len(result) >= 1600 {
			return nil, errors.New("postgres: inspect at most 1600 columns per relation")
		}
		if err := budget.add(catalogTextLimit, column.Name, column.DatabaseType, column.TypeSchema, column.TypeName, column.Identity, column.Generated, column.Collation, expression, sequence, elementSchema, elementType); err != nil {
			return nil, err
		}
		field := catalogField(column.Name, column.TypeSchema, column.TypeName, column.DatabaseType)
		if strings.HasPrefix(column.TypeName, "_") && elementType != "" && dimensions <= 1 {
			element := catalogField("element", elementSchema, elementType, strings.TrimSuffix(column.DatabaseType, "[]"))
			if element.Kind != models.Custom {
				field.Kind, field.Element = models.Array, &element
			}
		}
		field.Null, field.PrimaryKey, field.Column, field.Collation = !notNull, primary, column.Name, column.Collation
		if column.Generated != "" {
			element := field
			element.Name, element.Column, element.PrimaryKey, element.Null = "element", "", false, false
			field.Kind, field.Element = models.Generated, &element
			field.GeneratedExpression, field.Editable = expression, false
		} else {
			field.DBDefault = expression
		}
		if column.Identity != "" || sequence != "" {
			switch field.Kind {
			case models.SmallInteger:
				field.Kind = models.SmallAuto
			case models.Integer:
				field.Kind = models.Auto
			case models.BigInteger:
				field.Kind = models.BigAuto
			}
			field.Editable = false
			if column.Identity == "" {
				column.Identity = "serial"
			} else if column.Identity == "a" {
				column.Identity = "always"
			} else if column.Identity == "d" {
				column.Identity = "by_default"
			}
		}
		column.Field = field
		result = append(result, column)
	}
	return result, errors.Join(rows.Err(), rows.Close())
}

func catalogField(name, namespace, native, formatted string) models.Field {
	field := models.NewField(name, models.Custom)
	if namespace != "pg_catalog" {
		return field
	}
	switch native {
	case "int2":
		field.Kind = models.SmallInteger
	case "int4":
		field.Kind = models.Integer
	case "int8":
		field.Kind = models.BigInteger
	case "text":
		field.Kind = models.Text
	case "varchar", "bpchar":
		field.Kind = models.Char
		if offset := strings.IndexByte(formatted, '('); offset >= 0 {
			_, _ = fmt.Sscanf(formatted[offset:], "(%d)", &field.MaxLength)
		}
	case "numeric":
		field.Kind = models.Decimal
		_, _ = fmt.Sscanf(formatted, "numeric(%d,%d)", &field.MaxDigits, &field.DecimalPlaces)
	case "bool":
		field.Kind = models.Boolean
	case "uuid":
		field.Kind = models.UUID
	case "date":
		field.Kind = models.Date
	case "timestamp", "timestamptz":
		field.Kind = models.DateTime
	case "time":
		field.Kind = models.Time
	case "json", "jsonb":
		field.Kind = models.JSON
	case "bytea":
		field.Kind = models.Binary
	case "float4", "float8":
		field.Kind = models.Float
	case "interval":
		field.Kind = models.Duration
	case "inet":
		field.Kind = models.GenericIPAddress
	}
	return field
}
