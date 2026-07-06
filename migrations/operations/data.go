package operations

import (
	"context"

	"github.com/Newton-School/gogo/migrations"
)

type RunGoFunc func(context.Context, *migrations.ProjectState) error

type RunGo struct {
	Code        RunGoFunc
	ReverseCode RunGoFunc
	ElidableOp  bool
}

func (o RunGo) Name() string { return "RunGo" }
func (o RunGo) StateForwards(state *migrations.ProjectState) error {
	if o.Code == nil {
		return nil
	}
	return o.Code(context.Background(), state)
}
func (o RunGo) DatabaseForwards(context.Context, migrations.SchemaEditor) error { return nil }
func (o RunGo) DatabaseBackwards(ctx context.Context, editor migrations.SchemaEditor) error {
	_ = editor
	if o.ReverseCode == nil {
		return migrations.ErrIrreversibleOperation
	}
	state := migrations.NewProjectState()
	return o.ReverseCode(ctx, &state)
}
func (o RunGo) Describe() string                            { return "Run Go data migration" }
func (o RunGo) Reversible() bool                            { return o.ReverseCode != nil }
func (o RunGo) ReferencesModel(string, string) bool         { return false }
func (o RunGo) ReferencesField(string, string, string) bool { return false }
func (o RunGo) ReducesToSQL() bool                          { return false }
func (o RunGo) Elidable() bool                              { return o.ElidableOp }
func (o RunGo) Category() OperationCategory                 { return CategoryData }

func referencesModel(operations []migrations.Operation, appLabel, modelName string) bool {
	for _, operation := range operations {
		if operation.ReferencesModel(appLabel, modelName) {
			return true
		}
	}
	return false
}

func referencesField(operations []migrations.Operation, appLabel, modelName, fieldName string) bool {
	for _, operation := range operations {
		if operation.ReferencesField(appLabel, modelName, fieldName) {
			return true
		}
	}
	return false
}
