package forms

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"unicode"

	"github.com/Newton-School/gogo/models"
	modelfields "github.com/Newton-School/gogo/models/fields"
)

// ModelFieldFactory overrides the generated form field for one model field.
type ModelFieldFactory func(modelfields.Field, FieldOptions) *Field

// ModelFormOptions configures a model-backed form.
type ModelFormOptions struct {
	Model           models.Model
	Meta            models.Metadata
	ModelFields     []modelfields.Field
	Include         []string
	Exclude         []string
	Labels          map[string]string
	HelpTexts       map[string]string
	Widgets         map[string]Widget
	FieldClasses    map[string]ModelFieldFactory
	LocalizedFields []string
	ReadOnly        []string
	Data            map[string]any
	Initial         map[string]any
	Prefix          string
	Groups          []FieldGroup
	Clean           FormCleanFunc
	Store           models.InstanceStore
	Context         context.Context
	SaveOptions     []models.SaveOption
}

// ModelForm binds a Form to a model instance.
type ModelForm struct {
	*Form

	Model       models.Model
	Meta        models.Metadata
	Store       models.InstanceStore
	Context     context.Context
	SaveOptions []models.SaveOption

	fieldNames    []string
	assigned      bool
	uniqueChecked bool
}

func NewModelForm(options ModelFormOptions) *ModelForm {
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	meta := options.Meta.Clone()
	if options.Model != nil && meta.ModelName == "" && meta.AppLabel == "" && len(meta.Fields) == 0 {
		meta = models.ResolveMetadata(options.Model)
	}

	modelFieldByName := modelFieldMap(options.ModelFields)
	modelFieldOrder := modelFieldsOrder(options.ModelFields)
	metaFieldByName := metadataFieldMap(meta.Fields)
	fieldNames := selectModelFormFields(meta.Fields, modelFieldOrder, modelFieldByName, options.Include, options.Exclude)

	readonly := stringSet(options.ReadOnly)
	localized := stringSet(options.LocalizedFields)
	initial := cloneAnyMap(options.Initial)
	if initial == nil {
		initial = map[string]any{}
	}
	formFields := make(map[string]*Field, len(fieldNames))
	for _, name := range fieldNames {
		if _, ok := initial[name]; !ok {
			if value, ok := modelInitialValue(options.Model, name, metaFieldByName[name], modelFieldByName[name]); ok {
				initial[name] = value
			}
		}
		formField := buildModelFormField(name, metaFieldByName[name], modelFieldByName[name], initial[name], options, readonly[name], localized[name])
		if formField != nil {
			formFields[name] = formField
		}
	}

	base := NewForm(FormOptions{
		Fields:     formFields,
		FieldOrder: fieldNames,
		Data:       options.Data,
		Initial:    initial,
		Prefix:     options.Prefix,
		Groups:     options.Groups,
		Clean:      options.Clean,
	})
	return &ModelForm{
		Form:        base,
		Model:       options.Model,
		Meta:        meta,
		Store:       options.Store,
		Context:     ctx,
		SaveOptions: append([]models.SaveOption(nil), options.SaveOptions...),
		fieldNames:  fieldNames,
	}
}

func (mf *ModelForm) IsValid() bool {
	if mf == nil || mf.Form == nil || !mf.Form.IsValid() {
		return false
	}
	if !mf.assigned {
		if err := mf.assignCleanedData(); err != nil {
			mf.AddNonFieldError(err)
			return false
		}
		mf.assigned = true
	}
	if !mf.uniqueChecked && mf.Model != nil {
		mf.uniqueChecked = true
		if err := models.ValidateUnique(mf.Context, mf.Model); err != nil {
			mf.AddNonFieldError(err)
			return false
		}
	}
	return !mf.Errors().HasErrors() && len(mf.NonFieldErrors()) == 0
}

func (mf *ModelForm) Save(commit bool, options ...models.SaveOption) (models.Model, error) {
	if mf == nil {
		return nil, fmt.Errorf("%w: model form is nil", ErrValidation)
	}
	if !mf.IsValid() {
		return mf.Model, fmt.Errorf("%w: invalid model form", ErrValidation)
	}
	if !commit {
		return mf.Model, nil
	}
	saveOptions := append([]models.SaveOption(nil), mf.SaveOptions...)
	saveOptions = append(saveOptions, options...)
	if err := models.Save(mf.Context, mf.Model, mf.Store, saveOptions...); err != nil {
		return mf.Model, err
	}
	return mf.Model, nil
}

func (mf *ModelForm) assignCleanedData() error {
	if mf.Model == nil {
		return nil
	}
	for _, name := range mf.fieldNames {
		value, ok := mf.CleanedData[name]
		if !ok {
			continue
		}
		if err := setModelValue(mf.Model, name, value); err != nil {
			return err
		}
	}
	return nil
}

func buildModelFormField(name string, metaField models.FieldMeta, modelField modelfields.Field, initial any, options ModelFormOptions, readonly, localized bool) *Field {
	fieldOptions := modelFormFieldOptions(name, metaField, modelField, initial, options, readonly, localized)
	if factory := options.FieldClasses[name]; factory != nil {
		return factory(modelField, fieldOptions)
	}

	choices := modelFormChoices(metaField, modelField)
	if len(choices) > 0 {
		return ChoiceField(fieldOptions, choices)
	}

	kind := modelFieldKind(metaField, modelField)
	switch kind {
	case "boolean":
		return BooleanField(fieldOptions)
	case "email":
		return EmailField(fieldOptions)
	case "url":
		return URLField(fieldOptions)
	case "slug":
		return SlugField(fieldOptions)
	case "uuid":
		return UUIDField(fieldOptions)
	case "integer", "big_integer", "small_integer", "positive_integer", "positive_big_integer", "positive_small_integer", "auto", "big_auto", "small_auto":
		return IntegerField(fieldOptions)
	case "float":
		return FloatField(fieldOptions)
	case "decimal":
		return DecimalField(fieldOptions)
	case "date":
		return DateField(fieldOptions)
	case "datetime":
		return DateTimeField(fieldOptions)
	case "time":
		return TimeField(fieldOptions)
	case "duration":
		return DurationField(fieldOptions)
	case "file", "filepath":
		return FileField(fieldOptions)
	case "image":
		return ImageField(fieldOptions)
	case "ip_address":
		return GenericIPAddressField(fieldOptions)
	case "json", "hstore", "array":
		return JSONField(fieldOptions)
	case "foreign_key", "one_to_one":
		return ModelChoiceField(fieldOptions, choices)
	case "many_to_many":
		return ModelMultipleChoiceField(fieldOptions, choices)
	default:
		return CharField(fieldOptions)
	}
}

func modelFormFieldOptions(name string, metaField models.FieldMeta, modelField modelfields.Field, initial any, options ModelFormOptions, readonly, localized bool) FieldOptions {
	fieldOptions := FieldOptions{
		Required: modelFormFieldRequired(metaField, modelField),
		Label:    modelFormLabel(name, metaField, modelField, options.Labels),
		Initial:  initial,
		HelpText: modelFormHelpText(name, metaField, modelField, options.HelpTexts),
		Disabled: readonly || !modelFormFieldEditable(metaField, modelField),
		Localize: localized,
	}
	if modelField != nil {
		modelOptions := modelField.Options()
		fieldOptions.ErrorMessages = cloneStringMap(modelOptions.ErrorMessages)
		for _, validator := range modelOptions.Validators {
			fieldOptions.Validators = append(fieldOptions.Validators, Validator(validator))
		}
	}
	if widget := options.Widgets[name]; widget != nil {
		fieldOptions.Widget = widget
	}
	return fieldOptions
}

func selectModelFormFields(metaFields []models.FieldMeta, modelFieldOrder []string, modelFieldByName map[string]modelfields.Field, include, exclude []string) []string {
	includeSet := stringSet(include)
	excludeSet := stringSet(exclude)
	var base []string
	if len(include) > 0 {
		base = append([]string(nil), include...)
	} else if len(metaFields) > 0 {
		for _, field := range metaFields {
			base = append(base, field.Name)
		}
		seen := stringSet(base)
		for _, name := range modelFieldOrder {
			if !seen[name] {
				base = append(base, name)
			}
		}
	} else {
		base = append([]string(nil), modelFieldOrder...)
	}

	selected := make([]string, 0, len(base))
	seen := map[string]bool{}
	metaFieldByName := metadataFieldMap(metaFields)
	for _, name := range base {
		if name == "" || seen[name] || excludeSet[name] {
			continue
		}
		modelField := modelFieldByName[name]
		metaField := metaFieldByName[name]
		if len(includeSet) == 0 {
			if metaField.PrimaryKey || !metadataFieldEditable(metaField) || modelFieldPrimaryKey(modelField) || !modelFieldEditable(modelField) {
				continue
			}
		}
		selected = append(selected, name)
		seen[name] = true
	}
	return selected
}

func modelFieldKind(metaField models.FieldMeta, modelField modelfields.Field) string {
	if modelField != nil {
		return modelField.Kind()
	}
	if metaField.RelationTarget != "" {
		if metaField.RelationType != "" {
			return strings.ToLower(metaField.RelationType)
		}
		return "foreign_key"
	}
	if metaField.Kind != "" {
		return strings.ToLower(metaField.Kind)
	}
	return "char"
}

func modelFormFieldRequired(metaField models.FieldMeta, modelField modelfields.Field) bool {
	if modelField != nil {
		return !modelFieldAllowsBlank(modelField)
	}
	if metaField.PrimaryKey || metaField.Null || metaField.Blank {
		return false
	}
	if metaField.Default != nil {
		return false
	}
	defaultValue, err := models.NormalizeDatabaseDefault(metaField.DBDefault)
	return err != nil || defaultValue.Kind == models.DefaultNone
}

func modelFormChoices(metaField models.FieldMeta, modelField modelfields.Field) []Choice {
	if modelField == nil {
		return modelFormMetadataChoices(metaField.Choices)
	}
	modelOptions := modelField.Options()
	choices := make([]Choice, len(modelOptions.Choices))
	for i, choice := range modelOptions.Choices {
		choices[i] = Choice{Value: choice.Value, Label: choice.Label}
	}
	if len(choices) > 0 {
		return choices
	}
	return modelFormMetadataChoices(metaField.Choices)
}

func modelFormMetadataChoices(choices []models.FieldChoiceMeta) []Choice {
	if len(choices) == 0 {
		return nil
	}
	copied := make([]Choice, len(choices))
	for i, choice := range choices {
		copied[i] = Choice{Value: choice.Value, Label: choice.Label}
	}
	return copied
}

func modelFieldAllowsBlank(modelField modelfields.Field) bool {
	if modelField == nil {
		return false
	}
	options := modelField.Options()
	return options.Blank || options.Null
}

func modelFieldEditable(modelField modelfields.Field) bool {
	if modelField == nil {
		return true
	}
	options := modelField.Options()
	if options.Editable == nil {
		return true
	}
	return *options.Editable
}

func modelFormFieldEditable(metaField models.FieldMeta, modelField modelfields.Field) bool {
	return metadataFieldEditable(metaField) && modelFieldEditable(modelField)
}

func metadataFieldEditable(metaField models.FieldMeta) bool {
	if metaField.Editable == nil {
		return true
	}
	return *metaField.Editable
}

func modelFieldPrimaryKey(modelField modelfields.Field) bool {
	if modelField == nil {
		return false
	}
	return modelField.Options().PrimaryKey
}

func modelFormLabel(name string, metaField models.FieldMeta, modelField modelfields.Field, overrides map[string]string) string {
	if label := overrides[name]; label != "" {
		return label
	}
	if modelField != nil {
		if label := modelField.Options().VerboseName; label != "" {
			return label
		}
	}
	if metaField.VerboseName != "" {
		return metaField.VerboseName
	}
	return strings.ReplaceAll(name, "_", " ")
}

func modelFormHelpText(name string, metaField models.FieldMeta, modelField modelfields.Field, overrides map[string]string) string {
	if help := overrides[name]; help != "" {
		return help
	}
	if modelField != nil {
		return modelField.Options().HelpText
	}
	return metaField.HelpText
}

func modelInitialValue(model models.Model, name string, metaField models.FieldMeta, modelField modelfields.Field) (any, bool) {
	if model != nil {
		if value, ok := models.SerializableValue(model, name); ok {
			return value, true
		}
		if getter, ok := model.(interface{ ModelValue(string) (any, bool) }); ok {
			if value, ok := getter.ModelValue(name); ok {
				return value, true
			}
		}
		if value, ok := reflectedModelValue(model, name); ok {
			return value, true
		}
	}
	if modelField != nil {
		if value := modelField.Options().Default; value != nil {
			return value, true
		}
	}
	if metaField.Default != nil {
		return metaField.Default, true
	}
	return nil, false
}

func reflectedModelValue(model models.Model, name string) (any, bool) {
	value := reflect.ValueOf(model)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil, false
	}
	for _, fieldName := range modelStructFieldNames(name) {
		field := value.FieldByName(fieldName)
		if field.IsValid() && field.CanInterface() {
			return field.Interface(), true
		}
	}
	return nil, false
}

func setModelValue(model models.Model, name string, value any) error {
	if setter, ok := model.(interface{ SetModelValue(string, any) error }); ok {
		return setter.SetModelValue(name, value)
	}
	target := reflect.ValueOf(model)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("%w: model must be a non-nil pointer to save field %s", ErrValidation, name)
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct {
		return fmt.Errorf("%w: model must point to a struct to save field %s", ErrValidation, name)
	}
	for _, fieldName := range modelStructFieldNames(name) {
		field := target.FieldByName(fieldName)
		if field.IsValid() {
			if !field.CanSet() {
				return fmt.Errorf("%w: model field %s is not settable", ErrValidation, name)
			}
			return setReflectValue(field, value, name)
		}
	}
	return fmt.Errorf("%w: model field %s was not found", ErrValidation, name)
}

func setReflectValue(field reflect.Value, value any, name string) error {
	if value == nil {
		field.Set(reflect.Zero(field.Type()))
		return nil
	}
	source := reflect.ValueOf(value)
	if source.Type().AssignableTo(field.Type()) {
		field.Set(source)
		return nil
	}
	if source.Type().ConvertibleTo(field.Type()) {
		field.Set(source.Convert(field.Type()))
		return nil
	}
	if field.Kind() == reflect.Interface && source.Type().AssignableTo(field.Type()) {
		field.Set(source)
		return nil
	}
	return fmt.Errorf("%w: cleaned value for %s cannot be assigned to %s", ErrValidation, name, field.Type())
}

func modelFieldMap(fields []modelfields.Field) map[string]modelfields.Field {
	mapped := make(map[string]modelfields.Field, len(fields))
	for _, field := range fields {
		if field != nil {
			mapped[field.Name()] = field
		}
	}
	return mapped
}

func modelFieldsOrder(fields []modelfields.Field) []string {
	order := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != nil {
			order = append(order, field.Name())
		}
	}
	return order
}

func metadataFieldMap(fields []models.FieldMeta) map[string]models.FieldMeta {
	mapped := make(map[string]models.FieldMeta, len(fields))
	for _, field := range fields {
		mapped[field.Name] = field
	}
	return mapped
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func modelStructFieldNames(name string) []string {
	camel := camelIdentifier(name)
	if camel == name {
		return []string{name}
	}
	return []string{camel, name}
}

func camelIdentifier(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '_' || r == '-' || unicode.IsSpace(r)
	})
	var builder strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		lower := strings.ToLower(part)
		if initialism, ok := goInitialisms[lower]; ok {
			builder.WriteString(initialism)
			continue
		}
		runes := []rune(lower)
		runes[0] = unicode.ToUpper(runes[0])
		builder.WriteString(string(runes))
	}
	return builder.String()
}

var goInitialisms = map[string]string{
	"id":   "ID",
	"url":  "URL",
	"uuid": "UUID",
	"ip":   "IP",
	"api":  "API",
}
