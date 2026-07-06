package migrations

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Newton-School/gogo/models"
)

// SchemaDifference describes one mismatch between expected and actual table shape.
type SchemaDifference struct {
	Kind     string
	Table    string
	Column   string
	Object   string
	Expected string
	Actual   string
}

func (d SchemaDifference) String() string {
	target := d.Table
	if d.Column != "" {
		target += "." + d.Column
	} else if d.Object != "" {
		target += "." + d.Object
	}
	if d.Expected != "" || d.Actual != "" {
		return fmt.Sprintf("%s mismatch %s expected %s actual %s", d.Kind, target, d.Expected, d.Actual)
	}
	return fmt.Sprintf("%s %s", d.Kind, target)
}

// CompareTableSchema compares an expected initial table shape with inspected columns.
func CompareTableSchema(expected TableSchema, actualColumns []ColumnSchema) []SchemaDifference {
	return CompareTableShape(expected, TableSchema{Name: expected.Name, Columns: actualColumns})
}

// CompareTableShape compares an expected table shape with inspected schema.
func CompareTableShape(expected, actual TableSchema) []SchemaDifference {
	actualByName := make(map[string]ColumnSchema, len(actual.Columns))
	for _, column := range actual.Columns {
		actualByName[strings.ToLower(column.Name)] = column
	}
	var diffs []SchemaDifference
	for _, column := range expected.Columns {
		actual, ok := actualByName[strings.ToLower(column.Name)]
		if !ok {
			diffs = append(diffs, SchemaDifference{Kind: "MISSING column", Table: expected.Name, Column: column.Name})
			continue
		}
		if column.PrimaryKey && !actual.PrimaryKey {
			diffs = append(diffs, SchemaDifference{Kind: "PRIMARY KEY", Table: expected.Name, Column: column.Name, Expected: "primary key", Actual: "not primary key"})
		}
		if !column.Nullable && actual.Nullable && !column.PrimaryKey {
			diffs = append(diffs, SchemaDifference{Kind: "NULL", Table: expected.Name, Column: column.Name, Expected: "not null", Actual: "nullable"})
		}
		expectedKind := comparableColumnKind(column)
		actualKind := comparableColumnKind(actual)
		if expectedKind != "" && actualKind != "" && expectedKind != actualKind {
			diffs = append(diffs, SchemaDifference{Kind: "TYPE", Table: expected.Name, Column: column.Name, Expected: expectedKind, Actual: actualKind})
		}
		if (column.Default != nil || actual.Default != nil || strings.TrimSpace(actual.DefaultSQL) != "") && !databaseDefaultMatches(column.Default, actual.Default, actual.DefaultSQL) {
			diffs = append(diffs, SchemaDifference{Kind: "DEFAULT", Table: expected.Name, Column: column.Name, Expected: normalizeDefaultSQL(renderDatabaseDefault(column.Default)), Actual: normalizeDefaultSQL(actual.DefaultSQL)})
		}
		if column.Collation != "" && column.Collation != actual.Collation {
			diffs = append(diffs, SchemaDifference{Kind: "COLLATION", Table: expected.Name, Column: column.Name, Expected: column.Collation, Actual: actual.Collation})
		}
	}
	diffs = append(diffs, compareIndexSchemas(expected.Name, expected.Indexes, actual.Indexes)...)
	diffs = append(diffs, compareConstraintSchemas(expected.Name, expected.Constraints, actual.Constraints)...)
	return diffs
}

func compareIndexSchemas(table string, expected, actual []IndexSchema) []SchemaDifference {
	actualByName := make(map[string]IndexSchema, len(actual))
	for _, index := range actual {
		if index.Name != "" {
			actualByName[strings.ToLower(index.Name)] = index
		}
	}
	var diffs []SchemaDifference
	for _, index := range expected {
		if index.Name == "" {
			continue
		}
		actualIndex, ok := actualByName[strings.ToLower(index.Name)]
		if !ok {
			diffs = append(diffs, SchemaDifference{Kind: "MISSING index", Table: table, Object: index.Name})
			continue
		}
		if !indexMatchesExpected(index, actualIndex) {
			diffs = append(diffs, SchemaDifference{Kind: "INDEX", Table: table, Object: index.Name, Expected: indexSchemaSignature(index), Actual: indexSchemaSignature(actualIndex)})
		}
	}
	return diffs
}

func compareConstraintSchemas(table string, expected, actual []ConstraintSchema) []SchemaDifference {
	actualByName := make(map[string]ConstraintSchema, len(actual))
	for _, constraint := range actual {
		if constraint.Name != "" {
			actualByName[strings.ToLower(constraint.Name)] = constraint
		}
	}
	var diffs []SchemaDifference
	for _, constraint := range expected {
		if constraint.Name == "" {
			continue
		}
		actualConstraint, ok := actualByName[strings.ToLower(constraint.Name)]
		if !ok {
			diffs = append(diffs, SchemaDifference{Kind: "MISSING constraint", Table: table, Object: constraint.Name})
			continue
		}
		if !constraintMatchesExpected(constraint, actualConstraint) {
			kind := "CONSTRAINT"
			if normalizeConstraintType(constraint.Type) == "foreign_key" {
				kind = "FOREIGN KEY"
			}
			diffs = append(diffs, SchemaDifference{Kind: kind, Table: table, Object: constraint.Name, Expected: constraintSchemaSignature(constraint), Actual: constraintSchemaSignature(actualConstraint)})
		}
	}
	return diffs
}

func indexMatchesExpected(expected, actual IndexSchema) bool {
	if expected.Unique && !actual.Unique {
		return false
	}
	if expected.Primary && !actual.Primary {
		return false
	}
	if expected.Method != "" && !sameIdentifier(expected.Method, actual.Method) {
		return false
	}
	if len(expected.Fields) > 0 && !sameIdentifierSlice(expected.Fields, actual.Fields) {
		return false
	}
	if len(expected.Expressions) > 0 && !sameSQLSlice(expected.Expressions, actual.Expressions) {
		return false
	}
	if len(expected.OpClasses) > 0 && !sameIdentifierSlice(expected.OpClasses, actual.OpClasses) {
		return false
	}
	if len(expected.Include) > 0 && !sameIdentifierSlice(expected.Include, actual.Include) {
		return false
	}
	if expected.ConditionSQL != "" && normalizeSQL(expected.ConditionSQL) != normalizeSQL(actual.ConditionSQL) {
		return false
	}
	return true
}

func constraintMatchesExpected(expected, actual ConstraintSchema) bool {
	expectedType := normalizeConstraintType(expected.Type)
	if expectedType != "" && expectedType != normalizeConstraintType(actual.Type) {
		return false
	}
	if len(expected.Fields) > 0 && !sameIdentifierSlice(expected.Fields, actual.Fields) {
		return false
	}
	if len(expected.Expressions) > 0 && !sameSQLSlice(expected.Expressions, actual.Expressions) {
		return false
	}
	if expected.Check != "" && normalizeSQL(expected.Check) != normalizeSQL(actual.Check) {
		return false
	}
	if expected.ConditionSQL != "" && normalizeSQL(expected.ConditionSQL) != normalizeSQL(actual.ConditionSQL) {
		return false
	}
	if len(expected.Include) > 0 && !sameIdentifierSlice(expected.Include, actual.Include) {
		return false
	}
	if len(expected.OpClasses) > 0 && !sameIdentifierSlice(expected.OpClasses, actual.OpClasses) {
		return false
	}
	if expected.ReferencesTable != "" && !sameIdentifier(expected.ReferencesTable, actual.ReferencesTable) {
		return false
	}
	if len(expected.ReferencesColumns) > 0 && !sameIdentifierSlice(expected.ReferencesColumns, actual.ReferencesColumns) {
		return false
	}
	if expected.OnDelete != "" && !sameConstraintAction(expected.OnDelete, actual.OnDelete) {
		return false
	}
	if expected.Deferrable && !actual.Deferrable {
		return false
	}
	if expected.InitiallyDeferred && !actual.InitiallyDeferred {
		return false
	}
	return true
}

func indexSchemaSignature(index IndexSchema) string {
	parts := []string{}
	if index.Unique {
		parts = append(parts, "unique")
	}
	if index.Primary {
		parts = append(parts, "primary")
	}
	if index.Method != "" {
		parts = append(parts, "method="+normalizeIdentifier(index.Method))
	}
	terms := append(normalizeIdentifierSlice(index.Fields), normalizeSQLSlice(index.Expressions)...)
	if len(terms) > 0 {
		parts = append(parts, "terms="+strings.Join(terms, ","))
	}
	if len(index.OpClasses) > 0 {
		parts = append(parts, "opclasses="+strings.Join(normalizeIdentifierSlice(index.OpClasses), ","))
	}
	if len(index.Include) > 0 {
		parts = append(parts, "include="+strings.Join(normalizeIdentifierSlice(index.Include), ","))
	}
	if index.ConditionSQL != "" {
		parts = append(parts, "where="+normalizeSQL(index.ConditionSQL))
	}
	if len(parts) == 0 {
		return "index"
	}
	return strings.Join(parts, " ")
}

func constraintSchemaSignature(constraint ConstraintSchema) string {
	parts := []string{}
	if constraint.Type != "" {
		parts = append(parts, "type="+normalizeConstraintType(constraint.Type))
	}
	terms := append(normalizeIdentifierSlice(constraint.Fields), normalizeSQLSlice(constraint.Expressions)...)
	if len(terms) > 0 {
		parts = append(parts, "terms="+strings.Join(terms, ","))
	}
	if constraint.Check != "" {
		parts = append(parts, "check="+normalizeSQL(constraint.Check))
	}
	if constraint.ConditionSQL != "" {
		parts = append(parts, "where="+normalizeSQL(constraint.ConditionSQL))
	}
	if constraint.ReferencesTable != "" {
		parts = append(parts, "references="+normalizeIdentifier(constraint.ReferencesTable)+"("+strings.Join(normalizeIdentifierSlice(constraint.ReferencesColumns), ",")+")")
	}
	if constraint.OnDelete != "" {
		parts = append(parts, "on_delete="+normalizeConstraintAction(constraint.OnDelete))
	}
	if constraint.Deferrable {
		parts = append(parts, "deferrable")
	}
	if constraint.InitiallyDeferred {
		parts = append(parts, "initially_deferred")
	}
	if len(parts) == 0 {
		return "constraint"
	}
	return strings.Join(parts, " ")
}

func comparableColumnKind(column ColumnSchema) string {
	if column.NormalizedKind != "" {
		return strings.ToLower(strings.TrimSpace(column.NormalizedKind))
	}
	return NormalizeColumnKind(column.Kind)
}

// NormalizeColumnKind lowercases and trims a database type for shape comparison.
func NormalizeColumnKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	kind = strings.Join(strings.Fields(kind), " ")
	return kind
}

func sameIdentifier(left, right string) bool {
	return normalizeIdentifier(left) == normalizeIdentifier(right)
}

func sameIdentifierSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !sameIdentifier(left[i], right[i]) {
			return false
		}
	}
	return true
}

func sameSQLSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if normalizeSQL(left[i]) != normalizeSQL(right[i]) {
			return false
		}
	}
	return true
}

func sameConstraintAction(left, right string) bool {
	return normalizeConstraintAction(left) == normalizeConstraintAction(right)
}

func normalizeIdentifier(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'[]`)
	return strings.ToLower(value)
}

func normalizeIdentifierSlice(values []string) []string {
	normalized := make([]string, len(values))
	for i, value := range values {
		normalized[i] = normalizeIdentifier(value)
	}
	return normalized
}

func normalizeSQLSlice(values []string) []string {
	normalized := make([]string, len(values))
	for i, value := range values {
		normalized[i] = normalizeSQL(value)
	}
	return normalized
}

func normalizeSQL(value string) string {
	value = strings.TrimSpace(value)
	for strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")") {
		value = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "("), ")"))
	}
	value = strings.Join(strings.Fields(value), " ")
	return strings.ToLower(value)
}

func normalizeConstraintType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	switch value {
	case "f", "foreignkey":
		return "foreign_key"
	case "u":
		return "unique"
	case "c":
		return "check"
	case "x":
		return "exclusion"
	case "p":
		return "primary_key"
	default:
		return value
	}
}

func normalizeConstraintAction(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", " ")
	return strings.Join(strings.Fields(value), " ")
}

func databaseDefaultMatches(expected, actual *models.DatabaseDefault, actualSQL string) bool {
	expectedSQL := normalizeDefaultSQL(renderDatabaseDefault(expected))
	if actual != nil {
		return expectedSQL == normalizeDefaultSQL(renderDatabaseDefault(actual))
	}
	return expectedSQL == normalizeDefaultSQL(actualSQL)
}

func renderDatabaseDefault(defaultValue *models.DatabaseDefault) string {
	if defaultValue == nil {
		return ""
	}
	normalized, err := models.NormalizeDatabaseDefault(defaultValue)
	if err != nil {
		return ""
	}
	switch normalized.Kind {
	case models.DefaultExpression:
		return normalized.SQL
	case models.DefaultLiteral:
		return renderLiteralDefault(normalized.Value)
	default:
		return ""
	}
}

func renderLiteralDefault(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return "'" + strings.ReplaceAll(typed, "'", "''") + "'"
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.FormatInt(int64(typed), 10)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func normalizeDefaultSQL(sql string) string {
	sql = strings.TrimSpace(sql)
	for strings.HasPrefix(sql, "(") && strings.HasSuffix(sql, ")") {
		sql = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(sql, "("), ")"))
	}
	if index := strings.Index(sql, "::"); index > 0 {
		sql = sql[:index]
	}
	return strings.ToLower(strings.TrimSpace(sql))
}
