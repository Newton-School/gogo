package operations

import "github.com/cybersaksham/gogo/migrations"

func (o CreateModel) MigrationOperationSpec() migrations.OperationSpec {
	model := cloneModelState(o.Model)
	return migrations.OperationSpec{Type: o.Name(), Model: &model}
}

func (o DeleteModel) MigrationOperationSpec() migrations.OperationSpec {
	model := cloneModelState(o.Model)
	return migrations.OperationSpec{Type: o.Name(), Model: &model}
}

func (o RenameModel) MigrationOperationSpec() migrations.OperationSpec {
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, OldName: o.OldName, NewName: o.NewName}
}

func (o AlterModelTable) MigrationOperationSpec() migrations.OperationSpec {
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, OldTable: o.OldTable, NewTable: o.NewTable}
}

func (o AlterModelTableComment) MigrationOperationSpec() migrations.OperationSpec {
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, Comment: o.Comment}
}

func (o AlterModelOptions) MigrationOperationSpec() migrations.OperationSpec {
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, Options: cloneOptions(o.Options)}
}

func (o AlterModelManagers) MigrationOperationSpec() migrations.OperationSpec {
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, Managers: append([]string(nil), o.Managers...)}
}

func (o AlterOrderWithRespectTo) MigrationOperationSpec() migrations.OperationSpec {
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, FieldName: o.Field}
}

func (o AlterTogether) MigrationOperationSpec() migrations.OperationSpec {
	return migrations.OperationSpec{
		Type:           o.Name(),
		AppLabel:       o.AppLabel,
		ModelName:      o.ModelName,
		UniqueTogether: cloneStringMatrix(o.UniqueTogether),
		IndexTogether:  cloneStringMatrix(o.IndexTogether),
	}
}

func (o AddField) MigrationOperationSpec() migrations.OperationSpec {
	field := cloneFieldState(o.Field)
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, TableName: o.TableName, Field: &field, HasDefault: o.HasDefault, UnsafeAcknowledged: o.UnsafeAcknowledged}
}

func (o RemoveField) MigrationOperationSpec() migrations.OperationSpec {
	field := cloneFieldState(o.Field)
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, TableName: o.TableName, Field: &field}
}

func (o AlterField) MigrationOperationSpec() migrations.OperationSpec {
	oldField := cloneFieldState(o.OldField)
	newField := cloneFieldState(o.NewField)
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, TableName: o.TableName, OldField: &oldField, NewField: &newField, UnsafeAcknowledged: o.UnsafeAcknowledged}
}

func (o RenameField) MigrationOperationSpec() migrations.OperationSpec {
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, TableName: o.TableName, OldName: o.OldName, NewName: o.NewName}
}

func (o AddIndex) MigrationOperationSpec() migrations.OperationSpec {
	index := cloneIndexState(o.Index)
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, TableName: o.TableName, Index: &index}
}

func (o RemoveIndex) MigrationOperationSpec() migrations.OperationSpec {
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, TableName: o.TableName, IndexName: o.IndexName}
}

func (o RenameIndex) MigrationOperationSpec() migrations.OperationSpec {
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, TableName: o.TableName, OldName: o.OldName, NewName: o.NewName}
}

func (o AddConstraint) MigrationOperationSpec() migrations.OperationSpec {
	constraint := cloneConstraintState(o.Constraint)
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, TableName: o.TableName, Constraint: &constraint}
}

func (o RemoveConstraint) MigrationOperationSpec() migrations.OperationSpec {
	return migrations.OperationSpec{Type: o.Name(), AppLabel: o.AppLabel, ModelName: o.ModelName, TableName: o.TableName, ConstraintName: o.ConstraintName, ConstraintType: o.ConstraintType}
}

func (o RunSQL) MigrationOperationSpec() migrations.OperationSpec {
	return migrations.OperationSpec{Type: o.Name(), SQL: o.SQL, ReverseSQL: o.ReverseSQL, Elidable: o.ElidableOp}
}

func (o SeparateDatabaseAndState) MigrationOperationSpec() migrations.OperationSpec {
	spec := migrations.OperationSpec{Type: o.Name()}
	for _, operation := range o.DatabaseOperations {
		spec.DatabaseOperations = append(spec.DatabaseOperations, migrations.OperationSpecFor(operation))
	}
	for _, operation := range o.StateOperations {
		spec.StateOperations = append(spec.StateOperations, migrations.OperationSpecFor(operation))
	}
	return spec
}

func cloneModelState(model migrations.ModelState) migrations.ModelState {
	model.Fields = cloneFieldStates(model.Fields)
	indexes := model.Indexes
	model.Indexes = make([]migrations.IndexState, len(indexes))
	for index, value := range indexes {
		model.Indexes[index] = cloneIndexState(value)
	}
	constraints := model.Constraints
	model.Constraints = make([]migrations.ConstraintState, len(constraints))
	for index, value := range constraints {
		model.Constraints[index] = cloneConstraintState(value)
	}
	model.Options = cloneOptions(model.Options)
	return model
}

func cloneFieldStates(fields []migrations.FieldState) []migrations.FieldState {
	copied := make([]migrations.FieldState, len(fields))
	for index, field := range fields {
		copied[index] = cloneFieldState(field)
	}
	return copied
}

func cloneFieldState(field migrations.FieldState) migrations.FieldState {
	field.ColumnTypes = cloneStringMap(field.ColumnTypes)
	return field
}

func cloneIndexState(index migrations.IndexState) migrations.IndexState {
	return migrations.IndexState{
		Name:         index.Name,
		Fields:       append([]string(nil), index.Fields...),
		Unique:       index.Unique,
		Expressions:  append([]string(nil), index.Expressions...),
		Method:       index.Method,
		OpClasses:    append([]string(nil), index.OpClasses...),
		Include:      append([]string(nil), index.Include...),
		ConditionSQL: index.ConditionSQL,
		Concurrently: index.Concurrently,
		Source:       index.Source,
	}
}

func cloneConstraintState(constraint migrations.ConstraintState) migrations.ConstraintState {
	return migrations.ConstraintState{
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
		Source:            constraint.Source,
	}
}

func cloneOptions(options map[string]any) map[string]any {
	if options == nil {
		return nil
	}
	clone := make(map[string]any, len(options))
	for key, value := range options {
		clone[key] = value
	}
	return clone
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func cloneStringMatrix(values [][]string) [][]string {
	if values == nil {
		return nil
	}
	clone := make([][]string, len(values))
	for index, value := range values {
		clone[index] = append([]string(nil), value...)
	}
	return clone
}
