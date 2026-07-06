package migrations

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"unicode"

	"github.com/Newton-School/gogo/models"
	modelconstraints "github.com/Newton-School/gogo/models/constraints"
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
	Name            string                  `json:"name"`
	Column          string                  `json:"column,omitempty"`
	Kind            string                  `json:"kind,omitempty"`
	ColumnTypes     map[string]string       `json:"column_types,omitempty"`
	PrimaryKey      bool                    `json:"primary_key,omitempty"`
	Null            bool                    `json:"null,omitempty"`
	Unique          bool                    `json:"unique,omitempty"`
	DBIndex         bool                    `json:"db_index,omitempty"`
	DBDefault       *models.DatabaseDefault `json:"db_default,omitempty"`
	DBCollation     string                  `json:"db_collation,omitempty"`
	TargetFieldName string                  `json:"target_field_name,omitempty"`
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
				Name:            field.Name,
				Column:          field.Column,
				Kind:            fieldKindFromMetadata(meta, field, registry),
				ColumnTypes:     cloneStringMap(field.ColumnTypes),
				PrimaryKey:      field.PrimaryKey,
				Null:            field.Null,
				Unique:          field.Unique,
				DBIndex:         field.DBIndex,
				DBCollation:     field.DBCollation,
				TargetFieldName: field.TargetFieldName,
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
			if constraint.RequiresIndex() {
				model.Indexes = appendIndexState(model.Indexes, uniqueIndexStateFromConstraint(meta.TableName, constraint))
				continue
			}
			model.Constraints = appendConstraintState(model.Constraints, constraintStateFromMetadata(meta.TableName, constraint))
		}
		for _, field := range model.Fields {
			if field.DBIndex {
				model.Indexes = appendIndexState(model.Indexes, fieldIndexState(meta.TableName, field))
			}
			if field.Unique && !field.PrimaryKey {
				model.Constraints = appendConstraintState(model.Constraints, fieldUniqueConstraintState(meta.TableName, field))
			}
		}
		for _, field := range meta.Fields {
			foreignKey, ok := ForeignKeyConstraintFromRelation(meta, field, registry)
			if !ok || hasEquivalentForeignKeyConstraint(model.Constraints, foreignKey) {
				continue
			}
			model.Constraints = appendConstraintState(model.Constraints, foreignKey)
		}
		state.AddModel(model)
	}
	return state
}

func uniqueIndexStateFromConstraint(table string, constraint models.Constraint) IndexState {
	return IndexState{
		Name:         constraint.NameFor(table),
		Fields:       constraint.FieldNames(),
		Unique:       true,
		Expressions:  append([]string(nil), constraint.Expressions...),
		OpClasses:    append([]string(nil), constraint.OpClasses...),
		Include:      append([]string(nil), constraint.Include...),
		ConditionSQL: constraint.Condition,
		Source:       "constraint",
	}
}

func constraintStateFromMetadata(table string, constraint models.Constraint) ConstraintState {
	return ConstraintState{
		Name:              constraint.NameFor(table),
		Type:              string(constraint.Type),
		Fields:            constraint.FieldNames(),
		Expressions:       append([]string(nil), constraint.Expressions...),
		Check:             constraint.Check,
		ConditionSQL:      constraint.Condition,
		Include:           append([]string(nil), constraint.Include...),
		OpClasses:         append([]string(nil), constraint.OpClasses...),
		ReferencesTable:   constraint.ReferencesTable,
		ReferencesColumns: append([]string(nil), constraint.ReferencesColumns...),
		OnDelete:          constraint.OnDelete,
		Deferrable:        constraint.Deferrable != "",
		InitiallyDeferred: constraint.Deferrable == models.DeferrableDeferred,
		Source:            "model",
	}
}

// ForeignKeyConstraintFromRelation returns the database constraint implied by a
// concrete column-backed relation field.
func ForeignKeyConstraintFromRelation(meta models.Metadata, field models.FieldMeta, registry *models.Registry) (ConstraintState, bool) {
	if !isColumnBackedRelation(field) {
		return ConstraintState{}, false
	}
	target, ok := relationTargetMetadata(meta, field.RelationTarget, registry)
	if !ok {
		return ConstraintState{}, false
	}
	targetColumn, ok := relationTargetColumn(target, field.TargetFieldName)
	if !ok {
		return ConstraintState{}, false
	}
	onDelete, ok := relationDeleteAction(field)
	if !ok {
		return ConstraintState{}, false
	}
	sourceColumn := fieldMetaColumnName(field)
	return ConstraintState{
		Name:              foreignKeyConstraintName(meta.TableName, sourceColumn),
		Type:              "foreign_key",
		Fields:            []string{sourceColumn},
		ReferencesTable:   target.TableName,
		ReferencesColumns: []string{targetColumn},
		OnDelete:          onDelete,
		Source:            "relation",
	}, true
}

func isColumnBackedRelation(field models.FieldMeta) bool {
	if strings.TrimSpace(field.RelationTarget) == "" || strings.EqualFold(field.Kind, "many_to_many") {
		return false
	}
	if strings.TrimSpace(field.Column) == "" && strings.TrimSpace(field.DeleteBehavior) == "" {
		return false
	}
	return true
}

func relationTargetColumn(target models.Metadata, targetFieldName string) (string, bool) {
	if targetFieldName != "" {
		for _, field := range target.Fields {
			if field.Name == targetFieldName || field.Column == targetFieldName {
				return fieldMetaColumnName(field), true
			}
		}
		return "", false
	}
	for _, field := range target.Fields {
		if field.PrimaryKey {
			return fieldMetaColumnName(field), true
		}
	}
	return "", false
}

func relationDeleteAction(field models.FieldMeta) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(field.DeleteBehavior)) {
	case "cascade":
		return "CASCADE", true
	case "restrict", "protect":
		return "RESTRICT", true
	case "set_null":
		if !field.Null {
			return "", false
		}
		return "SET NULL", true
	case "set_default":
		defaultValue, err := models.NormalizeDatabaseDefault(field.DBDefault)
		if err != nil || defaultValue.Kind == models.DefaultNone {
			return "", false
		}
		return "SET DEFAULT", true
	case "", "do_nothing", "set_value":
		return "NO ACTION", true
	default:
		return "", false
	}
}

func hasEquivalentForeignKeyConstraint(constraints []ConstraintState, foreignKey ConstraintState) bool {
	for _, existing := range constraints {
		if existing.Type != "foreign_key" {
			continue
		}
		if sameStringSlice(existing.Fields, foreignKey.Fields) &&
			strings.EqualFold(existing.ReferencesTable, foreignKey.ReferencesTable) &&
			sameStringSlice(existing.ReferencesColumns, foreignKey.ReferencesColumns) &&
			strings.EqualFold(existing.OnDelete, foreignKey.OnDelete) {
			return true
		}
	}
	return false
}

func sameStringSlice(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for i := range first {
		if !strings.EqualFold(first[i], second[i]) {
			return false
		}
	}
	return true
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

func fieldMetaColumnName(field models.FieldMeta) string {
	if field.Column != "" {
		return field.Column
	}
	return field.Name
}

func foreignKeyConstraintName(table, column string) string {
	return ForeignKeyConstraintName(table, column)
}

// ForeignKeyConstraintName returns the deterministic name used for relation
// owned foreign-key constraints.
func ForeignKeyConstraintName(table, column string) string {
	base := sanitizeSchemaName("fk_" + table + "_" + column)
	if base == "" {
		base = "fk"
	}
	if len(base) <= modelconstraints.MaxNameLength {
		return base
	}
	sum := sha1.Sum([]byte(table + "|foreign_key|" + column))
	hash := hex.EncodeToString(sum[:])[:8]
	suffix := "_" + hash
	limit := modelconstraints.MaxNameLength - len(suffix)
	if limit < 1 {
		limit = 1
	}
	return strings.Trim(base[:limit], "_") + suffix
}

func sanitizeSchemaName(value string) string {
	var builder strings.Builder
	lastUnderscore := false
	for _, char := range strings.ToLower(value) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(builder.String(), "_")
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
