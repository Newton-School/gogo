package http

import (
	"encoding/json"
	"reflect"
	"time"

	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/templates"
)

func genericCreateSchemaBounded(schema models.Schema) bool {
	if len(schema.Fields) > 64 || len(schema.Indexes) > 64 || len(schema.Constraints) > 64 || len(schema.PrimaryKey) > 64 || len(schema.Ordering) > 64 || len(schema.RequiredCapabilities) > 64 {
		return false
	}
	metadata := templates.Context{
		"schema": []string{schema.AppLabel, schema.Name, schema.Table, schema.Parent, schema.ParentLink, schema.Concrete, schema.AutoCreatedBy, schema.AutoCreatedField, schema.Label, schema.LabelPlural, schema.Comment, schema.Tablespace},
		"keys":   schema.PrimaryKey, "ordering": schema.Ordering, "capabilities": schema.RequiredCapabilities, "indexes": schema.Indexes, "constraints": schema.Constraints,
	}
	fields := make([]any, 0, len(schema.Fields))
	for _, field := range schema.Fields {
		if len(field.Name) > 128 || field.Element != nil || field.Relation != nil || field.Codec != nil || len(field.Choices) > 128 || len(field.Validators) > 128 {
			return false
		}
		fields = append(fields, map[string]any{
			"text":    []string{field.Name, field.StructField, field.Column, string(field.Kind), field.Label, field.HelpText, field.Comment, field.Collation, field.Tablespace, field.DefaultID, field.DBDefault, field.GeneratedExpression, field.UniqueForDate, field.UniqueForMonth, field.UniqueForYear},
			"default": field.Default, "min": field.Min, "max": field.Max, "choices": field.Choices,
		})
	}
	metadata["fields"] = fields
	_, err := snapshotTemplateContext(metadata)
	if err != nil {
		return false
	}
	// Projection alone would silently discard private struct fields and strip
	// scalar methods. Reject these before a later JSON fingerprint or widget's
	// formatting could invoke them on the original incoming descriptor.
	for _, field := range schema.Fields {
		for _, value := range []any{field.Default, field.Min, field.Max} {
			if _, err := cloneGenericCreateMetadata(value); err != nil {
				return false
			}
		}
		for _, choice := range field.Choices {
			if _, err := cloneGenericCreateMetadata(choice.Value); err != nil {
				return false
			}
		}
	}
	return true
}

func cloneGenericCreateSchema(schema models.Schema) (models.Schema, error) {
	if !genericCreateSchemaBounded(schema) {
		return models.Schema{}, ErrGenericConfiguration
	}
	schema = schema.Clone()
	for i := range schema.Fields {
		field := &schema.Fields[i]
		var err error
		field.Default, err = cloneGenericCreateMetadata(field.Default)
		if err != nil {
			return models.Schema{}, err
		}
		field.Min, err = cloneGenericCreateMetadata(field.Min)
		if err != nil {
			return models.Schema{}, err
		}
		field.Max, err = cloneGenericCreateMetadata(field.Max)
		if err != nil {
			return models.Schema{}, err
		}
		for j := range field.Choices {
			field.Choices[j].Value, err = cloneGenericCreateMetadata(field.Choices[j].Value)
			if err != nil {
				return models.Schema{}, err
			}
		}
	}
	return schema, nil
}

// Declaration values keep their wire-relevant types (notably RawMessage and
// JSONNull). Unlike template projection, this clone cannot strip such markers
// or turn opaque application structs into an accidentally valid empty map.
// The bounded prepass rejects cycles before any recursive cloning allocation.
func cloneGenericCreateMetadata(raw any) (any, error) {
	budget := templateContextBudget{remainingValues: templateContextMaxValues, remainingBytes: templateContextMaxBytes}
	if _, ok := budget.copy(reflect.ValueOf(raw), 0); !ok {
		return nil, ErrGenericConfiguration
	}
	var copyValue func(reflect.Value) (reflect.Value, bool)
	copyValue = func(value reflect.Value) (reflect.Value, bool) {
		if !value.IsValid() {
			return value, true
		}
		if value.CanInterface() {
			switch item := value.Interface().(type) {
			case time.Time:
				zone := item.Location()
				_ = zone.String()
				copy := *zone
				return reflect.ValueOf(item.In(&copy)), true
			case json.RawMessage:
				if item == nil {
					return value, true
				}
				return reflect.ValueOf(append(json.RawMessage{}, item...)), true
			case json.Number:
				return value, true
			}
			if value.Type() == reflect.TypeOf(models.JSONNull) {
				return value, true
			}
		}
		if value.Type().NumMethod() != 0 {
			return reflect.Value{}, false
		}
		switch value.Kind() {
		case reflect.Interface:
			if value.IsNil() {
				return reflect.Zero(value.Type()), true
			}
			child, ok := copyValue(value.Elem())
			if !ok {
				return reflect.Value{}, false
			}
			out := reflect.New(value.Type()).Elem()
			out.Set(child)
			return out, true
		case reflect.Map:
			if value.Type().Key().Kind() != reflect.String || value.Type().Key().NumMethod() != 0 {
				return reflect.Value{}, false
			}
			if value.IsNil() {
				return reflect.Zero(value.Type()), true
			}
			out := reflect.MakeMapWithSize(value.Type(), value.Len())
			it := value.MapRange()
			for it.Next() {
				child, ok := copyValue(it.Value())
				if !ok {
					return reflect.Value{}, false
				}
				out.SetMapIndex(it.Key(), child)
			}
			return out, true
		case reflect.Slice, reflect.Array:
			var out reflect.Value
			if value.Kind() == reflect.Slice {
				if value.IsNil() {
					return reflect.Zero(value.Type()), true
				}
				out = reflect.MakeSlice(value.Type(), value.Len(), value.Len())
			} else {
				out = reflect.New(value.Type()).Elem()
			}
			for i := 0; i < value.Len(); i++ {
				child, ok := copyValue(value.Index(i))
				if !ok {
					return reflect.Value{}, false
				}
				out.Index(i).Set(child)
			}
			return out, true
		case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64, reflect.String:
			return value, true
		default:
			return reflect.Value{}, false
		}
	}
	copy, ok := copyValue(reflect.ValueOf(raw))
	if !ok {
		return nil, ErrGenericConfiguration
	}
	if !copy.IsValid() {
		return nil, nil
	}
	return copy.Interface(), nil
}
