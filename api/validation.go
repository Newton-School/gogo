package api

import (
	"context"
	"errors"
	"sort"

	modelvalidation "github.com/Newton-School/gogo/models/validation"
)

const NonFieldErrors = "__all__"

// ValidationErrors stores serializer validation messages by field name.
type ValidationErrors map[string][]string

// ObjectValidator validates a complete serializer value map.
type ObjectValidator func(map[string]any) ValidationErrors

// OrderedFieldError is a deterministic field error entry for API responses.
type OrderedFieldError struct {
	Field    string
	Messages []string
}

// Add appends a message to a field.
func (e ValidationErrors) Add(field, message string) {
	if field == "" {
		field = NonFieldErrors
	}
	e[field] = append(e[field], message)
}

// UniqueValidator validates a single field value with an application lookup.
func UniqueValidator(check func(any) (bool, error)) FieldValidator {
	return func(value any) error {
		if check == nil {
			return errors.New("unique validator is not configured")
		}
		unique, err := check(value)
		if err != nil || !unique {
			return errors.New("This field must be unique.")
		}
		return nil
	}
}

// UniqueTogetherValidator validates that a field set is unique together.
func UniqueTogetherValidator(fields []string, check func(map[string]any) (bool, error)) ObjectValidator {
	copied := append([]string(nil), fields...)
	return func(values map[string]any) ValidationErrors {
		if check == nil {
			return ValidationErrors{NonFieldErrors: {"unique together validator is not configured"}}
		}
		lookup := make(map[string]any, len(copied))
		for _, field := range copied {
			lookup[field] = values[field]
		}
		unique, err := check(lookup)
		if err != nil || !unique {
			return ValidationErrors{NonFieldErrors: {"fields must make a unique set"}}
		}
		return nil
	}
}

// ModelValidationValidator reuses model validation validators inside API serializers.
func ModelValidationValidator(ctx context.Context, validators ...modelvalidation.Validator) ObjectValidator {
	return func(values map[string]any) ValidationErrors {
		if ctx == nil {
			ctx = context.Background()
		}
		return FromModelValidationError(modelvalidation.Validate(ctx, values, validators...))
	}
}

// FromModelValidationError converts model validation errors into API field errors.
func FromModelValidationError(err error) ValidationErrors {
	if err == nil {
		return nil
	}
	var modelErrors *modelvalidation.Errors
	if !errors.As(err, &modelErrors) {
		return ValidationErrors{NonFieldErrors: {err.Error()}}
	}
	apiErrors := ValidationErrors{}
	for _, field := range orderedModelErrorKeys(modelErrors.Fields) {
		for _, fieldError := range modelErrors.Fields[field] {
			apiErrors.Add(field, fieldError.Message)
		}
	}
	return apiErrors
}

// OrderedErrorKeys returns field names in deterministic API response order.
func OrderedErrorKeys(errors map[string][]string) []string {
	keys := make([]string, 0, len(errors))
	for key := range errors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// OrderedFieldErrors returns deterministic field error entries for API responses.
func OrderedFieldErrors(errors map[string][]string) []OrderedFieldError {
	keys := OrderedErrorKeys(errors)
	ordered := make([]OrderedFieldError, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, OrderedFieldError{
			Field:    key,
			Messages: append([]string(nil), errors[key]...),
		})
	}
	return ordered
}

func mergeValidationErrors(target map[string][]string, source ValidationErrors) {
	if len(source) == 0 {
		return
	}
	for _, field := range OrderedErrorKeys(source) {
		target[field] = append(target[field], source[field]...)
	}
}

func orderedModelErrorKeys(errors map[string][]modelvalidation.Error) []string {
	keys := make([]string, 0, len(errors))
	for key := range errors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
