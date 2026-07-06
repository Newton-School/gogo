package operations

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/models"
)

type AddField struct {
	AppLabel, ModelName string
	TableName           string
	Field               migrations.FieldState
	HasDefault          bool
	UnsafeAcknowledged  bool
}

type RemoveField struct {
	AppLabel, ModelName string
	TableName           string
	Field               migrations.FieldState
}

type AlterField struct {
	AppLabel, ModelName string
	TableName           string
	OldField            migrations.FieldState
	NewField            migrations.FieldState
	UnsafeAcknowledged  bool
}

type RenameField struct {
	AppLabel, ModelName string
	TableName           string
	OldName, NewName    string
}

func (o AddField) Name() string { return "AddField" }
func (o AddField) ValidateSafety() error {
	if !o.Field.Null && !o.HasDefault && !o.UnsafeAcknowledged {
		return fmt.Errorf("%w: adding non-null field %s requires a default or acknowledgement", migrations.ErrUnsafeMigration, o.Field.Name)
	}
	return nil
}
func (o AddField) StateForwards(state *migrations.ProjectState) error {
	if err := o.ValidateSafety(); err != nil {
		return err
	}
	state.AddField(o.AppLabel, o.ModelName, o.Field)
	return nil
}
func (o AddField) DatabaseForwards(ctx context.Context, editor migrations.SchemaEditor) error {
	table := operationTableName(o.TableName, o.AppLabel, o.ModelName)
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.AddColumn(table, o.Field))
	}
	return editor.Execute(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, columnName(o.Field), fieldKind(o.Field)))
}
func (o AddField) DatabaseBackwards(ctx context.Context, editor migrations.SchemaEditor) error {
	table := operationTableName(o.TableName, o.AppLabel, o.ModelName)
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.DropColumn(table, columnName(o.Field)))
	}
	return editor.Execute(ctx, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, columnName(o.Field)))
}
func (o AddField) Describe() string { return "Add field " + o.Field.Name }
func (o AddField) Reversible() bool { return true }
func (o AddField) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && o.ModelName == modelName
}
func (o AddField) ReferencesField(appLabel, modelName, fieldName string) bool {
	return o.ReferencesModel(appLabel, modelName) && o.Field.Name == fieldName
}
func (o AddField) SafetyChecks() []migrations.SafetyCheck {
	if !o.Field.Null && !o.HasDefault && !o.UnsafeAcknowledged {
		return []migrations.SafetyCheck{{Operation: o.Name(), Message: "adds non-null field without default"}}
	}
	return nil
}

func (o RemoveField) Name() string { return "RemoveField" }
func (o RemoveField) StateForwards(state *migrations.ProjectState) error {
	model := state.Models[key(o.AppLabel, o.ModelName)]
	model.Fields = removeField(model.Fields, o.Field.Name)
	state.Models[key(o.AppLabel, o.ModelName)] = model
	return nil
}
func (o RemoveField) DatabaseForwards(ctx context.Context, editor migrations.SchemaEditor) error {
	table := operationTableName(o.TableName, o.AppLabel, o.ModelName)
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.DropColumn(table, columnName(o.Field)))
	}
	return editor.Execute(ctx, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, columnName(o.Field)))
}
func (o RemoveField) DatabaseBackwards(ctx context.Context, editor migrations.SchemaEditor) error {
	table := operationTableName(o.TableName, o.AppLabel, o.ModelName)
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.AddColumn(table, o.Field))
	}
	return editor.Execute(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, columnName(o.Field), fieldKind(o.Field)))
}
func (o RemoveField) Describe() string { return "Remove field " + o.Field.Name }
func (o RemoveField) Reversible() bool { return true }
func (o RemoveField) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && o.ModelName == modelName
}
func (o RemoveField) ReferencesField(appLabel, modelName, fieldName string) bool {
	return o.ReferencesModel(appLabel, modelName) && o.Field.Name == fieldName
}
func (o RemoveField) SafetyChecks() []migrations.SafetyCheck {
	return []migrations.SafetyCheck{{Operation: o.Name(), Message: "drops column " + o.Field.Name}}
}

func (o AlterField) Name() string { return "AlterField" }
func (o AlterField) ValidateSafety() error {
	if o.OldField.Null && !o.NewField.Null && o.NewField.DBDefault == nil && !o.UnsafeAcknowledged {
		return fmt.Errorf("%w: making field %s non-null requires a default or acknowledgement", migrations.ErrUnsafeMigration, o.NewField.Name)
	}
	return nil
}
func (o AlterField) StateForwards(state *migrations.ProjectState) error {
	if err := o.ValidateSafety(); err != nil {
		return err
	}
	model := state.Models[key(o.AppLabel, o.ModelName)]
	for i, field := range model.Fields {
		if field.Name == o.OldField.Name {
			model.Fields[i] = o.NewField
			break
		}
	}
	state.Models[key(o.AppLabel, o.ModelName)] = model
	return nil
}
func (o AlterField) DatabaseForwards(ctx context.Context, editor migrations.SchemaEditor) error {
	if err := o.ValidateSafety(); err != nil {
		return err
	}
	table := operationTableName(o.TableName, o.AppLabel, o.ModelName)
	return executeSchemaStatements(ctx, editor, alterFieldStatements(editor, table, o.OldField, o.NewField, false))
}
func (o AlterField) DatabaseBackwards(ctx context.Context, editor migrations.SchemaEditor) error {
	table := operationTableName(o.TableName, o.AppLabel, o.ModelName)
	return executeSchemaStatements(ctx, editor, alterFieldStatements(editor, table, o.OldField, o.NewField, true))
}
func (o AlterField) Describe() string { return "Alter field " + o.NewField.Name }
func (o AlterField) Reversible() bool { return true }
func (o AlterField) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && o.ModelName == modelName
}
func (o AlterField) ReferencesField(appLabel, modelName, fieldName string) bool {
	return o.ReferencesModel(appLabel, modelName) && (o.OldField.Name == fieldName || o.NewField.Name == fieldName)
}
func (o AlterField) SafetyChecks() []migrations.SafetyCheck {
	var checks []migrations.SafetyCheck
	if isNarrowingType(o.OldField.Kind, o.NewField.Kind) {
		checks = append(checks, migrations.SafetyCheck{Operation: o.Name(), Message: "narrows field type " + o.NewField.Name})
	}
	if o.OldField.Null && !o.NewField.Null && o.NewField.DBDefault == nil && !o.UnsafeAcknowledged {
		checks = append(checks, migrations.SafetyCheck{Operation: o.Name(), Message: "makes field non-null without default"})
	}
	return checks
}

func (o RenameField) Name() string { return "RenameField" }
func (o RenameField) StateForwards(state *migrations.ProjectState) error {
	model := state.Models[key(o.AppLabel, o.ModelName)]
	for i, field := range model.Fields {
		if field.Name == o.OldName {
			field.Name = o.NewName
			if field.Column == "" || field.Column == o.OldName {
				field.Column = o.NewName
			}
			model.Fields[i] = field
			break
		}
	}
	state.Models[key(o.AppLabel, o.ModelName)] = model
	return nil
}
func (o RenameField) DatabaseForwards(ctx context.Context, editor migrations.SchemaEditor) error {
	table := operationTableName(o.TableName, o.AppLabel, o.ModelName)
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.RenameColumn(table, o.OldName, o.NewName))
	}
	return editor.Execute(ctx, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", table, o.OldName, o.NewName))
}
func (o RenameField) DatabaseBackwards(ctx context.Context, editor migrations.SchemaEditor) error {
	table := operationTableName(o.TableName, o.AppLabel, o.ModelName)
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.RenameColumn(table, o.NewName, o.OldName))
	}
	return editor.Execute(ctx, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", table, o.NewName, o.OldName))
}
func (o RenameField) Describe() string { return "Rename field " + o.OldName + " to " + o.NewName }
func (o RenameField) Reversible() bool { return true }
func (o RenameField) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && o.ModelName == modelName
}
func (o RenameField) ReferencesField(appLabel, modelName, fieldName string) bool {
	return o.ReferencesModel(appLabel, modelName) && (o.OldName == fieldName || o.NewName == fieldName)
}
func (o RenameField) SafetyChecks() []migrations.SafetyCheck {
	return []migrations.SafetyCheck{{Operation: o.Name(), Message: "renames field with ambiguous data movement"}}
}

func removeField(fields []migrations.FieldState, name string) []migrations.FieldState {
	result := fields[:0]
	for _, field := range fields {
		if field.Name != name {
			result = append(result, field)
		}
	}
	return result
}

func tableName(appLabel, modelName string) string {
	return appLabel + "_" + strings.ToLower(modelName)
}

func operationTableName(explicit, appLabel, modelName string) string {
	if explicit != "" {
		return explicit
	}
	return tableName(appLabel, modelName)
}

func columnName(field migrations.FieldState) string {
	if field.Column != "" {
		return field.Column
	}
	return field.Name
}

func fieldKind(field migrations.FieldState) string {
	if field.Kind == "" {
		return "text"
	}
	return field.Kind
}

func executeSchemaStatements(ctx context.Context, editor migrations.SchemaEditor, statements []string) error {
	for _, statement := range statements {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if err := editor.Execute(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func alterFieldStatements(editor migrations.SchemaEditor, table string, oldField, newField migrations.FieldState, backwards bool) []string {
	changes := []string{"type", "default", "null", "collation"}
	if backwards {
		for left, right := 0, len(changes)-1; left < right; left, right = left+1, right-1 {
			changes[left], changes[right] = changes[right], changes[left]
		}
	}
	statements := make([]string, 0, len(changes))
	for _, change := range changes {
		switch change {
		case "type":
			if fieldKind(oldField) != fieldKind(newField) {
				target := newField
				if backwards {
					target = oldField
				}
				statements = append(statements, alterColumnTypeSQL(editor, table, columnName(target), fieldKind(target)))
			}
		case "default":
			if !reflect.DeepEqual(oldField.DBDefault, newField.DBDefault) {
				target := newField.DBDefault
				column := columnName(newField)
				if backwards {
					target = oldField.DBDefault
					column = columnName(oldField)
				}
				statements = append(statements, alterDefaultSQL(editor, table, column, target))
			}
		case "null":
			if oldField.Null != newField.Null {
				target := newField
				if backwards {
					target = oldField
				}
				statements = append(statements, alterNullSQL(editor, table, columnName(target), target.Null))
			}
		case "collation":
			if oldField.DBCollation != newField.DBCollation {
				target := newField
				if backwards {
					target = oldField
				}
				statements = append(statements, alterColumnCollationSQL(editor, table, columnName(target), fieldKind(target), target.DBCollation))
			}
		}
	}
	return statements
}

func alterColumnTypeSQL(editor migrations.SchemaEditor, table, column, kind string) string {
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return renderer.AlterColumnType(table, column, kind)
	}
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s", table, column, kind)
}

func alterNullSQL(editor migrations.SchemaEditor, table, column string, nullable bool) string {
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return renderer.AlterNull(table, column, nullable)
	}
	action := "SET NOT NULL"
	if nullable {
		action = "DROP NOT NULL"
	}
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s", table, column, action)
}

func alterDefaultSQL(editor migrations.SchemaEditor, table, column string, value *models.DatabaseDefault) string {
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return renderer.AlterDefault(table, column, value)
	}
	if value == nil {
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT", table, column)
	}
	sql, err := fallbackDatabaseDefaultSQL(value)
	if err != nil || sql == "" {
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT", table, column)
	}
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s", table, column, sql)
}

func alterColumnCollationSQL(editor migrations.SchemaEditor, table, column, kind, collation string) string {
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return renderer.AlterColumnCollation(table, column, kind, collation)
	}
	statement := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s", table, column, kind)
	if collation != "" {
		statement += " COLLATE " + collation
	}
	return statement
}

func fallbackDatabaseDefaultSQL(value *models.DatabaseDefault) (string, error) {
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
		return fallbackLiteralDefaultSQL(defaultValue.Value)
	default:
		return "", fmt.Errorf("%w: unknown database default kind %q", models.ErrInvalidMetadata, defaultValue.Kind)
	}
}

func fallbackLiteralDefaultSQL(value any) (string, error) {
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

func isNarrowingType(oldKind, newKind string) bool {
	return oldKind == "text" && strings.HasPrefix(newKind, "varchar")
}
