package api

import (
	"fmt"

	"github.com/Newton-School/gogo/models"
)

// ModelSerializerConfig configures metadata-driven serializers.
type ModelSerializerConfig struct {
	Model             models.Metadata
	Fields            []string
	Exclude           []string
	ReadOnlyFields    []string
	ExtraKwargs       map[string]FieldOptions
	Depth             int
	NestedSerializers map[string]*Serializer
	CreateFunc        func(map[string]any) (map[string]any, error)
	UpdateFunc        func(map[string]any, map[string]any) (map[string]any, error)
}

// ModelSerializer maps model metadata to serializer fields.
type ModelSerializer struct {
	config     ModelSerializerConfig
	serializer *Serializer
	fieldNames []string
}

// NewModelSerializer builds a serializer from model metadata.
func NewModelSerializer(config ModelSerializerConfig) (*ModelSerializer, error) {
	fields, names, err := modelSerializerFields(config)
	if err != nil {
		return nil, err
	}
	return &ModelSerializer{config: cloneModelSerializerConfig(config), serializer: NewSerializer(fields...), fieldNames: names}, nil
}

// FieldNames returns serializer field order.
func (s *ModelSerializer) FieldNames() []string {
	return append([]string(nil), s.fieldNames...)
}

// Validate validates model serializer input.
func (s *ModelSerializer) Validate(input map[string]any) (map[string]any, map[string][]string, bool) {
	return s.serializer.Validate(input)
}

// Render renders a model instance map.
func (s *ModelSerializer) Render(instance map[string]any) map[string]any {
	rendered := s.serializer.Render(instance)
	if s.config.Depth > 0 {
		for field, serializer := range s.config.NestedSerializers {
			if nested, ok := instance[field].(map[string]any); ok {
				rendered[field] = serializer.Render(nested)
			}
		}
	}
	return rendered
}

// Create creates an object from validated data.
func (s *ModelSerializer) Create(data map[string]any) (map[string]any, error) {
	if s.config.CreateFunc != nil {
		return s.config.CreateFunc(cloneAnyMap(data))
	}
	return cloneAnyMap(data), nil
}

// Update updates an object from validated data.
func (s *ModelSerializer) Update(instance map[string]any, data map[string]any) (map[string]any, error) {
	if s.config.UpdateFunc != nil {
		return s.config.UpdateFunc(cloneAnyMap(instance), cloneAnyMap(data))
	}
	updated := cloneAnyMap(instance)
	for key, value := range data {
		updated[key] = value
	}
	return updated, nil
}

func modelSerializerFields(config ModelSerializerConfig) ([]SerializerField, []string, error) {
	available := map[string]models.FieldMeta{}
	order := make([]string, 0, len(config.Model.Fields))
	for _, field := range config.Model.Fields {
		available[field.Name] = field
		order = append(order, field.Name)
	}
	selected := config.Fields
	if len(selected) == 0 {
		selected = order
	}
	excluded := stringSet(config.Exclude)
	readonly := stringSet(config.ReadOnlyFields)
	fields := make([]SerializerField, 0, len(selected))
	names := make([]string, 0, len(selected))
	for _, name := range selected {
		meta, ok := available[name]
		if !ok {
			return nil, nil, fmt.Errorf("%w: unknown field %s", ErrInvalidSerializerConfig, name)
		}
		if _, skip := excluded[name]; skip {
			continue
		}
		options := config.ExtraKwargs[name]
		if _, ok := readonly[name]; ok || meta.PrimaryKey {
			options.ReadOnly = true
		}
		field := serializerFieldFromModel(meta, options)
		fields = append(fields, field)
		names = append(names, name)
	}
	return fields, names, nil
}

func serializerFieldFromModel(meta models.FieldMeta, options FieldOptions) SerializerField {
	if meta.RelationTarget != "" {
		return PrimaryKeyRelatedField(meta.Name, options)
	}
	if meta.PrimaryKey {
		return IntegerField(meta.Name, options)
	}
	return StringField(meta.Name, options)
}

func cloneModelSerializerConfig(config ModelSerializerConfig) ModelSerializerConfig {
	config.Model = config.Model.Clone()
	config.Fields = append([]string(nil), config.Fields...)
	config.Exclude = append([]string(nil), config.Exclude...)
	config.ReadOnlyFields = append([]string(nil), config.ReadOnlyFields...)
	config.ExtraKwargs = cloneFieldOptionsMap(config.ExtraKwargs)
	config.NestedSerializers = cloneSerializerMap(config.NestedSerializers)
	return config
}

func cloneFieldOptionsMap(values map[string]FieldOptions) map[string]FieldOptions {
	if values == nil {
		return nil
	}
	copied := make(map[string]FieldOptions, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func cloneSerializerMap(values map[string]*Serializer) map[string]*Serializer {
	if values == nil {
		return nil
	}
	copied := make(map[string]*Serializer, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func cloneAnyMap(values map[string]any) map[string]any {
	copied := make(map[string]any, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}
