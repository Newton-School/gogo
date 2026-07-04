package schema

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/cybersaksham/gogo/migrations"
	"github.com/cybersaksham/gogo/models"
	"github.com/cybersaksham/gogo/orm/dialects"
)

// Editor renders schema SQL for one dialect.
type Editor struct {
	Dialect dialects.Dialect
}

// NewEditor creates a schema editor.
func NewEditor(dialect dialects.Dialect) Editor {
	return Editor{Dialect: dialect}
}

func (e Editor) CreateTable(name string, fields []migrations.FieldState) string {
	columns := make([]string, len(fields))
	for i, field := range fields {
		columns[i] = e.columnDefinition(field)
	}
	return "CREATE TABLE " + e.q(name) + " (" + strings.Join(columns, ", ") + ")"
}

func (e Editor) DropTable(name string) string {
	return "DROP TABLE " + e.q(name)
}

func (e Editor) RenameTable(oldName, newName string) string {
	return "ALTER TABLE " + e.q(oldName) + " RENAME TO " + e.q(newName)
}

func (e Editor) AddColumn(table string, field migrations.FieldState) string {
	return "ALTER TABLE " + e.q(table) + " ADD COLUMN " + e.columnDefinition(field)
}

func (e Editor) DropColumn(table, column string) string {
	return "ALTER TABLE " + e.q(table) + " DROP COLUMN " + e.q(column)
}

func (e Editor) AlterColumnType(table, column, kind string) string {
	return "ALTER TABLE " + e.q(table) + " ALTER COLUMN " + e.q(column) + " TYPE " + kind
}

func (e Editor) AlterNull(table, column string, nullable bool) string {
	action := "SET NOT NULL"
	if nullable {
		action = "DROP NOT NULL"
	}
	return "ALTER TABLE " + e.q(table) + " ALTER COLUMN " + e.q(column) + " " + action
}

func (e Editor) AlterDefault(table, column string, value any) string {
	if value == nil {
		return "ALTER TABLE " + e.q(table) + " ALTER COLUMN " + e.q(column) + " DROP DEFAULT"
	}
	defaultSQL, err := databaseDefaultSQL(value)
	if err != nil || defaultSQL == "" {
		return "ALTER TABLE " + e.q(table) + " ALTER COLUMN " + e.q(column) + " DROP DEFAULT"
	}
	return "ALTER TABLE " + e.q(table) + " ALTER COLUMN " + e.q(column) + " SET DEFAULT " + defaultSQL
}

func (e Editor) AlterColumnCollation(table, column, kind, collation string) string {
	statement := "ALTER TABLE " + e.q(table) + " ALTER COLUMN " + e.q(column) + " TYPE " + kind
	if collation != "" {
		statement += " COLLATE " + e.q(collation)
	}
	return statement
}

func (e Editor) RenameColumn(table, oldName, newName string) string {
	return "ALTER TABLE " + e.q(table) + " RENAME COLUMN " + e.q(oldName) + " TO " + e.q(newName)
}

func (e Editor) AddIndex(table string, index migrations.IndexState) string {
	terms := e.indexTerms(index)
	parts := []string{"CREATE INDEX"}
	if index.Unique {
		parts = []string{"CREATE UNIQUE INDEX"}
	}
	if index.Concurrently {
		parts = append(parts, "CONCURRENTLY")
	}
	parts = append(parts, e.q(index.Name), "ON", e.q(table))
	if index.Method != "" {
		parts = append(parts, "USING", index.Method)
	}
	statement := strings.Join(parts, " ") + " (" + strings.Join(terms, ", ") + ")"
	if len(index.Include) > 0 {
		statement += " INCLUDE (" + strings.Join(quoteList(e, index.Include), ", ") + ")"
	}
	if index.ConditionSQL != "" {
		statement += " WHERE " + index.ConditionSQL
	}
	return statement
}

func (e Editor) DropIndex(name string) string {
	return "DROP INDEX " + e.q(name)
}

func (e Editor) RenameIndex(oldName, newName string) string {
	return "ALTER INDEX " + e.q(oldName) + " RENAME TO " + e.q(newName)
}

func (e Editor) AddConstraint(table string, constraint migrations.ConstraintState) string {
	if e.Dialect.Name() == "sqlite" {
		return "-- SQLite rebuild required to add constraint " + e.q(constraint.Name) + " on " + e.q(table)
	}
	return "ALTER TABLE " + e.q(table) + " ADD CONSTRAINT " + e.q(constraint.Name) + " " + e.constraintSQL(constraint)
}

func (e Editor) DropConstraint(table, name string) string {
	if e.Dialect.Name() == "sqlite" {
		return "-- SQLite rebuild required to drop constraint " + e.q(name) + " on " + e.q(table)
	}
	return "ALTER TABLE " + e.q(table) + " DROP CONSTRAINT " + e.q(name)
}

func (e Editor) CreateManyToManyTable(table, fromColumn, toColumn string) string {
	return "CREATE TABLE " + e.q(table) + " (" + e.q(fromColumn) + " bigint NOT NULL, " + e.q(toColumn) + " bigint NOT NULL)"
}

func (e Editor) DropManyToManyTable(table string) string {
	return "DROP TABLE " + e.q(table)
}

func (e Editor) columnDefinition(field migrations.FieldState) string {
	parts := []string{e.q(columnName(field)), fieldKind(field)}
	if field.DBDefault != nil {
		if defaultSQL, err := databaseDefaultSQL(field.DBDefault); err == nil && defaultSQL != "" {
			parts = append(parts, "DEFAULT", defaultSQL)
		}
	}
	if field.PrimaryKey {
		parts = append(parts, "PRIMARY KEY")
	}
	if !field.Null || field.PrimaryKey {
		parts = append(parts, "NOT NULL")
	}
	return strings.Join(parts, " ")
}

func (e Editor) constraintSQL(constraint migrations.ConstraintState) string {
	fields := strings.Join(quoteList(e, constraint.Fields), ", ")
	switch constraint.Type {
	case "check":
		return "CHECK (" + constraint.Check + ")"
	case "exclusion":
		return "EXCLUDE (" + fields + ")"
	case "foreign_key":
		references := constraint.ReferencesTable
		columns := constraint.ReferencesColumns
		if len(columns) == 0 {
			columns = []string{"id"}
		}
		sql := "FOREIGN KEY (" + fields + ") REFERENCES " + e.q(references) + " (" + strings.Join(quoteList(e, columns), ", ") + ")"
		if constraint.OnDelete != "" {
			sql += " ON DELETE " + constraint.OnDelete
		}
		if constraint.Deferrable {
			sql += " DEFERRABLE"
			if constraint.InitiallyDeferred {
				sql += " INITIALLY DEFERRED"
			}
		}
		return sql
	default:
		return "UNIQUE (" + fields + ")"
	}
}

func (e Editor) indexTerms(index migrations.IndexState) []string {
	terms := make([]string, 0, len(index.Fields)+len(index.Expressions))
	for _, field := range index.Fields {
		terms = append(terms, e.q(field))
	}
	terms = append(terms, index.Expressions...)
	for i := range terms {
		if i < len(index.OpClasses) && index.OpClasses[i] != "" {
			terms[i] += " " + index.OpClasses[i]
		}
	}
	return terms
}

func (e Editor) q(identifier string) string {
	return e.Dialect.QuoteIdent(identifier)
}

func quoteList(e Editor, values []string) []string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = e.q(value)
	}
	return quoted
}

func columnName(field migrations.FieldState) string {
	if field.Column != "" {
		return field.Column
	}
	return field.Name
}

func fieldKind(field migrations.FieldState) string {
	if field.Kind != "" {
		return field.Kind
	}
	return "text"
}

func databaseDefaultSQL(value any) (string, error) {
	defaultValue, err := models.NormalizeDatabaseDefault(value)
	if err != nil {
		return "", err
	}
	switch defaultValue.Kind {
	case models.DefaultNone:
		return "", nil
	case models.DefaultExpression:
		return defaultValue.SQL, nil
	case models.DefaultLiteral:
		return literalDefaultSQL(defaultValue.Value)
	default:
		return "", fmt.Errorf("%w: unknown database default kind %q", models.ErrInvalidMetadata, defaultValue.Kind)
	}
}

func literalDefaultSQL(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "NULL", nil
	case string:
		return "'" + strings.ReplaceAll(typed, "'", "''") + "'", nil
	case bool:
		if typed {
			return "true", nil
		}
		return "false", nil
	case int:
		return strconv.FormatInt(int64(typed), 10), nil
	case int8:
		return strconv.FormatInt(int64(typed), 10), nil
	case int16:
		return strconv.FormatInt(int64(typed), 10), nil
	case int32:
		return strconv.FormatInt(int64(typed), 10), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case uint:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint64:
		return strconv.FormatUint(typed, 10), nil
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32), nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("%w: unsupported database default literal %T", models.ErrInvalidMetadata, value)
	}
}
