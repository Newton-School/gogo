package constraints

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidConstraint = errors.New("invalid constraint metadata")

// Type identifies a database constraint family.
type Type string

const (
	TypeUnique    Type = "unique"
	TypeCheck     Type = "check"
	TypeExclusion Type = "exclusion"
)

// Deferrable stores deferrable constraint timing.
type Deferrable string

const (
	DeferrableImmediate Deferrable = "immediate"
	DeferrableDeferred  Deferrable = "deferred"
)

// Exclusion describes one exclusion constraint expression/operator pair.
type Exclusion struct {
	Expression string
	Operator   string
	OpClass    string
}

// Constraint stores migration-ready SQL constraint metadata.
type Constraint struct {
	Name             string
	Type             Type
	Fields           []IndexField
	Expressions      []string
	Check            string
	Exclusions       []Exclusion
	Condition        string
	Deferrable       Deferrable
	NullsDistinct    *bool
	ViolationCode    string
	ViolationMessage string
	Include          []string
	OpClasses        []string
}

// Unique creates unique constraint metadata over fields.
func Unique(name string, fields ...string) Constraint {
	constraint := Constraint{Name: name, Type: TypeUnique}
	for _, field := range fields {
		constraint.Fields = append(constraint.Fields, Asc(field))
	}
	return constraint
}

// UniqueExpression creates a functional unique constraint.
func UniqueExpression(name string, expressions ...string) Constraint {
	return Constraint{Name: name, Type: TypeUnique, Expressions: append([]string(nil), expressions...)}
}

// Check creates check constraint metadata.
func Check(name, expression string) Constraint {
	return Constraint{Name: name, Type: TypeCheck, Check: expression}
}

// Exclude creates exclusion constraint metadata.
func Exclude(name string, exclusions ...Exclusion) Constraint {
	return Constraint{Name: name, Type: TypeExclusion, Exclusions: append([]Exclusion(nil), exclusions...)}
}

// WithFields appends ordered fields to a constraint.
func (c Constraint) WithFields(fields ...IndexField) Constraint {
	c.Fields = append(c.Fields, fields...)
	return c
}

// WithExpressions appends functional constraint expressions.
func (c Constraint) WithExpressions(expressions ...string) Constraint {
	c.Expressions = append(c.Expressions, expressions...)
	return c
}

// WithCondition adds a conditional constraint predicate.
func (c Constraint) WithCondition(condition string) Constraint {
	c.Condition = condition
	return c
}

// WithDeferrable adds deferrable behavior metadata.
func (c Constraint) WithDeferrable(deferrable Deferrable) Constraint {
	c.Deferrable = deferrable
	return c
}

// WithNullsDistinct stores explicit NULL distinctness behavior.
func (c Constraint) WithNullsDistinct(value bool) Constraint {
	c.NullsDistinct = &value
	return c
}

// WithViolation stores Django-style violation code and message metadata.
func (c Constraint) WithViolation(code, message string) Constraint {
	c.ViolationCode = code
	c.ViolationMessage = message
	return c
}

// WithInclude adds covering columns for supported unique constraints.
func (c Constraint) WithInclude(fields ...string) Constraint {
	c.Include = append(c.Include, fields...)
	return c
}

// WithOperatorClasses adds operator class metadata.
func (c Constraint) WithOperatorClasses(opClasses ...string) Constraint {
	c.OpClasses = append(c.OpClasses, opClasses...)
	return c
}

// FieldNames returns ordered field names.
func (c Constraint) FieldNames() []string {
	names := make([]string, len(c.Fields))
	for idx, field := range c.Fields {
		names[idx] = field.Name
	}
	return names
}

// RequiresIndex reports whether a unique constraint needs physical index DDL.
func (c Constraint) RequiresIndex() bool {
	return c.Type == TypeUnique &&
		(len(c.Expressions) > 0 ||
			strings.TrimSpace(c.Condition) != "" ||
			len(c.Include) > 0 ||
			len(c.OpClasses) > 0)
}

// Clone returns a deep copy of constraint metadata.
func (c Constraint) Clone() Constraint {
	copied := c
	copied.Fields = append([]IndexField(nil), c.Fields...)
	copied.Expressions = append([]string(nil), c.Expressions...)
	copied.Exclusions = append([]Exclusion(nil), c.Exclusions...)
	copied.Include = append([]string(nil), c.Include...)
	copied.OpClasses = append([]string(nil), c.OpClasses...)
	if c.NullsDistinct != nil {
		value := *c.NullsDistinct
		copied.NullsDistinct = &value
	}
	return copied
}

// NameFor returns the explicit name or a deterministic generated name.
func (c Constraint) NameFor(table string) string {
	if c.Name != "" {
		return c.Name
	}
	return deterministicName(table, string(c.Type), c.nameParts()...)
}

// Validate verifies constraint metadata is usable by migration generation.
func (c Constraint) Validate() error {
	if err := validateDeferrable(c.Deferrable); err != nil {
		return err
	}
	if c.Deferrable != "" && c.RequiresIndex() {
		return fmt.Errorf("%w: deferrable unique constraints cannot use expressions, conditions, include columns, or operator classes", ErrInvalidConstraint)
	}
	if c.NullsDistinct != nil && c.Type != TypeUnique {
		return fmt.Errorf("%w: nulls distinct is only valid for unique constraints", ErrInvalidConstraint)
	}
	for _, field := range c.Fields {
		if err := field.validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidConstraint, err)
		}
	}
	if err := validateNonEmpty("expression", c.Expressions, ErrInvalidConstraint); err != nil {
		return err
	}
	if err := validateNonEmpty("include field", c.Include, ErrInvalidConstraint); err != nil {
		return err
	}
	if err := validateNonEmpty("operator class", c.OpClasses, ErrInvalidConstraint); err != nil {
		return err
	}
	if len(c.OpClasses) > len(c.Fields)+len(c.Expressions) {
		return fmt.Errorf("%w: operator classes cannot outnumber constrained terms", ErrInvalidConstraint)
	}

	switch c.Type {
	case TypeUnique:
		if len(c.Fields) == 0 && len(c.Expressions) == 0 {
			return fmt.Errorf("%w: unique constraints require fields or expressions", ErrInvalidConstraint)
		}
	case TypeCheck:
		if strings.TrimSpace(c.Check) == "" {
			return fmt.Errorf("%w: check expression is required", ErrInvalidConstraint)
		}
	case TypeExclusion:
		if len(c.Exclusions) == 0 {
			return fmt.Errorf("%w: exclusions are required", ErrInvalidConstraint)
		}
		for _, exclusion := range c.Exclusions {
			if strings.TrimSpace(exclusion.Expression) == "" || strings.TrimSpace(exclusion.Operator) == "" {
				return fmt.Errorf("%w: exclusion expression and operator are required", ErrInvalidConstraint)
			}
		}
	default:
		return fmt.Errorf("%w: unsupported constraint type %q", ErrInvalidConstraint, c.Type)
	}
	return nil
}

func validateDeferrable(value Deferrable) error {
	switch value {
	case "", DeferrableImmediate, DeferrableDeferred:
		return nil
	default:
		return fmt.Errorf("%w: unsupported deferrable value %q", ErrInvalidConstraint, value)
	}
}

func (c Constraint) nameParts() []string {
	parts := make([]string, 0, len(c.Fields)+len(c.Expressions)+len(c.Exclusions)+len(c.Include)+2)
	for _, field := range c.Fields {
		parts = append(parts, field.namePart())
	}
	parts = append(parts, c.Expressions...)
	if c.Check != "" {
		parts = append(parts, c.Check)
	}
	for _, exclusion := range c.Exclusions {
		parts = append(parts, exclusion.Expression, exclusion.Operator)
	}
	if c.Condition != "" {
		parts = append(parts, c.Condition)
	}
	parts = append(parts, c.Include...)
	return parts
}
