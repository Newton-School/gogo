package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/Newton-School/gogo/migrations"
)

type AddIndex struct {
	AppLabel, ModelName string
	TableName           string
	Index               migrations.IndexState
}
type RemoveIndex struct {
	AppLabel, ModelName string
	TableName           string
	IndexName           string
}
type RenameIndex struct {
	AppLabel, ModelName string
	TableName           string
	OldName, NewName    string
}

func (o AddIndex) Name() string { return "AddIndex" }
func (o AddIndex) StateForwards(state *migrations.ProjectState) error {
	model := state.Models[key(o.AppLabel, o.ModelName)]
	model.Indexes = append(model.Indexes, o.Index)
	state.Models[key(o.AppLabel, o.ModelName)] = model
	return nil
}
func (o AddIndex) DatabaseForwards(ctx context.Context, editor migrations.SchemaEditor) error {
	table := operationTableName(o.TableName, o.AppLabel, o.ModelName)
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.AddIndex(table, o.Index))
	}
	return editor.Execute(ctx, fmt.Sprintf("CREATE INDEX %s ON %s (%s)", o.Index.Name, table, strings.Join(o.Index.Fields, ", ")))
}
func (o AddIndex) DatabaseBackwards(ctx context.Context, editor migrations.SchemaEditor) error {
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.DropIndex(o.Index.Name))
	}
	return editor.Execute(ctx, fmt.Sprintf("DROP INDEX %s", o.Index.Name))
}
func (o AddIndex) Describe() string { return "Add index " + o.Index.Name }
func (o AddIndex) Reversible() bool { return true }
func (o AddIndex) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && o.ModelName == modelName
}
func (o AddIndex) ReferencesField(appLabel, modelName, fieldName string) bool {
	return o.ReferencesModel(appLabel, modelName) && contains(o.Index.Fields, fieldName)
}

func (o RemoveIndex) Name() string { return "RemoveIndex" }
func (o RemoveIndex) StateForwards(state *migrations.ProjectState) error {
	model := state.Models[key(o.AppLabel, o.ModelName)]
	indexes := model.Indexes[:0]
	for _, index := range model.Indexes {
		if index.Name != o.IndexName {
			indexes = append(indexes, index)
		}
	}
	model.Indexes = indexes
	state.Models[key(o.AppLabel, o.ModelName)] = model
	return nil
}
func (o RemoveIndex) DatabaseForwards(ctx context.Context, editor migrations.SchemaEditor) error {
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.DropIndex(o.IndexName))
	}
	return editor.Execute(ctx, fmt.Sprintf("DROP INDEX %s", o.IndexName))
}
func (o RemoveIndex) DatabaseBackwards(context.Context, migrations.SchemaEditor) error { return nil }
func (o RemoveIndex) Describe() string {
	return "Remove index " + o.IndexName
}
func (o RemoveIndex) Reversible() bool { return false }
func (o RemoveIndex) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && o.ModelName == modelName
}
func (o RemoveIndex) ReferencesField(string, string, string) bool { return false }

func (o RenameIndex) Name() string { return "RenameIndex" }
func (o RenameIndex) StateForwards(state *migrations.ProjectState) error {
	model := state.Models[key(o.AppLabel, o.ModelName)]
	for i, index := range model.Indexes {
		if index.Name == o.OldName {
			model.Indexes[i].Name = o.NewName
		}
	}
	state.Models[key(o.AppLabel, o.ModelName)] = model
	return nil
}
func (o RenameIndex) DatabaseForwards(ctx context.Context, editor migrations.SchemaEditor) error {
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.RenameIndex(o.OldName, o.NewName))
	}
	return editor.Execute(ctx, fmt.Sprintf("ALTER INDEX %s RENAME TO %s", o.OldName, o.NewName))
}
func (o RenameIndex) DatabaseBackwards(ctx context.Context, editor migrations.SchemaEditor) error {
	if renderer, ok := editor.(migrations.SchemaRenderer); ok {
		return editor.Execute(ctx, renderer.RenameIndex(o.NewName, o.OldName))
	}
	return editor.Execute(ctx, fmt.Sprintf("ALTER INDEX %s RENAME TO %s", o.NewName, o.OldName))
}
func (o RenameIndex) Describe() string { return "Rename index " + o.OldName }
func (o RenameIndex) Reversible() bool { return true }
func (o RenameIndex) ReferencesModel(appLabel, modelName string) bool {
	return o.AppLabel == appLabel && o.ModelName == modelName
}
func (o RenameIndex) ReferencesField(string, string, string) bool { return false }

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
