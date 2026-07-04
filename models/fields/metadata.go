package fields

import "github.com/cybersaksham/gogo/models"

// Metadata converts a concrete field into model metadata for migrations, admin,
// API, and ORM stores.
func Metadata(field Field, dialect string) models.FieldMeta {
	if field == nil {
		return models.FieldMeta{}
	}
	options := field.Options()
	meta := models.FieldMeta{
		Name:        field.Name(),
		Column:      field.ColumnName(),
		Kind:        field.ColumnType(dialect),
		PrimaryKey:  options.PrimaryKey,
		Null:        options.Null,
		Unique:      options.Unique,
		DBIndex:     options.DBIndex,
		DBDefault:   options.DBDefault,
		DBCollation: options.DBCollation,
	}
	if base, ok := field.(*BaseField); ok {
		meta.ColumnTypes = cloneStringMap(base.columnTypes)
	}
	if custom, ok := field.(*CustomField); ok {
		meta.ColumnTypes = cloneStringMap(custom.config.ColumnTypes)
	}
	if relation, ok := field.(*RelationField); ok {
		meta.RelationTarget = relation.Target()
		meta.TargetFieldName = relation.TargetFieldName()
		meta.DeleteBehavior = string(relation.OnDelete())
		meta.ColumnTypes = cloneStringMap(relation.BaseField.columnTypes)
	}
	return meta
}
