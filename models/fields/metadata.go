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
		Blank:       options.Blank,
		Default:     options.Default,
		HelpText:    options.HelpText,
		VerboseName: options.VerboseName,
		Editable:    cloneBoolPointer(options.Editable),
		Choices:     choiceMetadata(options.Choices),
		Validators:  validatorMetadata(options.Validators),
	}
	if base, ok := field.(*BaseField); ok {
		meta.ColumnTypes = cloneStringMap(base.columnTypes)
	}
	if text, ok := field.(*stringField); ok {
		meta.MaxLength = text.maxLength
	}
	if decimal, ok := field.(*DecimalField); ok {
		meta.MaxDigits = decimal.maxDigits
		meta.DecimalPlaces = decimal.decimalPlaces
	}
	if file, ok := field.(*FileField); ok {
		meta.UploadTo = file.config.UploadTo
	}
	if image, ok := field.(*ImageField); ok {
		meta.UploadTo = image.config.UploadTo
	}
	if custom, ok := field.(*CustomField); ok {
		meta.ColumnTypes = cloneStringMap(custom.config.ColumnTypes)
	}
	if relation, ok := field.(*RelationField); ok {
		meta.RelationTarget = relation.Target()
		meta.RelationType = string(relation.RelationType())
		meta.ThroughModel = relation.Through()
		meta.ReverseName = relation.RelatedName()
		meta.TargetFieldName = relation.TargetFieldName()
		meta.DeleteBehavior = string(relation.OnDelete())
		meta.ColumnTypes = cloneStringMap(relation.BaseField.columnTypes)
	}
	return meta
}

func choiceMetadata(choices []Choice) []models.FieldChoiceMeta {
	if len(choices) == 0 {
		return nil
	}
	copied := make([]models.FieldChoiceMeta, len(choices))
	for i, choice := range choices {
		copied[i] = models.FieldChoiceMeta{Value: choice.Value, Label: choice.Label, Group: choice.Group}
	}
	return copied
}

func validatorMetadata(validators []Validator) []models.FieldValidator {
	if len(validators) == 0 {
		return nil
	}
	copied := make([]models.FieldValidator, len(validators))
	for i, validator := range validators {
		copied[i] = models.FieldValidator(validator)
	}
	return copied
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
