package sqlite

import (
	"fmt"
	"strings"

	"github.com/Newton-School/gogo/orm/dialects"
)

// Dialect renders SQLite SQL syntax.
type Dialect struct{}

// New returns a SQLite dialect.
func New() Dialect {
	return Dialect{}
}

func (Dialect) Name() string {
	return "sqlite"
}

func (Dialect) Placeholder(int) string {
	return "?"
}

func (Dialect) QuoteIdent(identifier string) string {
	return dialects.QuoteQualifiedIdent(identifier)
}

func (Dialect) ColumnType(kind string) (string, bool) {
	columnTypes := map[string]string{
		"auto":       "integer",
		"integer":    "integer",
		"bigint":     "integer",
		"decimal":    "numeric",
		"float":      "real",
		"boolean":    "boolean",
		"char":       "varchar",
		"text":       "text",
		"date":       "date",
		"datetime":   "datetime",
		"time":       "time",
		"duration":   "bigint",
		"binary":     "blob",
		"json":       "text",
		"uuid":       "text",
		"ip_address": "text",
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
	return "json_extract(" + column + ", '$." + strings.Join(path, ".") + "')", nil
}

func (Dialect) DateExtract(part, expression string) (string, error) {
	normalized, err := dialects.ValidateDatePart(part)
	if err != nil {
		return "", err
	}
	formats := map[string]string{
		"year":    "%Y",
		"month":   "%m",
		"week":    "%W",
		"day":     "%d",
		"weekday": "%w",
		"hour":    "%H",
		"minute":  "%M",
		"second":  "%S",
	}
	format, ok := formats[normalized]
	if !ok {
		return "", fmt.Errorf("%w: SQLite cannot extract %q", dialects.ErrUnsupportedFeature, part)
	}
	return "CAST(strftime('" + format + "', " + expression + ") AS INTEGER)", nil
}

func (Dialect) LockClause(options dialects.LockOptions) (string, error) {
	if options.ForUpdate {
		return "", fmt.Errorf("%w: SQLite does not support SELECT FOR UPDATE", dialects.ErrUnsupportedFeature)
	}
	return "", nil
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
		TablesSQL:      "SELECT name FROM sqlite_master WHERE type = 'table'",
		ColumnsSQL:     "PRAGMA table_info",
		ConstraintsSQL: "PRAGMA foreign_key_list",
		IndexesSQL:     "PRAGMA index_list",
	}
}
