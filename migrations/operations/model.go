package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/Newton-School/gogo/migrations"
)

type CreateModel struct{ Model migrations.ModelState }
type DeleteModel struct{ Model migrations.ModelState }
type RenameModel struct{ AppLabel, OldName, NewName string }
type AlterModelTable struct{ AppLabel, ModelName, OldTable, NewTable string }
type AlterModelTableComment struct{ AppLabel, ModelName, Comment string }
type AlterModelOptions struct {
	AppLabel, ModelName string
	Options             map[string]any
}
type AlterModelManagers struct {
	AppLabel, ModelName string
	Managers            []string
}
type AlterOrderWithRespectTo struct{ AppLabel, ModelName, Field string }
type AlterTogether struct {
	AppLabel, ModelName string
	UniqueTogether      [][]string
	IndexTogether       [][]string
}

func (o CreateModel) Name() string { return "CreateModel" }
func (o CreateModel) StateForwards(state *migrations.ProjectState) error {
	state.AddModel(o.Model)
	return nil
}
func (o CreateModel) DatabaseForwards(ctx context.Context, editor migrations.SchemaEditor) error {
	return createModelDatabase(ctx, editor, o.Model)
}
func (o CreateModel) DatabaseBackwards(ctx context.Context, editor migrations.SchemaEditor) error {
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.DropTable(modelTableName(o.Model)))
	}
	return editor.Execute(ctx, fmt.Sprintf("DROP TABLE %s", o.Model.TableName))
}
func (o CreateModel) Describe() string { return "Create model " + o.Model.Name }
func (o CreateModel) Reversible() bool { return true }
func (o CreateModel) ReferencesModel(appLabel, modelName string) bool {
	return o.Model.AppLabel == appLabel && o.Model.Name == modelName
}
func (o CreateModel) ReferencesField(string, string, string) bool { return false }
func (o CreateModel) InitialTables() []string {
	if o.Model.TableName == "" {
		return nil
	}
	return []string{o.Model.TableName}
}
func (o CreateModel) InitialSchema() []migrations.TableSchema {
	table := o.Model.TableName
	if table == "" {
		table = tableName(o.Model.AppLabel, o.Model.Name)
	}
	fields := o.Model.Fields
	if len(fields) == 0 {
		fields = []migrations.FieldState{{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true}}
	}
	columns := make([]migrations.ColumnSchema, 0, len(fields))
	for _, field := range fields {
		columns = append(columns, migrations.ColumnSchema{
			Name:           columnName(field),
			Kind:           fieldKind(field),
			NormalizedKind: migrations.NormalizeColumnKind(fieldKind(field)),
			Default:        field.DBDefault,
			Collation:      field.DBCollation,
			PrimaryKey:     field.PrimaryKey,
			Nullable:       field.Null,
		})
	}
	indexes := make([]migrations.IndexSchema, 0, len(o.Model.Indexes))
	for _, index := range o.Model.Indexes {
		indexes = append(indexes, migrations.IndexSchemaFromState(index))
	}
	constraints := make([]migrations.ConstraintSchema, 0, len(o.Model.Constraints))
	for _, constraint := range o.Model.Constraints {
		constraints = append(constraints, migrations.ConstraintSchemaFromState(constraint))
	}
	return []migrations.TableSchema{{Name: table, Columns: columns, Indexes: indexes, Constraints: constraints}}
}

func (o DeleteModel) Name() string { return "DeleteModel" }
func (o DeleteModel) StateForwards(state *migrations.ProjectState) error {
	state.RemoveModel(o.Model.AppLabel, o.Model.Name)
	return nil
}
func (o DeleteModel) DatabaseForwards(ctx context.Context, editor migrations.SchemaEditor) error {
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.DropTable(modelTableName(o.Model)))
	}
	return editor.Execute(ctx, fmt.Sprintf("DROP TABLE %s", o.Model.TableName))
}
func (o DeleteModel) DatabaseBackwards(ctx context.Context, editor migrations.SchemaEditor) error {
	return createModelDatabase(ctx, editor, o.Model)
}
func (o DeleteModel) Describe() string { return "Delete model " + o.Model.Name }
func (o DeleteModel) Reversible() bool { return true }
func (o DeleteModel) ReferencesModel(appLabel, modelName string) bool {
	return o.Model.AppLabel == appLabel && o.Model.Name == modelName
}
func (o DeleteModel) ReferencesField(string, string, string) bool { return false }
func (o DeleteModel) SafetyChecks() []migrations.SafetyCheck {
	return []migrations.SafetyCheck{{Operation: o.Name(), Message: "drops table " + o.Model.TableName}}
}

func (o RenameModel) Name() string { return "RenameModel" }
func (o RenameModel) StateForwards(state *migrations.ProjectState) error {
	oldKey := key(o.AppLabel, o.OldName)
	model := state.Models[oldKey]
	delete(state.Models, oldKey)
	model.Name = o.NewName
	state.Models[key(o.AppLabel, o.NewName)] = model
	return nil
}
func (o RenameModel) DatabaseForwards(context.Context, migrations.SchemaEditor) error  { return nil }
func (o RenameModel) DatabaseBackwards(context.Context, migrations.SchemaEditor) error { return nil }
func (o RenameModel) Describe() string                                                 { return "Rename model " + o.OldName + " to " + o.NewName }
func (o RenameModel) Reversible() bool                                                 { return true }
func (o RenameModel) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && (o.OldName == modelName || o.NewName == modelName)
}
func (o RenameModel) ReferencesField(string, string, string) bool { return false }

func (o AlterModelTable) Name() string { return "AlterModelTable" }
func (o AlterModelTable) StateForwards(state *migrations.ProjectState) error {
	model := state.Models[key(o.AppLabel, o.ModelName)]
	model.TableName = o.NewTable
	state.Models[key(o.AppLabel, o.ModelName)] = model
	return nil
}
func (o AlterModelTable) DatabaseForwards(ctx context.Context, editor migrations.SchemaEditor) error {
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.RenameTable(o.OldTable, o.NewTable))
	}
	return editor.Execute(ctx, fmt.Sprintf("ALTER TABLE %s RENAME TO %s", o.OldTable, o.NewTable))
}
func (o AlterModelTable) DatabaseBackwards(ctx context.Context, editor migrations.SchemaEditor) error {
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.RenameTable(o.NewTable, o.OldTable))
	}
	return editor.Execute(ctx, fmt.Sprintf("ALTER TABLE %s RENAME TO %s", o.NewTable, o.OldTable))
}
func (o AlterModelTable) Describe() string { return "Alter model table " + o.ModelName }
func (o AlterModelTable) Reversible() bool { return true }
func (o AlterModelTable) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && o.ModelName == modelName
}
func (o AlterModelTable) ReferencesField(string, string, string) bool { return false }

func (o AlterModelTableComment) Name() string { return "AlterModelTableComment" }
func (o AlterModelTableComment) StateForwards(state *migrations.ProjectState) error {
	model := state.Models[key(o.AppLabel, o.ModelName)]
	ensureOptions(&model)
	model.Options["table_comment"] = o.Comment
	state.Models[key(o.AppLabel, o.ModelName)] = model
	return nil
}
func (o AlterModelTableComment) DatabaseForwards(ctx context.Context, editor migrations.SchemaEditor) error {
	return editor.Execute(ctx, "-- alter table comment "+o.Comment)
}
func (o AlterModelTableComment) DatabaseBackwards(context.Context, migrations.SchemaEditor) error {
	return nil
}
func (o AlterModelTableComment) Describe() string { return "Alter table comment " + o.ModelName }
func (o AlterModelTableComment) Reversible() bool { return true }
func (o AlterModelTableComment) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && o.ModelName == modelName
}
func (o AlterModelTableComment) ReferencesField(string, string, string) bool { return false }

func (o AlterModelOptions) Name() string { return "AlterModelOptions" }
func (o AlterModelOptions) StateForwards(state *migrations.ProjectState) error {
	model := state.Models[key(o.AppLabel, o.ModelName)]
	ensureOptions(&model)
	for key, value := range o.Options {
		model.Options[key] = value
	}
	state.Models[key(o.AppLabel, o.ModelName)] = model
	return nil
}
func (o AlterModelOptions) DatabaseForwards(context.Context, migrations.SchemaEditor) error {
	return nil
}
func (o AlterModelOptions) DatabaseBackwards(context.Context, migrations.SchemaEditor) error {
	return nil
}
func (o AlterModelOptions) Describe() string { return "Alter model options " + o.ModelName }
func (o AlterModelOptions) Reversible() bool { return true }
func (o AlterModelOptions) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && o.ModelName == modelName
}
func (o AlterModelOptions) ReferencesField(string, string, string) bool { return false }

func (o AlterModelManagers) Name() string { return "AlterModelManagers" }
func (o AlterModelManagers) StateForwards(state *migrations.ProjectState) error {
	model := state.Models[key(o.AppLabel, o.ModelName)]
	ensureOptions(&model)
	model.Options["managers"] = append([]string(nil), o.Managers...)
	state.Models[key(o.AppLabel, o.ModelName)] = model
	return nil
}
func (o AlterModelManagers) DatabaseForwards(context.Context, migrations.SchemaEditor) error {
	return nil
}
func (o AlterModelManagers) DatabaseBackwards(context.Context, migrations.SchemaEditor) error {
	return nil
}
func (o AlterModelManagers) Describe() string { return "Alter model managers " + o.ModelName }
func (o AlterModelManagers) Reversible() bool { return true }
func (o AlterModelManagers) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && o.ModelName == modelName
}
func (o AlterModelManagers) ReferencesField(string, string, string) bool { return false }

func (o AlterOrderWithRespectTo) Name() string { return "AlterOrderWithRespectTo" }
func (o AlterOrderWithRespectTo) StateForwards(state *migrations.ProjectState) error {
	model := state.Models[key(o.AppLabel, o.ModelName)]
	ensureOptions(&model)
	model.Options["order_with_respect_to"] = o.Field
	state.Models[key(o.AppLabel, o.ModelName)] = model
	return nil
}
func (o AlterOrderWithRespectTo) DatabaseForwards(context.Context, migrations.SchemaEditor) error {
	return nil
}
func (o AlterOrderWithRespectTo) DatabaseBackwards(context.Context, migrations.SchemaEditor) error {
	return nil
}
func (o AlterOrderWithRespectTo) Describe() string {
	return "Alter order with respect to " + o.ModelName
}
func (o AlterOrderWithRespectTo) Reversible() bool { return true }
func (o AlterOrderWithRespectTo) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && o.ModelName == modelName
}
func (o AlterOrderWithRespectTo) ReferencesField(appLabel, modelName, fieldName string) bool {
	return o.ReferencesModel(appLabel, modelName) && o.Field == fieldName
}

func (o AlterTogether) Name() string { return "AlterTogether" }
func (o AlterTogether) StateForwards(state *migrations.ProjectState) error {
	model := state.Models[key(o.AppLabel, o.ModelName)]
	ensureOptions(&model)
	model.Options["unique_together"] = o.UniqueTogether
	model.Options["index_together"] = o.IndexTogether
	state.Models[key(o.AppLabel, o.ModelName)] = model
	return nil
}
func (o AlterTogether) DatabaseForwards(context.Context, migrations.SchemaEditor) error  { return nil }
func (o AlterTogether) DatabaseBackwards(context.Context, migrations.SchemaEditor) error { return nil }
func (o AlterTogether) Describe() string                                                 { return "Alter together " + o.ModelName }
func (o AlterTogether) Reversible() bool                                                 { return true }
func (o AlterTogether) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && o.ModelName == modelName
}
func (o AlterTogether) ReferencesField(string, string, string) bool { return false }

func createTableSQL(model migrations.ModelState) string {
	table := modelTableName(model)
	fields := modelFields(model)
	columns := make([]string, 0, len(fields))
	for _, field := range fields {
		column := columnName(field)
		kind := fieldKind(field)
		if field.PrimaryKey && kind == "text" {
			kind = "bigint"
		}
		parts := []string{column, kind}
		if field.PrimaryKey {
			parts = append(parts, "PRIMARY KEY")
		} else if !field.Null {
			parts = append(parts, "NOT NULL")
		}
		columns = append(columns, strings.Join(parts, " "))
	}
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", table, strings.Join(columns, ", "))
}

func createModelDatabase(ctx context.Context, editor migrations.SchemaEditor, model migrations.ModelState) error {
	table := modelTableName(model)
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		if err := editor.Execute(ctx, renderer.CreateTable(table, modelFields(model))); err != nil {
			return err
		}
	} else if err := editor.Execute(ctx, createTableSQL(model)); err != nil {
		return err
	}
	for _, constraint := range model.Constraints {
		if err := (AddConstraint{AppLabel: model.AppLabel, ModelName: model.Name, TableName: table, Constraint: constraint}).DatabaseForwards(ctx, editor); err != nil {
			return err
		}
	}
	for _, index := range model.Indexes {
		if err := (AddIndex{AppLabel: model.AppLabel, ModelName: model.Name, TableName: table, Index: index}).DatabaseForwards(ctx, editor); err != nil {
			return err
		}
	}
	return nil
}

func modelTableName(model migrations.ModelState) string {
	if model.TableName != "" {
		return model.TableName
	}
	return tableName(model.AppLabel, model.Name)
}

func modelFields(model migrations.ModelState) []migrations.FieldState {
	if len(model.Fields) > 0 {
		return model.Fields
	}
	return []migrations.FieldState{{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true}}
}

func ensureOptions(model *migrations.ModelState) {
	if model.Options == nil {
		model.Options = make(map[string]any)
	}
}

func key(appLabel, modelName string) string {
	return appLabel + "." + modelName
}
