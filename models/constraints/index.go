package constraints

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidIndex = errors.New("invalid index metadata")

// IndexField describes one ordered field inside an index or unique constraint.
type IndexField struct {
	Name       string
	Descending bool
	NullsFirst bool
	NullsLast  bool
	OpClass    string
}

// Asc creates an ascending indexed field.
func Asc(name string) IndexField {
	return IndexField{Name: name}
}

// Desc creates a descending indexed field.
func Desc(name string) IndexField {
	return IndexField{Name: name, Descending: true}
}

// WithNullsFirst returns a field ordered with NULL values first.
func (f IndexField) WithNullsFirst() IndexField {
	f.NullsFirst = true
	return f
}

// WithNullsLast returns a field ordered with NULL values last.
func (f IndexField) WithNullsLast() IndexField {
	f.NullsLast = true
	return f
}

// WithOpClass returns a field using a database operator class.
func (f IndexField) WithOpClass(opClass string) IndexField {
	f.OpClass = opClass
	return f
}

func (f IndexField) validate() error {
	if strings.TrimSpace(f.Name) == "" {
		return fmt.Errorf("%w: index field name is required", ErrInvalidIndex)
	}
	if f.NullsFirst && f.NullsLast {
		return fmt.Errorf("%w: nulls first and nulls last are mutually exclusive", ErrInvalidIndex)
	}
	return nil
}

func (f IndexField) namePart() string {
	if f.Descending {
		return f.Name + "_desc"
	}
	return f.Name
}

// Index stores migration-ready SQL index metadata.
type Index struct {
	Name        string
	Fields      []IndexField
	Unique      bool
	Expressions []string
	Condition   string
	Include     []string
	OpClasses   []string
	Tablespace  string
	Method      string
}

// NewIndex creates index metadata.
func NewIndex(name string, fields ...IndexField) Index {
	return Index{Name: name, Fields: append([]IndexField(nil), fields...)}
}

// NewUniqueIndex creates unique index metadata.
func NewUniqueIndex(name string, fields ...IndexField) Index {
	return NewIndex(name, fields...).WithUnique()
}

// WithUnique marks the index as unique.
func (i Index) WithUnique() Index {
	i.Unique = true
	return i
}

// WithExpressions adds functional index expressions.
func (i Index) WithExpressions(expressions ...string) Index {
	i.Expressions = append(i.Expressions, expressions...)
	return i
}

// WithCondition adds a partial index condition.
func (i Index) WithCondition(condition string) Index {
	i.Condition = condition
	return i
}

// WithInclude adds covering index columns.
func (i Index) WithInclude(fields ...string) Index {
	i.Include = append(i.Include, fields...)
	return i
}

// WithOperatorClasses adds operator class metadata.
func (i Index) WithOperatorClasses(opClasses ...string) Index {
	i.OpClasses = append(i.OpClasses, opClasses...)
	return i
}

// WithTablespace adds database tablespace metadata.
func (i Index) WithTablespace(tablespace string) Index {
	i.Tablespace = tablespace
	return i
}

// WithMethod adds backend-specific index method metadata.
func (i Index) WithMethod(method string) Index {
	i.Method = method
	return i
}

// FieldNames returns the ordered field names.
func (i Index) FieldNames() []string {
	names := make([]string, len(i.Fields))
	for idx, field := range i.Fields {
		names[idx] = field.Name
	}
	return names
}

// Clone returns a deep copy of index metadata.
func (i Index) Clone() Index {
	copied := i
	copied.Fields = append([]IndexField(nil), i.Fields...)
	copied.Expressions = append([]string(nil), i.Expressions...)
	copied.Include = append([]string(nil), i.Include...)
	copied.OpClasses = append([]string(nil), i.OpClasses...)
	return copied
}

// NameFor returns the explicit name or a deterministic generated name.
func (i Index) NameFor(table string) string {
	if i.Name != "" {
		return i.Name
	}
	prefix := "idx"
	if i.Unique {
		prefix = "uniq"
	}
	return deterministicName(table, prefix, i.nameParts()...)
}

// Validate verifies index metadata is usable by migration generation.
func (i Index) Validate() error {
	if len(i.Fields) == 0 && len(i.Expressions) == 0 {
		return fmt.Errorf("%w: fields or expressions are required", ErrInvalidIndex)
	}
	for _, field := range i.Fields {
		if err := field.validate(); err != nil {
			return err
		}
	}
	if err := validateNonEmpty("expression", i.Expressions, ErrInvalidIndex); err != nil {
		return err
	}
	if err := validateNonEmpty("include field", i.Include, ErrInvalidIndex); err != nil {
		return err
	}
	if err := validateNonEmpty("operator class", i.OpClasses, ErrInvalidIndex); err != nil {
		return err
	}
	if len(i.OpClasses) > len(i.Fields)+len(i.Expressions) {
		return fmt.Errorf("%w: operator classes cannot outnumber indexed terms", ErrInvalidIndex)
	}
	return nil
}

func (i Index) nameParts() []string {
	parts := make([]string, 0, len(i.Fields)+len(i.Expressions)+len(i.Include)+1)
	for _, field := range i.Fields {
		parts = append(parts, field.namePart())
	}
	parts = append(parts, i.Expressions...)
	if i.Condition != "" {
		parts = append(parts, i.Condition)
	}
	parts = append(parts, i.Include...)
	return parts
}

func validateNonEmpty(label string, values []string, sentinel error) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s cannot be empty", sentinel, label)
		}
	}
	return nil
}
