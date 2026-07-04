package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/cybersaksham/gogo/migrations"
	"github.com/cybersaksham/gogo/models"
	"github.com/cybersaksham/gogo/orm"
)

func NewInspectDBCommand() Command {
	return inspectDBCommand{}
}

func NewDiffSchemaCommand(projectModels []models.Metadata) Command {
	return diffSchemaCommand{projectModels: append([]models.Metadata(nil), projectModels...)}
}

type inspectDBCommand struct{}

func (c inspectDBCommand) Name() string    { return "inspectdb" }
func (c inspectDBCommand) Summary() string { return "Inspect existing database schema" }
func (c inspectDBCommand) Run(ctx context.Context, args []string) error {
	return c.runWithIO(ctx, args, io.Discard, io.Discard)
}
func (c inspectDBCommand) runWithIO(ctx context.Context, args []string, stdout, _ io.Writer) error {
	options, err := parseSchemaFlags("inspectdb", args)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	database, err := openMigrationDatabase(ctx, options.database)
	if err != nil {
		return fmt.Errorf("%w: open database: %v", ErrCommandFailed, err)
	}
	defer database.Close()
	tables, err := schemaTables(ctx, database)
	if err != nil {
		return fmt.Errorf("%w: inspect tables: %v", ErrCommandFailed, err)
	}
	editor := sqlSchemaEditor{db: database.SQLDB(), dialect: database.Dialect}
	for _, table := range tables {
		if options.table != "" && table != options.table {
			continue
		}
		schema, err := inspectTableSchema(ctx, editor, table)
		if err != nil {
			return fmt.Errorf("%w: inspect schema for %s: %v", ErrCommandFailed, table, err)
		}
		if err := writeInspectedTable(stdout, table, schema, database.Dialect.Name()); err != nil {
			return err
		}
	}
	return nil
}

type diffSchemaCommand struct {
	projectModels []models.Metadata
}

func (c diffSchemaCommand) Name() string    { return "diffschema" }
func (c diffSchemaCommand) Summary() string { return "Compare database schema with model metadata" }
func (c diffSchemaCommand) Run(ctx context.Context, args []string) error {
	return c.runWithIO(ctx, args, io.Discard, io.Discard)
}
func (c diffSchemaCommand) runWithIO(ctx context.Context, args []string, stdout, _ io.Writer) error {
	options, err := parseSchemaFlags("diffschema", args)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	if len(c.projectModels) == 0 {
		return fmt.Errorf("%w: no project model metadata registered", ErrCommandFailed)
	}
	database, err := openMigrationDatabase(ctx, options.database)
	if err != nil {
		return fmt.Errorf("%w: open database: %v", ErrCommandFailed, err)
	}
	defer database.Close()
	editor := sqlSchemaEditor{db: database.SQLDB(), dialect: database.Dialect}
	var diffs []string
	for _, meta := range c.projectModels {
		if options.app != "" && meta.AppLabel != options.app {
			continue
		}
		diffs = append(diffs, compareModelToSchema(ctx, editor, meta)...)
	}
	if len(diffs) == 0 {
		_, err := fmt.Fprintln(stdout, "schema matches model metadata")
		return err
	}
	for _, diff := range diffs {
		if _, err := fmt.Fprintln(stdout, diff); err != nil {
			return err
		}
	}
	return ErrCommandFailed
}

type schemaOptions struct {
	database string
	app      string
	table    string
}

func parseSchemaFlags(command string, args []string) (schemaOptions, error) {
	options := schemaOptions{database: orm.DefaultDatabase}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.database, "database", orm.DefaultDatabase, "database alias")
	flags.StringVar(&options.app, "app", "", "app label")
	flags.StringVar(&options.table, "table", "", "table name")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	return options, nil
}

func schemaTables(ctx context.Context, database *orm.Database) ([]string, error) {
	if database == nil || database.Dialect == nil || database.Dialect.SchemaIntrospection().TablesSQL == "" {
		return nil, fmt.Errorf("database dialect does not support table introspection")
	}
	rows, err := database.SQLDB().QueryContext(ctx, database.Dialect.SchemaIntrospection().TablesSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		if strings.HasPrefix(table, "sqlite_") {
			continue
		}
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables, rows.Err()
}

func writeInspectedTable(stdout io.Writer, table string, tableSchema migrations.TableSchema, dialect string) error {
	modelName := modelNameFromTable(table)
	managedVar := strings.ToLower(modelName[:1]) + modelName[1:] + "Managed"
	if _, err := fmt.Fprintf(stdout, "var %s = false\n\nvar %sMetadata = models.Metadata{\n", managedVar, modelName); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "\tAppLabel: \"legacy\",\n\tModelName: %q,\n\tTableName: %q,\n\tDBTable: %q,\n\tManaged: &%s,\n", modelName, table, table, managedVar); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, "\tFields: []models.FieldMeta{"); err != nil {
		return err
	}
	for _, column := range tableSchema.Columns {
		if _, err := fmt.Fprintf(stdout, "\t\t%s,\n", inspectedFieldMeta(column, dialect)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(stdout, "\t},"); err != nil {
		return err
	}
	if indexes := inspectedModelIndexes(tableSchema); len(indexes) > 0 {
		if _, err := fmt.Fprintln(stdout, "\tIndexes: []models.Index{"); err != nil {
			return err
		}
		for _, index := range indexes {
			if _, err := fmt.Fprintf(stdout, "\t\t%s,\n", inspectedIndexMeta(index)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(stdout, "\t},"); err != nil {
			return err
		}
	}
	if constraints := inspectedModelConstraints(tableSchema); len(constraints) > 0 {
		if _, err := fmt.Fprintln(stdout, "\tConstraints: []models.Constraint{"); err != nil {
			return err
		}
		for _, constraint := range constraints {
			if _, err := fmt.Fprintf(stdout, "\t\t%s,\n", inspectedConstraintMeta(constraint)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(stdout, "\t},"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(stdout, "}"); err != nil {
		return err
	}
	for _, constraint := range tableSchema.Constraints {
		if isModelConstraint(constraint) {
			continue
		}
		definition := constraint.DefinitionSQL
		if definition == "" {
			definition = constraintSchemaComment(constraint)
		}
		if definition != "" {
			if _, err := fmt.Fprintf(stdout, "// %s %s: %s\n", constraint.Type, constraint.Name, definition); err != nil {
				return err
			}
		}
	}
	return nil
}

func inspectedFieldMeta(column migrations.ColumnSchema, dialect string) string {
	kind := column.NormalizedKind
	if kind == "" {
		kind = migrations.NormalizeColumnKind(column.Kind)
	}
	parts := []string{
		fmt.Sprintf("Name: %q", column.Name),
		fmt.Sprintf("Column: %q", column.Name),
		fmt.Sprintf("Kind: %q", kind),
	}
	if dialect != "" && column.Kind != "" {
		parts = append(parts, fmt.Sprintf("ColumnTypes: map[string]string{%q: %q}", dialect, column.Kind))
	}
	if column.PrimaryKey {
		parts = append(parts, "PrimaryKey: true")
	}
	if column.Nullable {
		parts = append(parts, "Null: true")
	}
	if column.DefaultSQL != "" {
		parts = append(parts, fmt.Sprintf("DBDefault: models.DefaultSQL(%q)", column.DefaultSQL))
	} else if column.Identity {
		parts = append(parts, `DBDefault: models.DefaultSQL("GENERATED AS IDENTITY")`)
	}
	if column.Collation != "" {
		parts = append(parts, fmt.Sprintf("DBCollation: %q", column.Collation))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func inspectedModelIndexes(tableSchema migrations.TableSchema) []migrations.IndexSchema {
	constraintNames := make(map[string]struct{}, len(tableSchema.Constraints))
	for _, constraint := range tableSchema.Constraints {
		constraintNames[strings.ToLower(constraint.Name)] = struct{}{}
	}
	indexes := make([]migrations.IndexSchema, 0, len(tableSchema.Indexes))
	for _, index := range tableSchema.Indexes {
		if index.Primary {
			continue
		}
		if _, exists := constraintNames[strings.ToLower(index.Name)]; exists {
			continue
		}
		if len(index.Fields) == 0 && len(index.Expressions) == 0 {
			continue
		}
		indexes = append(indexes, index)
	}
	return indexes
}

func inspectedModelConstraints(tableSchema migrations.TableSchema) []migrations.ConstraintSchema {
	constraints := make([]migrations.ConstraintSchema, 0, len(tableSchema.Constraints))
	for _, constraint := range tableSchema.Constraints {
		if isModelConstraint(constraint) {
			constraints = append(constraints, constraint)
		}
	}
	return constraints
}

func isModelConstraint(constraint migrations.ConstraintSchema) bool {
	switch strings.ToLower(constraint.Type) {
	case "unique":
		return len(constraint.Fields) > 0 || len(constraint.Expressions) > 0
	case "check":
		return constraint.Check != ""
	default:
		return false
	}
}

func inspectedIndexMeta(index migrations.IndexSchema) string {
	parts := []string{fmt.Sprintf("Name: %q", index.Name)}
	if len(index.Fields) > 0 {
		parts = append(parts, "Fields: "+indexFieldList(index.Fields))
	}
	if len(index.Expressions) > 0 {
		parts = append(parts, "Expressions: "+stringList(index.Expressions))
	}
	if index.Method != "" {
		parts = append(parts, fmt.Sprintf("Method: %q", index.Method))
	}
	if len(index.OpClasses) > 0 {
		parts = append(parts, "OpClasses: "+stringList(index.OpClasses))
	}
	if len(index.Include) > 0 {
		parts = append(parts, "Include: "+stringList(index.Include))
	}
	if index.ConditionSQL != "" {
		parts = append(parts, fmt.Sprintf("Condition: %q", index.ConditionSQL))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func inspectedConstraintMeta(constraint migrations.ConstraintSchema) string {
	parts := []string{fmt.Sprintf("Name: %q", constraint.Name)}
	switch strings.ToLower(constraint.Type) {
	case "check":
		parts = append(parts, "Type: models.ConstraintCheck", fmt.Sprintf("Check: %q", constraint.Check))
	default:
		parts = append(parts, "Type: models.ConstraintUnique")
		if len(constraint.Fields) > 0 {
			parts = append(parts, "Fields: "+indexFieldList(constraint.Fields))
		}
		if len(constraint.Expressions) > 0 {
			parts = append(parts, "Expressions: "+stringList(constraint.Expressions))
		}
	}
	if constraint.ConditionSQL != "" {
		parts = append(parts, fmt.Sprintf("Condition: %q", constraint.ConditionSQL))
	}
	if len(constraint.Include) > 0 {
		parts = append(parts, "Include: "+stringList(constraint.Include))
	}
	if len(constraint.OpClasses) > 0 {
		parts = append(parts, "OpClasses: "+stringList(constraint.OpClasses))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func indexFieldList(fields []string) string {
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, fmt.Sprintf("models.Asc(%q)", field))
	}
	return "[]models.IndexField{" + strings.Join(parts, ", ") + "}"
}

func stringList(values []string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%q", value))
	}
	return "[]string{" + strings.Join(parts, ", ") + "}"
}

func constraintSchemaComment(constraint migrations.ConstraintSchema) string {
	if constraint.DefinitionSQL != "" {
		return constraint.DefinitionSQL
	}
	if constraint.ReferencesTable != "" {
		return fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)", strings.Join(constraint.Fields, ", "), constraint.ReferencesTable, strings.Join(constraint.ReferencesColumns, ", "))
	}
	return ""
}

func compareModelToSchema(ctx context.Context, editor sqlSchemaEditor, meta models.Metadata) []string {
	table := meta.TableName
	if table == "" {
		table = meta.DBTable
	}
	if table == "" {
		table = strings.ToLower(meta.AppLabel + "_" + meta.ModelName)
	}
	actual, err := inspectTableSchema(ctx, editor, table)
	if err != nil {
		return []string{fmt.Sprintf("ERROR %s.%s inspect table %s: %v", meta.AppLabel, meta.ModelName, table, err)}
	}
	if len(actual.Columns) == 0 {
		return []string{fmt.Sprintf("MISSING table %s for %s.%s", table, meta.AppLabel, meta.ModelName)}
	}
	expected := tableSchemaFromMetadata(meta, table)
	differences := migrations.CompareTableShape(expected, actual)
	diffs := make([]string, 0, len(differences))
	for _, difference := range differences {
		diffs = append(diffs, difference.String()+fmt.Sprintf(" for %s.%s", meta.AppLabel, meta.ModelName))
	}
	return diffs
}

func inspectTableSchema(ctx context.Context, editor sqlSchemaEditor, table string) (migrations.TableSchema, error) {
	columns, err := editor.TableColumns(ctx, table)
	if err != nil {
		return migrations.TableSchema{}, err
	}
	indexes, err := editor.TableIndexes(ctx, table)
	if err != nil {
		return migrations.TableSchema{}, err
	}
	constraints, err := editor.TableConstraints(ctx, table)
	if err != nil {
		return migrations.TableSchema{}, err
	}
	return migrations.TableSchema{Name: table, Columns: columns, Indexes: indexes, Constraints: constraints}, nil
}

func tableSchemaFromMetadata(meta models.Metadata, table string) migrations.TableSchema {
	columns := make([]migrations.ColumnSchema, 0, len(meta.Fields))
	for _, field := range meta.Fields {
		column := field.Column
		if column == "" {
			column = field.Name
		}
		defaultValue, _ := models.NormalizeDatabaseDefault(field.DBDefault)
		columnSchema := migrations.ColumnSchema{
			Name:           column,
			Kind:           field.Kind,
			NormalizedKind: migrations.NormalizeColumnKind(field.Kind),
			PrimaryKey:     field.PrimaryKey,
			Nullable:       field.Null,
			Collation:      field.DBCollation,
		}
		if defaultValue.Kind != models.DefaultNone {
			columnSchema.Default = &defaultValue
		}
		columns = append(columns, columnSchema)
	}
	indexes := make([]migrations.IndexSchema, 0, len(meta.Indexes))
	for _, index := range meta.Indexes {
		indexes = append(indexes, migrations.IndexSchema{
			Name:         index.NameFor(table),
			Fields:       index.FieldNames(),
			Unique:       index.Unique,
			Expressions:  append([]string(nil), index.Expressions...),
			Method:       index.Method,
			OpClasses:    append([]string(nil), index.OpClasses...),
			Include:      append([]string(nil), index.Include...),
			ConditionSQL: index.Condition,
		})
	}
	constraints := make([]migrations.ConstraintSchema, 0, len(meta.Constraints))
	for _, constraint := range meta.Constraints {
		if constraint.RequiresIndex() {
			indexes = append(indexes, migrations.IndexSchema{
				Name:         constraint.NameFor(table),
				Fields:       constraint.FieldNames(),
				Unique:       true,
				Expressions:  append([]string(nil), constraint.Expressions...),
				OpClasses:    append([]string(nil), constraint.OpClasses...),
				Include:      append([]string(nil), constraint.Include...),
				ConditionSQL: constraint.Condition,
			})
			continue
		}
		constraints = append(constraints, migrations.ConstraintSchema{
			Name:         constraint.NameFor(table),
			Type:         string(constraint.Type),
			Fields:       constraint.FieldNames(),
			Expressions:  append([]string(nil), constraint.Expressions...),
			Check:        constraint.Check,
			ConditionSQL: constraint.Condition,
			Include:      append([]string(nil), constraint.Include...),
			OpClasses:    append([]string(nil), constraint.OpClasses...),
		})
	}
	return migrations.TableSchema{Name: table, Columns: columns, Indexes: indexes, Constraints: constraints}
}

func modelNameFromTable(table string) string {
	var builder strings.Builder
	upperNext := true
	for _, r := range table {
		if r == '_' || r == '-' || r == ' ' {
			upperNext = true
			continue
		}
		if upperNext && r >= 'a' && r <= 'z' {
			builder.WriteRune(r - ('a' - 'A'))
		} else {
			builder.WriteRune(r)
		}
		upperNext = false
	}
	if builder.Len() == 0 {
		return "InspectedModel"
	}
	return builder.String()
}
