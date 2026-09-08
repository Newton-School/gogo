package migrations

import (
	"errors"
	"reflect"

	"github.com/Newton-School/gogo/core/models"
)

// Detach ordinary metadata containers without invoking application codecs or
// copying opaque runtime objects. Callbacks and struct values remain trusted
// configuration and must not be mutated by schema editors.
func detachTransitionValues(schemas []models.Schema) error {
	var fieldValues func(*models.Field) error
	fieldValues = func(field *models.Field) error {
		var err error
		if field.Default, err = detachMetadataValue(field.Default); err != nil {
			return err
		}
		if field.Min, err = detachMetadataValue(field.Min); err != nil {
			return err
		}
		if field.Max, err = detachMetadataValue(field.Max); err != nil {
			return err
		}
		for i := range field.Choices {
			if field.Choices[i].Value, err = detachMetadataValue(field.Choices[i].Value); err != nil {
				return err
			}
		}
		if field.Element != nil {
			return fieldValues(field.Element)
		}
		return nil
	}
	for i := range schemas {
		for j := range schemas[i].Fields {
			if err := fieldValues(&schemas[i].Fields[j]); err != nil {
				return err
			}
		}
	}
	return nil
}

func detachMetadataValue(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	nodes := 0
	var clone func(reflect.Value, int) (reflect.Value, error)
	clone = func(value reflect.Value, depth int) (reflect.Value, error) {
		nodes++
		if depth > 64 || nodes > 8192 {
			return reflect.Value{}, errors.New("migrations: transition metadata exceeds the depth or size limit")
		}
		switch value.Kind() {
		case reflect.Interface, reflect.Pointer:
			if value.IsNil() || value.Kind() == reflect.Pointer && value.Type().Elem().Kind() == reflect.Struct {
				return value, nil
			}
			inner, err := clone(value.Elem(), depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			if value.Kind() == reflect.Pointer {
				result := reflect.New(value.Type().Elem())
				result.Elem().Set(inner)
				if result.Type() != value.Type() {
					result = result.Convert(value.Type())
				}
				return result, nil
			}
			result := reflect.New(value.Type()).Elem()
			result.Set(inner)
			return result, nil
		case reflect.Map:
			if value.IsNil() {
				return value, nil
			}
			if value.Len() > 8192-nodes {
				return reflect.Value{}, errors.New("migrations: transition metadata exceeds the depth or size limit")
			}
			result := reflect.MakeMapWithSize(value.Type(), value.Len())
			iterator := value.MapRange()
			for iterator.Next() {
				entry, err := clone(iterator.Value(), depth+1)
				if err != nil {
					return reflect.Value{}, err
				}
				result.SetMapIndex(iterator.Key(), entry)
			}
			return result, nil
		case reflect.Slice, reflect.Array:
			if value.Kind() == reflect.Slice && value.IsNil() {
				return value, nil
			}
			if value.Len() > 8192-nodes {
				return reflect.Value{}, errors.New("migrations: transition metadata exceeds the depth or size limit")
			}
			var result reflect.Value
			if value.Kind() == reflect.Slice {
				result = reflect.MakeSlice(value.Type(), value.Len(), value.Len())
			} else {
				result = reflect.New(value.Type()).Elem()
			}
			for i := 0; i < value.Len(); i++ {
				entry, err := clone(value.Index(i), depth+1)
				if err != nil {
					return reflect.Value{}, err
				}
				result.Index(i).Set(entry)
			}
			return result, nil
		default:
			return value, nil
		}
	}
	result, err := clone(reflect.ValueOf(value), 0)
	if err != nil {
		return nil, err
	}
	return result.Interface(), nil
}
