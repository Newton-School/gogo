// Package api provides explicit, direction-aware REST resource contracts.
// Database model registration never exposes fields automatically.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/models"
)

type ValidationError struct {
	Fields map[string][]models.FieldError `json:"fields"`
}

func (*ValidationError) Error() string { return "api: input validation failed" }
func (e *ValidationError) add(path, code, message string) {
	if e.Fields == nil {
		e.Fields = map[string][]models.FieldError{}
	}
	e.Fields[path] = append(e.Fields[path], models.FieldError{Code: code, Message: message})
}
func (e *ValidationError) merge(path string, err error) bool {
	var nested *ValidationError
	if errors.As(err, &nested) {
		for key, items := range nested.Fields {
			if path != "" {
				key = path + "." + key
			}
			e.Fields[key] = append(e.Fields[key], items...)
		}
		return true
	}
	var field models.FieldError
	if errors.As(err, &field) {
		e.add(path, field.Code, field.Message)
		return true
	}
	return false
}

type Values map[string]any

func (v Values) Get(name string) (any, error) {
	value, ok := v[name]
	if !ok {
		return nil, ErrMissingField
	}
	return value, nil
}

var ErrMissingField = errors.New("api: representation field is missing")

type ValueReader interface{ Get(string) (any, error) }
type Validator func(context.Context, Values) error
type FieldValidator func(context.Context, any) (any, error)
type Field struct {
	Name, Source, Label, HelpText                    string
	Required, ReadOnly, WriteOnly, Hidden, AllowNull bool
	Default                                          func(context.Context) (any, error)
	Model                                            models.Field
	Element                                          *Field
	Nested                                           *Serializer
	Dictionary                                       bool
	MinItems, MaxItems                               int
	Validate                                         FieldValidator
	Represent                                        func(context.Context, any) (any, error)
	Compute                                          func(context.Context, ValueReader) (any, error)
	// OutputSchema describes an otherwise ambiguous custom representation.
	// New freezes its metadata; OutputSchema generation checks completeness.
	OutputSchema *WireSchema
}
type Definition struct {
	Fields       []Field
	Validate     Validator
	AllowUnknown bool
}
type Serializer struct {
	fields       []Field
	validate     Validator
	allowUnknown bool
}
type BindOptions struct{ Partial bool }

// Scalar reuses the same value normalization and validators as models, without
// treating model editability or model registration as a public field allowlist.
func Scalar(field models.Field) Field {
	return Field{Name: field.Name, Required: !field.Blank && !field.HasDefault(), AllowNull: field.Null, Model: field}
}
func StringField(name string, options ...models.FieldOption) Field {
	return Scalar(models.CharField(name, options...))
}
func IntegerField(name string, options ...models.FieldOption) Field {
	return Scalar(models.BigIntegerField(name, options...))
}
func BooleanField(name string) Field { return Scalar(models.BooleanField(name)) }
func DecimalField(name string, digits, places int) Field {
	return Scalar(models.DecimalField(name, digits, places))
}
func JSONField(name string) Field { return Scalar(models.JSONField(name)) }
func ListField(name string, element Field, maximum int) Field {
	return Field{Name: name, Required: true, Element: &element, MaxItems: maximum}
}
func DictField(name string, element Field, maximum int) Field {
	f := ListField(name, element, maximum)
	f.Dictionary = true
	return f
}
func NestedField(name string, serializer *Serializer) Field {
	return Field{Name: name, Required: true, Nested: serializer}
}
func ComputedField(name string, compute func(context.Context, ValueReader) (any, error)) Field {
	return Field{Name: name, ReadOnly: true, Compute: compute}
}

func New(def Definition) (*Serializer, error) {
	s := &Serializer{validate: def.Validate, allowUnknown: def.AllowUnknown}
	seen, writable := map[string]bool{}, map[string]bool{}
	outputBudget := outputSchemaBudget{nodes: outputSchemaMaxNodes, text: outputSchemaMaxBytes}
	for _, f := range def.Fields {
		var err error
		f, err = freezeField(f, 0, &outputBudget)
		if err != nil {
			return nil, err
		}
		if seen[f.Name] || !models.ValidIdentifier(f.Name) {
			return nil, errors.New("api: duplicate or invalid serializer field")
		}
		seen[f.Name] = true
		if f.Source == "" {
			f.Source = f.Name
		}
		if !models.ValidIdentifier(f.Source) {
			return nil, errors.New("api: field source must be one explicit attribute")
		}
		if !f.ReadOnly {
			if writable[f.Source] {
				return nil, errors.New("api: duplicate writable source")
			}
			writable[f.Source] = true
		}
		s.fields = append(s.fields, f)
	}
	return s, nil
}
func freezeField(f Field, depth int, outputBudget *outputSchemaBudget) (Field, error) {
	if depth > 32 {
		return Field{}, errors.New("api: recursive field definition")
	}
	if f.OutputSchema != nil {
		var err error
		f.OutputSchema, err = freezeOutputSchema(f.OutputSchema, outputBudget)
		if err != nil {
			return Field{}, err
		}
	}
	if f.ReadOnly && (f.WriteOnly || f.Hidden) || f.Compute != nil && !f.ReadOnly || f.Hidden && f.Default == nil {
		return Field{}, errors.New("api: conflicting field direction or missing hidden default")
	}
	if f.MinItems < 0 || f.MaxItems < 0 || f.MaxItems > 0 && f.MinItems > f.MaxItems {
		return Field{}, errors.New("api: invalid collection limits")
	}
	if f.Element != nil {
		if f.Element.ReadOnly || f.Element.WriteOnly || f.Element.Hidden || f.Element.Compute != nil {
			return Field{}, errors.New("api: set collection direction on its parent field")
		}
		child, err := freezeField(*f.Element, depth+1, outputBudget)
		if err != nil {
			return Field{}, err
		}
		f.Element = &child
		if f.MaxItems == 0 {
			f.MaxItems = 1000
		}
	}
	if f.Element == nil && f.Nested == nil && f.Model.Kind == "" && f.Validate == nil && f.Compute == nil {
		return Field{}, errors.New("api: field needs a codec")
	}
	if f.Element != nil && f.Nested != nil {
		return Field{}, errors.New("api: collection element must own nested serializer")
	}
	if f.Model.Relation != nil && (!f.ReadOnly && f.Validate == nil || !f.WriteOnly && !f.Hidden && f.Represent == nil) {
		return Field{}, errors.New("api: relation fields require explicit scoped input and output policies")
	}
	f.Model = (models.Schema{Fields: []models.Field{f.Model}}).Clone().Fields[0]
	return f, nil
}

// Validate returns only declared writable sources. Missing PATCH fields remain
// absent; null, zero and omitted values are deliberately distinct.
func (s *Serializer) Validate(ctx context.Context, input Values, options BindOptions) (Values, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if input == nil {
		return nil, &ValidationError{Fields: map[string][]models.FieldError{"non_field_errors": {{Code: "object", Message: "Expected an object."}}}}
	}
	result := Values{}
	validation := &ValidationError{Fields: map[string][]models.FieldError{}}
	known := map[string]bool{}
	for _, f := range s.fields {
		known[f.Name] = true
		if f.ReadOnly {
			continue
		}
		value, exists := input[f.Name]
		if f.Hidden {
			exists = false
		}
		if !exists {
			if options.Partial {
				continue
			}
			if f.Default != nil {
				var err error
				value, err = f.Default(ctx)
				if err != nil {
					return nil, err
				}
				exists = true
			} else if f.Model.DefaultFunc != nil {
				value = f.Model.DefaultFunc()
				exists = true
			} else if f.Model.Default != nil {
				value = f.Model.Default
				exists = true
			}
			if !exists {
				if f.Required {
					validation.add(f.Name, "required", "This field is required.")
				}
				continue
			}
		}
		cleaned, err := f.clean(ctx, value, options)
		if err != nil {
			if !validation.merge(f.Name, err) {
				return nil, err
			}
			continue
		}
		result[f.Source] = cleaned
	}
	if !s.allowUnknown {
		for name := range input {
			if !known[name] {
				validation.add(name, "unknown", "Unknown field.")
			}
		}
	}
	if len(validation.Fields) != 0 {
		return nil, validation
	}
	if s.validate != nil {
		if err := s.validate(ctx, result); err != nil {
			if validation.merge("non_field_errors", err) {
				return nil, validation
			}
			return nil, err
		}
	}
	return result, nil
}

func (f Field) clean(ctx context.Context, value any, options BindOptions) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if value == nil {
		if f.AllowNull {
			return nil, nil
		}
		return nil, models.Invalid("null", "This field may not be null.")
	}
	if f.Element != nil {
		validation := &ValidationError{Fields: map[string][]models.FieldError{}}
		if f.Dictionary {
			values, ok := object(value)
			if !ok {
				return nil, models.Invalid("object", "Expected an object.")
			}
			if err := f.length(len(values)); err != nil {
				return nil, err
			}
			out := Values{}
			keys := make([]string, 0, len(values))
			for key := range values {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				cleaned, err := f.Element.clean(ctx, values[key], options)
				if err != nil {
					if !validation.merge(key, err) {
						return nil, err
					}
				} else {
					out[key] = cleaned
				}
			}
			if len(validation.Fields) > 0 {
				return nil, validation
			}
			value = out
		} else {
			v := reflect.ValueOf(value)
			if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
				return nil, models.Invalid("list", "Expected a list.")
			}
			if err := f.length(v.Len()); err != nil {
				return nil, err
			}
			out := make([]any, v.Len())
			for i := 0; i < v.Len(); i++ {
				cleaned, err := f.Element.clean(ctx, v.Index(i).Interface(), options)
				if err != nil {
					if !validation.merge(strconv.Itoa(i), err) {
						return nil, err
					}
				} else {
					out[i] = cleaned
				}
			}
			if len(validation.Fields) > 0 {
				return nil, validation
			}
			value = out
		}
	} else if f.Nested != nil {
		values, ok := object(value)
		if !ok {
			return nil, models.Invalid("object", "Expected an object.")
		}
		var err error
		value, err = f.Nested.Validate(ctx, values, options)
		if err != nil {
			return nil, err
		}
	} else if f.Model.Kind != "" {
		var err error
		// A JSON string is a JSON value, not a second document to parse.
		if f.Model.Kind == models.JSON {
			value, err = json.Marshal(value)
			if err != nil {
				return nil, models.Invalid("invalid", "Enter valid JSON.")
			}
		}
		value, err = f.Model.Clean(ctx, value)
		if err != nil {
			return nil, err
		}
	}
	if f.Validate != nil {
		return f.Validate(ctx, value)
	}
	return value, nil
}
func (f Field) length(n int) error {
	if n < f.MinItems || n > f.MaxItems {
		return models.Invalid("length", "Collection size is outside its limits.")
	}
	return nil
}
func object(value any) (Values, bool) {
	switch v := value.(type) {
	case Values:
		return v, true
	case map[string]any:
		return Values(v), true
	}
	return nil, false
}

func (s *Serializer) Representation(ctx context.Context, value ValueReader) (Values, error) {
	if value == nil {
		return nil, ErrMissingField
	}
	out := Values{}
	for _, f := range s.fields {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if f.WriteOnly || f.Hidden {
			continue
		}
		var raw any
		var err error
		if f.Compute != nil {
			raw, err = f.Compute(ctx, value)
		} else {
			raw, err = value.Get(f.Source)
		}
		if errors.Is(err, ErrMissingField) && !f.Required {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[f.Name], err = f.output(ctx, raw)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (f Field) output(ctx context.Context, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if f.Represent != nil {
		return f.Represent(ctx, value)
	}
	if f.Nested != nil {
		reader, ok := value.(ValueReader)
		if !ok {
			m, yes := object(value)
			if !yes {
				return nil, errors.New("api: nested output must expose explicit values")
			}
			reader = m
		}
		return f.Nested.Representation(ctx, reader)
	}
	if f.Element != nil {
		if f.Dictionary {
			values, ok := object(value)
			if !ok {
				return nil, errors.New("api: invalid dictionary output")
			}
			if err := f.length(len(values)); err != nil {
				return nil, err
			}
			out := Values{}
			for key, v := range values {
				var err error
				out[key], err = f.Element.output(ctx, v)
				if err != nil {
					return nil, err
				}
			}
			return out, nil
		}
		v := reflect.ValueOf(value)
		if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
			return nil, errors.New("api: invalid list output")
		}
		if err := f.length(v.Len()); err != nil {
			return nil, err
		}
		out := make([]any, v.Len())
		for i := range out {
			var err error
			out[i], err = f.Element.output(ctx, v.Index(i).Interface())
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	if f.Model.Kind != "" {
		metadata := f.Model
		// Persistence validators are input rules, not query operations that
		// should run implicitly while serializing a response.
		metadata.Validators = nil
		candidate := value
		if metadata.Kind == models.JSON {
			var err error
			candidate, err = json.Marshal(value)
			if err != nil {
				return nil, err
			}
		}
		var err error
		value, err = metadata.Clean(ctx, candidate)
		if err != nil {
			return nil, errors.New("api: output does not match declared field codec")
		}
	}
	if instant, ok := value.(time.Time); ok {
		switch f.Model.Kind {
		case models.Date:
			return instant.Format("2006-01-02"), nil
		case models.Time:
			return instant.Format("15:04:05.999999"), nil
		case models.DateTime:
			return instant.UTC().Format(time.RFC3339Nano), nil
		}
	}
	if duration, ok := value.(time.Duration); ok && f.Model.Kind == models.Duration {
		return duration.String(), nil
	}
	if f.Model.Kind == models.Decimal {
		return fmt.Sprint(value), nil
	}
	// Return a detached JSON value so maps/slices cannot escape registry or
	// instance ownership, and unsupported output types fail before headers.
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var detached any
	err = decoder.Decode(&detached)
	return detached, err
}

// SavePolicy makes nested persistence and its transaction an explicit client
// decision. Validation never performs a database write.
type SavePolicy struct {
	Atomic func(context.Context, func(context.Context) error) error
	Write  func(context.Context, Values) (ValueReader, error)
}

func (s *Serializer) Save(ctx context.Context, input Values, options BindOptions, policy SavePolicy) (ValueReader, error) {
	if policy.Atomic == nil || policy.Write == nil {
		return nil, errors.New("api: explicit atomic save policy required")
	}
	values, err := s.Validate(ctx, input, options)
	if err != nil {
		return nil, err
	}
	var result ValueReader
	err = policy.Atomic(ctx, func(tx context.Context) error { var err error; result, err = policy.Write(tx, values); return err })
	if err != nil {
		return nil, err
	}
	return result, nil
}
