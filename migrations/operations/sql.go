package operations

import (
	"context"

	"github.com/Newton-School/gogo/migrations"
)

type OperationCategory string

const (
	CategorySQL   OperationCategory = "sql"
	CategoryData  OperationCategory = "data"
	CategoryMixed OperationCategory = "mixed"
)

type RunSQL struct {
	SQL        string
	ReverseSQL string
	ElidableOp bool
}

func (o RunSQL) Name() string { return "RunSQL" }
func (o RunSQL) StateForwards(*migrations.ProjectState) error {
	return nil
}
func (o RunSQL) DatabaseForwards(ctx context.Context, editor migrations.SchemaEditor) error {
	return editor.Execute(ctx, o.SQL)
}
func (o RunSQL) DatabaseBackwards(ctx context.Context, editor migrations.SchemaEditor) error {
	if o.ReverseSQL == "" {
		return migrations.ErrIrreversibleOperation
	}
	return editor.Execute(ctx, o.ReverseSQL)
}
func (o RunSQL) Describe() string                            { return "Run raw SQL" }
func (o RunSQL) Reversible() bool                            { return o.ReverseSQL != "" }
func (o RunSQL) ReferencesModel(string, string) bool         { return false }
func (o RunSQL) ReferencesField(string, string, string) bool { return false }
func (o RunSQL) ReducesToSQL() bool                          { return true }
func (o RunSQL) Elidable() bool                              { return o.ElidableOp }
func (o RunSQL) Category() OperationCategory                 { return CategorySQL }
func (o RunSQL) InitialTables() []string {
	table, ok := migrations.InitialTableNameFromSQL(o.SQL)
	if !ok {
		return nil
	}
	return []string{table}
}
func (o RunSQL) Reduce(next migrations.Operation) []migrations.Operation {
	return []migrations.Operation{o, next}
}

type SeparateDatabaseAndState struct {
	DatabaseOperations []migrations.Operation
	StateOperations    []migrations.Operation
}

func (o SeparateDatabaseAndState) Name() string { return "SeparateDatabaseAndState" }
func (o SeparateDatabaseAndState) StateForwards(state *migrations.ProjectState) error {
	for _, operation := range o.StateOperations {
		if err := operation.StateForwards(state); err != nil {
			return err
		}
	}
	return nil
}
func (o SeparateDatabaseAndState) DatabaseForwards(ctx context.Context, editor migrations.SchemaEditor) error {
	for _, operation := range o.DatabaseOperations {
		if err := operation.DatabaseForwards(ctx, editor); err != nil {
			return err
		}
	}
	return nil
}
func (o SeparateDatabaseAndState) DatabaseBackwards(ctx context.Context, editor migrations.SchemaEditor) error {
	for i := len(o.DatabaseOperations) - 1; i >= 0; i-- {
		if err := o.DatabaseOperations[i].DatabaseBackwards(ctx, editor); err != nil {
			return err
		}
	}
	return nil
}
func (o SeparateDatabaseAndState) Describe() string { return "Separate database and state" }
func (o SeparateDatabaseAndState) Reversible() bool {
	for _, operation := range o.DatabaseOperations {
		if !operation.Reversible() {
			return false
		}
	}
	return true
}
func (o SeparateDatabaseAndState) ReferencesModel(appLabel, modelName string) bool {
	return referencesModel(o.DatabaseOperations, appLabel, modelName) || referencesModel(o.StateOperations, appLabel, modelName)
}
func (o SeparateDatabaseAndState) ReferencesField(appLabel, modelName, fieldName string) bool {
	return referencesField(o.DatabaseOperations, appLabel, modelName, fieldName) || referencesField(o.StateOperations, appLabel, modelName, fieldName)
}
