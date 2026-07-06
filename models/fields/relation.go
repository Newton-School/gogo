package fields

import (
	"fmt"

	"github.com/Newton-School/gogo/models"
)

const Self = "self"

type RelationType string

const (
	RelationForeignKey RelationType = "foreign_key"
	RelationOneToOne   RelationType = "one_to_one"
	RelationManyToMany RelationType = "many_to_many"
)

type DeleteBehavior string

const (
	Cascade    DeleteBehavior = "cascade"
	Protect    DeleteBehavior = "protect"
	Restrict   DeleteBehavior = "restrict"
	SetNull    DeleteBehavior = "set_null"
	SetDefault DeleteBehavior = "set_default"
	SetValue   DeleteBehavior = "set_value"
	DoNothing  DeleteBehavior = "do_nothing"
)

// RelationConfig configures relationship fields.
type RelationConfig struct {
	Target           string
	Through          string
	RelatedName      string
	RelatedQueryName string
	OnDelete         DeleteBehavior
	SetValue         any
	ColumnTypes      map[string]string
	TargetFieldName  string
}

// ReverseRelation describes generated reverse relation metadata.
type ReverseRelation struct {
	SourceField      string
	Target           string
	RelatedName      string
	RelatedQueryName string
	Type             RelationType
}

// RelationField describes Django-style relationship field metadata.
type RelationField struct {
	*BaseField
	relationType RelationType
	config       RelationConfig
}

// NewForeignKey creates a foreign key field.
func NewForeignKey(options Options, config RelationConfig) *RelationField {
	columnTypes := config.ColumnTypes
	if len(columnTypes) == 0 {
		columnTypes = map[string]string{"postgres": "bigint", "sqlite": "integer"}
	}
	return newRelationField(RelationForeignKey, options, config, columnTypes)
}

// NewOneToOneField creates a one-to-one field.
func NewOneToOneField(options Options, config RelationConfig) *RelationField {
	options.Unique = true
	columnTypes := config.ColumnTypes
	if len(columnTypes) == 0 {
		columnTypes = map[string]string{"postgres": "bigint", "sqlite": "integer"}
	}
	return newRelationField(RelationOneToOne, options, config, columnTypes)
}

// NewManyToManyField creates a many-to-many field.
func NewManyToManyField(options Options, config RelationConfig) *RelationField {
	columnTypes := config.ColumnTypes
	if len(columnTypes) == 0 {
		columnTypes = map[string]string{"postgres": "many_to_many", "sqlite": "many_to_many"}
	}
	return newRelationField(RelationManyToMany, options, config, columnTypes)
}

func newRelationField(relationType RelationType, options Options, config RelationConfig, columnTypes map[string]string) *RelationField {
	if config.OnDelete == "" {
		config.OnDelete = Cascade
	}
	return &RelationField{
		BaseField:    NewBaseField(string(relationType), options, columnTypes),
		relationType: relationType,
		config:       cloneRelationConfig(config),
	}
}

func (f *RelationField) RelationType() RelationType {
	return f.relationType
}

func (f *RelationField) Target() string {
	return f.config.Target
}

func (f *RelationField) Through() string {
	return f.config.Through
}

func (f *RelationField) RelatedName() string {
	return f.config.RelatedName
}

func (f *RelationField) RelatedQueryName() string {
	return f.config.RelatedQueryName
}

func (f *RelationField) OnDelete() DeleteBehavior {
	return f.config.OnDelete
}

func (f *RelationField) TargetFieldName() string {
	return f.config.TargetFieldName
}

func (f *RelationField) IsSelfReference() bool {
	return f.config.Target == Self
}

func (f *RelationField) ReverseRelation() ReverseRelation {
	return ReverseRelation{
		SourceField:      f.Name(),
		Target:           f.Target(),
		RelatedName:      f.RelatedName(),
		RelatedQueryName: f.RelatedQueryName(),
		Type:             f.RelationType(),
	}
}

func (f *RelationField) Clone() Field {
	return &RelationField{
		BaseField:    f.BaseField.Clone().(*BaseField),
		relationType: f.relationType,
		config:       cloneRelationConfig(f.config),
	}
}

func cloneRelationConfig(config RelationConfig) RelationConfig {
	copied := config
	copied.ColumnTypes = cloneStringMap(config.ColumnTypes)
	return copied
}

// ModelLookup resolves model metadata by app_label.ModelName.
type ModelLookup interface {
	Lookup(string) (models.Metadata, bool)
}

// ValidateRelations validates relationship metadata against registered models.
func ValidateRelations(registry ModelLookup, relations ...*RelationField) error {
	reverseNames := map[string]string{}
	for _, relation := range relations {
		if relation == nil {
			continue
		}
		if err := validateRelationTarget(registry, relation); err != nil {
			return err
		}
		if err := validateDeleteBehavior(relation); err != nil {
			return err
		}
		if relation.RelatedName() != "" && relation.RelatedName() != "+" {
			key := relation.Target() + ":" + relation.RelatedName()
			if existing, ok := reverseNames[key]; ok {
				return fmt.Errorf("%w: duplicate reverse name %q on %s and %s", ErrInvalidField, relation.RelatedName(), existing, relation.Name())
			}
			reverseNames[key] = relation.Name()
		}
	}
	return nil
}

func validateRelationTarget(registry ModelLookup, relation *RelationField) error {
	if relation.Target() == "" {
		return fmt.Errorf("%w: relation %s missing target", ErrInvalidField, relation.Name())
	}
	if relation.Target() != Self {
		if registry == nil {
			return fmt.Errorf("%w: registry is required for relation validation", ErrInvalidField)
		}
		if _, ok := registry.Lookup(relation.Target()); !ok {
			return fmt.Errorf("%w: target %s is not registered", ErrInvalidField, relation.Target())
		}
	}
	if relation.RelationType() == RelationManyToMany && relation.Through() != "" {
		if registry == nil {
			return fmt.Errorf("%w: registry is required for through model validation", ErrInvalidField)
		}
		if _, ok := registry.Lookup(relation.Through()); !ok {
			return fmt.Errorf("%w: through model %s is not registered", ErrInvalidField, relation.Through())
		}
	}
	return nil
}

func validateDeleteBehavior(relation *RelationField) error {
	switch relation.OnDelete() {
	case Cascade, Protect, Restrict, DoNothing:
		return nil
	case SetNull:
		if !relation.options.Null {
			return fmt.Errorf("%w: SET_NULL requires nullable field", ErrInvalidField)
		}
	case SetDefault:
		if relation.options.Default == nil {
			return fmt.Errorf("%w: SET_DEFAULT requires default value", ErrInvalidField)
		}
	case SetValue:
		if relation.config.SetValue == nil {
			return fmt.Errorf("%w: SET_VALUE requires value", ErrInvalidField)
		}
	default:
		return fmt.Errorf("%w: unsupported delete behavior %q", ErrInvalidField, relation.OnDelete())
	}
	return nil
}
