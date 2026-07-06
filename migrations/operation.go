package migrations

import (
	"context"
	"strings"

	"github.com/Newton-School/gogo/models"
)

// SchemaEditor is the database-facing contract used by operations.
type SchemaEditor interface {
	Execute(context.Context, string, ...any) error
}

// SchemaRenderer optionally centralizes dialect-specific schema SQL rendering.
type SchemaRenderer interface {
	CreateTable(table string, fields []FieldState) string
	DropTable(table string) string
	RenameTable(oldName, newName string) string
	AddColumn(table string, field FieldState) string
	DropColumn(table, column string) string
	AlterColumnType(table, column, kind string) string
	AlterNull(table, column string, nullable bool) string
	AlterDefault(table, column string, value any) string
	AlterColumnCollation(table, column, kind, collation string) string
	RenameColumn(table, oldName, newName string) string
	AddIndex(table string, index IndexState) string
	DropIndex(name string) string
	RenameIndex(oldName, newName string) string
	AddConstraint(table string, constraint ConstraintState) string
	DropConstraint(table, name string) string
}

// TableExistenceChecker is implemented by schema editors that can inspect existing tables.
type TableExistenceChecker interface {
	TableExists(context.Context, string) (bool, error)
}

// TableShapeChecker is implemented by schema editors that can inspect columns.
type TableShapeChecker interface {
	TableColumns(context.Context, string) ([]ColumnSchema, error)
}

// TableObjectShapeChecker is implemented by schema editors that can inspect
// table indexes and constraints in addition to columns.
type TableObjectShapeChecker interface {
	TableIndexes(context.Context, string) ([]IndexSchema, error)
	TableConstraints(context.Context, string) ([]ConstraintSchema, error)
}

// InitialTableProvider lets fake-initial compare an initial migration with existing schema.
type InitialTableProvider interface {
	InitialTables() []string
}

// InitialSchemaProvider lets fake-initial validate existing table shape.
type InitialSchemaProvider interface {
	InitialSchema() []TableSchema
}

// TableSchema describes the minimum table shape required by an initial migration.
type TableSchema struct {
	Name        string
	Columns     []ColumnSchema
	Indexes     []IndexSchema
	Constraints []ConstraintSchema
}

// ColumnSchema describes one column required by an initial migration.
type ColumnSchema struct {
	Schema             string
	Name               string
	Kind               string
	NormalizedKind     string
	UDTName            string
	CharacterMaxLength int
	NumericPrecision   int
	NumericScale       int
	Default            *models.DatabaseDefault
	DefaultSQL         string
	Collation          string
	Identity           bool
	PrimaryKey         bool
	Nullable           bool
	OrdinalPosition    int
}

// IndexSchema describes one inspected or expected index.
type IndexSchema struct {
	Schema        string
	Table         string
	Name          string
	Fields        []string
	Expressions   []string
	Method        string
	OpClasses     []string
	Include       []string
	ConditionSQL  string
	Unique        bool
	Primary       bool
	DefinitionSQL string
}

// ConstraintSchema describes one inspected or expected table constraint.
type ConstraintSchema struct {
	Schema            string
	Table             string
	Name              string
	Type              string
	Fields            []string
	Expressions       []string
	Check             string
	ConditionSQL      string
	Include           []string
	OpClasses         []string
	ReferencesTable   string
	ReferencesColumns []string
	OnDelete          string
	Deferrable        bool
	InitiallyDeferred bool
	DefinitionSQL     string
}

// IndexSchemaFromState converts migration index state into comparable schema.
func IndexSchemaFromState(index IndexState) IndexSchema {
	return IndexSchema{
		Name:         index.Name,
		Fields:       append([]string(nil), index.Fields...),
		Unique:       index.Unique,
		Expressions:  append([]string(nil), index.Expressions...),
		Method:       index.Method,
		OpClasses:    append([]string(nil), index.OpClasses...),
		Include:      append([]string(nil), index.Include...),
		ConditionSQL: index.ConditionSQL,
	}
}

// ConstraintSchemaFromState converts migration constraint state into comparable schema.
func ConstraintSchemaFromState(constraint ConstraintState) ConstraintSchema {
	return ConstraintSchema{
		Name:              constraint.Name,
		Type:              constraint.Type,
		Fields:            append([]string(nil), constraint.Fields...),
		Expressions:       append([]string(nil), constraint.Expressions...),
		Check:             constraint.Check,
		ConditionSQL:      constraint.ConditionSQL,
		Include:           append([]string(nil), constraint.Include...),
		OpClasses:         append([]string(nil), constraint.OpClasses...),
		ReferencesTable:   constraint.ReferencesTable,
		ReferencesColumns: append([]string(nil), constraint.ReferencesColumns...),
		OnDelete:          constraint.OnDelete,
		Deferrable:        constraint.Deferrable,
		InitiallyDeferred: constraint.InitiallyDeferred,
	}
}

// Operation is the complete migration operation contract.
type Operation interface {
	Name() string
	StateForwards(*ProjectState) error
	DatabaseForwards(context.Context, SchemaEditor) error
	DatabaseBackwards(context.Context, SchemaEditor) error
	Describe() string
	Reversible() bool
	ReferencesModel(appLabel, modelName string) bool
	ReferencesField(appLabel, modelName, fieldName string) bool
}

// InitialTableNameFromSQL returns the table created by a simple CREATE TABLE statement.
func InitialTableNameFromSQL(statement string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(statement))
	if len(fields) < 3 || !strings.EqualFold(fields[0], "CREATE") || !strings.EqualFold(fields[1], "TABLE") {
		return "", false
	}
	index := 2
	if len(fields) > 5 && strings.EqualFold(fields[index], "IF") && strings.EqualFold(fields[index+1], "NOT") && strings.EqualFold(fields[index+2], "EXISTS") {
		index += 3
	}
	if index >= len(fields) {
		return "", false
	}
	name := fields[index]
	if paren := strings.Index(name, "("); paren >= 0 {
		name = name[:paren]
	}
	name = strings.Trim(name, "`\"[]")
	if name == "" {
		return "", false
	}
	return name, true
}
