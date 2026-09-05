package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
)

// JSON cleaning and database decoding use canonical maps/slices and json.Number.
// Restore declared Go container types through the JSON codec, not scalar string
// conversion. Decode into a fresh value so a type/overflow error cannot partially
// overwrite the model. UseNumber also preserves exact numbers inside any values.
func assignJSONContainer(dst reflect.Value, value any) error {
	var encoded []byte
	var err error
	switch raw := value.(type) {
	case json.RawMessage:
		encoded = raw
	case []byte:
		encoded = raw
	default:
		encoded, err = json.Marshal(value)
	}
	if err != nil || !json.Valid(encoded) {
		return errors.New("invalid JSON container")
	}
	fresh := reflect.New(dst.Type())
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(fresh.Interface()); err != nil {
		return errors.New("JSON value does not match the declared Go container type")
	}
	// encoding/json otherwise accepts null into a string/integer element as
	// its zero value, and truncates excess fixed-array elements. Neither is a
	// faithful JSON model assignment. Verify shape/null preservation before
	// installing the decoded container; explicit numeric Go types still own
	// their normal representation/rounding semantics.
	converted, err := json.Marshal(fresh.Elem().Interface())
	if err != nil || !jsonContainerShapePreserved(encoded, converted) {
		return errors.New("JSON shape or null cannot be represented by the declared Go container type")
	}
	dst.Set(fresh.Elem())
	return nil
}

func jsonContainerShapePreserved(before, after []byte) bool {
	decode := func(encoded []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		return value, err
	}
	source, err := decode(before)
	if err != nil {
		return false
	}
	target, err := decode(after)
	if err != nil {
		return false
	}
	var matches func(any, any, int) bool
	matches = func(source, target any, depth int) bool {
		if depth > 64 {
			return false
		}
		if source == nil || target == nil {
			return source == nil && target == nil
		}
		switch source := source.(type) {
		case []any:
			target, ok := target.([]any)
			if !ok || len(source) != len(target) {
				return false
			}
			for i, value := range source {
				if !matches(value, target[i], depth+1) {
					return false
				}
			}
		case map[string]any:
			target, ok := target.(map[string]any)
			if !ok || len(source) != len(target) {
				return false
			}
			for key, value := range source {
				other, ok := target[key]
				if !ok || !matches(value, other, depth+1) {
					return false
				}
			}
		default:
			switch target.(type) {
			case []any, map[string]any:
				return false
			}
		}
		return true
	}
	return matches(source, target, 0)
}
