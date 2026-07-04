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
		if err := writeInspectedTable(stdout, table, schema.Columns); err != nil {
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

func writeInspectedTable(stdout io.Writer, table string, columns []migrations.ColumnSchema) error {
	modelName := modelNameFromTable(table)
	managedVar := strings.ToLower(modelName[:1]) + modelName[1:] + "Managed"
	if _, err := fmt.Fprintf(stdout, "%s := false\nmodels.Metadata{AppLabel: \"legacy\", ModelName: %q, TableName: %q, DBTable: %q, Managed: &%s}\n", managedVar, modelName, table, table, managedVar); err != nil {
		return err
	}
	for _, column := range columns {
		parts := []string{column.Name, column.Kind}
		if column.PrimaryKey {
			parts = append(parts, "primary_key")
		}
		if column.Nullable {
			parts = append(parts, "null")
		}
		if _, err := fmt.Fprintf(stdout, "  field %s\n", strings.Join(parts, " ")); err != nil {
			return err
		}
	}
	return nil
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
			Expressions:  append([]string(nil), index.Expressions...),
			Method:       index.Method,
			OpClasses:    append([]string(nil), index.OpClasses...),
			Include:      append([]string(nil), index.Include...),
			ConditionSQL: index.Condition,
		})
	}
	constraints := make([]migrations.ConstraintSchema, 0, len(meta.Constraints))
	for _, constraint := range meta.Constraints {
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
