package migrations

import (
	"strings"

	"github.com/cybersaksham/gogo/models"
)

// ProjectState stores historical migration state.
type ProjectState struct {
	Models map[string]ModelState
}

// ModelState stores historical model state without live Go types.
type ModelState struct {
	AppLabel    string            `json:"app_label"`
	Name        string            `json:"name"`
	TableName   string            `json:"table_name"`
	Fields      []FieldState      `json:"fields,omitempty"`
	Indexes     []IndexState      `json:"indexes,omitempty"`
	Constraints []ConstraintState `json:"constraints,omitempty"`
	Options     map[string]any    `json:"options,omitempty"`
}

// FieldState stores historical field state.
type FieldState struct {
	Name        string                  `json:"name"`
	Column      string                  `json:"column,omitempty"`
	Kind        string                  `json:"kind,omitempty"`
	ColumnTypes map[string]string       `json:"column_types,omitempty"`
	PrimaryKey  bool                    `json:"primary_key,omitempty"`
	Null        bool                    `json:"null,omitempty"`
	Unique      bool                    `json:"unique,omitempty"`
	DBIndex     bool                    `json:"db_index,omitempty"`
	DBDefault   *models.DatabaseDefault `json:"db_default,omitempty"`
	DBCollation string                  `json:"db_collation,omitempty"`
}

// IndexState stores historical index state.
type IndexState struct {
	Name         string   `json:"name"`
	Fields       []string `json:"fields,omitempty"`
	Unique       bool     `json:"unique,omitempty"`
	Expressions  []string `json:"expressions,omitempty"`
	Method       string   `json:"method,omitempty"`
	OpClasses    []string `json:"op_classes,omitempty"`
	Include      []string `json:"include,omitempty"`
	ConditionSQL string   `json:"condition_sql,omitempty"`
	Concurrently bool     `json:"concurrently,omitempty"`
	Source       string   `json:"source,omitempty"`
}

// ConstraintState stores historical constraint state.
type ConstraintState struct {
	Name              string   `json:"name"`
	Type              string   `json:"type"`
	Fields            []string `json:"fields,omitempty"`
	Expressions       []string `json:"expressions,omitempty"`
	Check             string   `json:"check,omitempty"`
	ConditionSQL      string   `json:"condition_sql,omitempty"`
	Include           []string `json:"include,omitempty"`
	OpClasses         []string `json:"op_classes,omitempty"`
	ReferencesTable   string   `json:"references_table,omitempty"`
	ReferencesColumns []string `json:"references_columns,omitempty"`
	OnDelete          string   `json:"on_delete,omitempty"`
	Deferrable        bool     `json:"deferrable,omitempty"`
	InitiallyDeferred bool     `json:"initially_deferred,omitempty"`
	Source            string   `json:"source,omitempty"`
}

// NewProjectState creates empty state.
func NewProjectState() ProjectState {
	return ProjectState{Models: make(map[string]ModelState)}
}

// Clone returns a deep state copy.
func (s ProjectState) Clone() ProjectState {
	copied := NewProjectState()
	for key, model := range s.Models {
		copied.Models[key] = model.clone()
	}
	return copied
}

// AddModel stores a model state.
func (s ProjectState) AddModel(model ModelState) {
	s.Models[modelKey(model.AppLabel, model.Name)] = model.clone()
}

// RemoveModel deletes a model state.
func (s ProjectState) RemoveModel(appLabel, modelName string) {
	delete(s.Models, modelKey(appLabel, modelName))
}

// AddField appends a field state.
func (s ProjectState) AddField(appLabel, modelName string, field FieldState) {
	key := modelKey(appLabel, modelName)
	model := s.Models[key].clone()
	model.Fields = append(model.Fields, cloneFieldState(field))
	s.Models[key] = model
}

// StateFromRegistry converts live model registry metadata into migration state.
func StateFromRegistry(registry *models.Registry) ProjectState {
	state := NewProjectState()
	if registry == nil {
		return state
	}
	for _, meta := range registry.Models() {
		if !meta.IsManaged() {
			continue
		}
		model := ModelState{
			AppLabel:    meta.AppLabel,
			Name:        meta.ModelName,
			TableName:   meta.TableName,
			Fields:      make([]FieldState, len(meta.Fields)),
			Indexes:     make([]IndexState, 0, len(meta.Indexes)+len(meta.Fields)),
			Constraints: make([]ConstraintState, 0, len(meta.Constraints)+len(meta.Fields)),
			Options: map[string]any{
				"verbose_name": meta.VerboseName,
				"ordering":     append([]string(nil), meta.Ordering...),
			},
		}
		for i, field := range meta.Fields {
			defaultValue, err := models.NormalizeDatabaseDefault(field.DBDefault)
			if err != nil {
				defaultValue = models.DatabaseDefault{}
			}
			model.Fields[i] = FieldState{
				Name:        field.Name,
				Column:      field.Column,
				Kind:        fieldKindFromMetadata(meta, field, registry),
				ColumnTypes: cloneStringMap(field.ColumnTypes),
				PrimaryKey:  field.PrimaryKey,
				Null:        field.Null,
				Unique:      field.Unique,
				DBIndex:     field.DBIndex,
				DBCollation: field.DBCollation,
			}
			if defaultValue.Kind != models.DefaultNone {
				model.Fields[i].DBDefault = &defaultValue
			}
		}
		for _, index := range meta.Indexes {
			model.Indexes = appendIndexState(model.Indexes, IndexState{
				Name:         index.NameFor(meta.TableName),
				Fields:       index.FieldNames(),
				Unique:       index.Unique,
				Expressions:  append([]string(nil), index.Expressions...),
				Method:       index.Method,
				OpClasses:    append([]string(nil), index.OpClasses...),
				Include:      append([]string(nil), index.Include...),
				ConditionSQL: index.Condition,
				Source:       "model",
			})
		}
		for _, constraint := range meta.Constraints {
			model.Constraints = appendConstraintState(model.Constraints, ConstraintState{
				Name:         constraint.NameFor(meta.TableName),
				Type:         string(constraint.Type),
				Fields:       constraint.FieldNames(),
				Expressions:  append([]string(nil), constraint.Expressions...),
				Check:        constraint.Check,
				ConditionSQL: constraint.Condition,
				Include:      append([]string(nil), constraint.Include...),
				OpClasses:    append([]string(nil), constraint.OpClasses...),
				Source:       "model",
			})
		}
		for _, field := range model.Fields {
			if field.DBIndex {
				model.Indexes = appendIndexState(model.Indexes, fieldIndexState(meta.TableName, field))
			}
			if field.Unique && !field.PrimaryKey {
				model.Constraints = appendConstraintState(model.Constraints, fieldUniqueConstraintState(meta.TableName, field))
			}
		}
		state.AddModel(model)
	}
	return state
}

func fieldKindFromMetadata(meta models.Metadata, field models.FieldMeta, registry *models.Registry) string {
	if field.Kind != "" || field.RelationTarget == "" {
		return field.Kind
	}
	target, ok := relationTargetMetadata(meta, field.RelationTarget, registry)
	if !ok {
		return "bigint"
	}
	for _, targetField := range target.Fields {
		if targetField.PrimaryKey {
			if targetField.Kind != "" {
				return targetField.Kind
			}
			break
		}
	}
	return "bigint"
}

func relationTargetMetadata(meta models.Metadata, target string, registry *models.Registry) (models.Metadata, bool) {
	if registry == nil {
		return models.Metadata{}, false
	}
	if target == "self" {
		target = meta.Label()
	} else if !strings.Contains(target, ".") {
		target = meta.AppLabel + "." + target
	}
	return registry.Lookup(target)
}

func (m ModelState) clone() ModelState {
	m.Fields = cloneFieldStates(m.Fields)
	m.Indexes = cloneIndexStates(m.Indexes)
	m.Constraints = cloneConstraintStates(m.Constraints)
	if m.Options != nil {
		options := make(map[string]any, len(m.Options))
		for key, value := range m.Options {
			options[key] = value
		}
		m.Options = options
	}
	return m
}

func cloneFieldStates(fields []FieldState) []FieldState {
	copied := make([]FieldState, len(fields))
	for i, field := range fields {
		copied[i] = cloneFieldState(field)
	}
	return copied
}

func cloneFieldState(field FieldState) FieldState {
	field.ColumnTypes = cloneStringMap(field.ColumnTypes)
	return field
}

func cloneIndexStates(indexes []IndexState) []IndexState {
	copied := make([]IndexState, len(indexes))
	for i, index := range indexes {
		copied[i] = IndexState{
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
	return copied
}

func cloneConstraintStates(constraints []ConstraintState) []ConstraintState {
	copied := make([]ConstraintState, len(constraints))
	for i, constraint := range constraints {
		copied[i] = ConstraintState{
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
	return copied
}

func appendIndexState(indexes []IndexState, index IndexState) []IndexState {
	for _, existing := range indexes {
		if existing.Name == index.Name {
			return indexes
		}
	}
	return append(indexes, index)
}

func appendConstraintState(constraints []ConstraintState, constraint ConstraintState) []ConstraintState {
	for _, existing := range constraints {
		if existing.Name == constraint.Name {
			return constraints
		}
	}
	return append(constraints, constraint)
}

func fieldIndexState(table string, field FieldState) IndexState {
	column := fieldColumnName(field)
	index := models.Index{Fields: []models.IndexField{models.Asc(column)}}
	return IndexState{Name: index.NameFor(table), Fields: []string{column}, Source: "field"}
}

func fieldUniqueConstraintState(table string, field FieldState) ConstraintState {
	column := fieldColumnName(field)
	constraint := models.Constraint{Type: models.ConstraintUnique, Fields: []models.IndexField{models.Asc(column)}}
	return ConstraintState{Name: constraint.NameFor(table), Type: string(models.ConstraintUnique), Fields: []string{column}, Source: "field"}
}

func fieldColumnName(field FieldState) string {
	if field.Column != "" {
		return field.Column
	}
	return field.Name
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func modelKey(appLabel, modelName string) string {
	return appLabel + "." + modelName
}
