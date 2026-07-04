package fields

import "fmt"

// CustomConfig configures a custom model field for extension or domain-specific
// database types.
type CustomConfig struct {
	Kind           string
	ColumnTypes    map[string]string
	RequireDialect []string
	ValidateFunc   func(any) error
	ToDBFunc       func(any) (any, error)
	FromDBFunc     func(any) (any, error)
}

// CustomField is a public escape hatch for database-specific or domain-specific
// field types while preserving the common Field contract.
type CustomField struct {
	*BaseField
	config CustomConfig
}

// NewCustomField creates a custom field with optional dialect column types and
// conversion hooks.
func NewCustomField(options Options, config CustomConfig) *CustomField {
	if config.Kind == "" {
		config.Kind = "custom"
	}
	return &CustomField{
		BaseField: NewBaseField(config.Kind, options, config.ColumnTypes),
		config:    cloneCustomConfig(config),
	}
}

// Config returns a copy of the custom field configuration.
func (f *CustomField) Config() CustomConfig {
	if f == nil {
		return CustomConfig{}
	}
	return cloneCustomConfig(f.config)
}

// Validate validates common field options and then the custom validator.
func (f *CustomField) Validate(value any) error {
	if err := f.BaseField.Validate(value); err != nil {
		return err
	}
	if f.config.ValidateFunc == nil {
		return nil
	}
	if err := f.config.ValidateFunc(value); err != nil {
		return fmt.Errorf("%w: %v", ErrValidation, err)
	}
	return nil
}

// ToDB converts a value for database storage.
func (f *CustomField) ToDB(value any) (any, error) {
	if f.config.ToDBFunc == nil {
		return f.BaseField.ToDB(value)
	}
	return f.config.ToDBFunc(value)
}

// FromDB converts a database value for model use.
func (f *CustomField) FromDB(value any) (any, error) {
	if f.config.FromDBFunc == nil {
		return f.BaseField.FromDB(value)
	}
	return f.config.FromDBFunc(value)
}

// Clone returns an independent custom field copy.
func (f *CustomField) Clone() Field {
	return &CustomField{
		BaseField: f.BaseField.Clone().(*BaseField),
		config:    cloneCustomConfig(f.config),
	}
}

func cloneCustomConfig(config CustomConfig) CustomConfig {
	copied := config
	copied.ColumnTypes = cloneStringMap(config.ColumnTypes)
	copied.RequireDialect = append([]string(nil), config.RequireDialect...)
	return copied
}
