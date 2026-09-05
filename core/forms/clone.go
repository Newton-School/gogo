package forms

import (
	"maps"
	"reflect"
	"slices"
)

// Clone isolates mutable declaration metadata. Custom widgets must be immutable
// or implement CloneWidget so that request-local declarations do not share state.
func (f Field) Clone() Field {
	f.Initial = cloneValue(f.Initial)
	f.Choices = slices.Clone(f.Choices)
	f.Validators = slices.Clone(f.Validators)
	f.InputFormats = slices.Clone(f.InputFormats)
	f.ErrorMessages = maps.Clone(f.ErrorMessages)
	if f.MinValue != nil {
		value := *f.MinValue
		f.MinValue = &value
	}
	if f.MaxValue != nil {
		value := *f.MaxValue
		f.MaxValue = &value
	}
	if f.Pattern != nil {
		f.Pattern = f.Pattern.Copy()
	}
	f.Fields = slices.Clone(f.Fields)
	for i := range f.Fields {
		f.Fields[i] = f.Fields[i].Clone()
	}
	switch widget := f.Widget.(type) {
	case InputWidget:
		widget.Attrs = maps.Clone(widget.Attrs)
		f.Widget = widget
	case *InputWidget:
		if widget != nil {
			f.Widget = &InputWidget{Type: widget.Type, Attrs: maps.Clone(widget.Attrs)}
		}
	case interface{ CloneWidget() Widget }:
		f.Widget = widget.CloneWidget()
	}
	return f
}

func cloneValue(value any) any {
	if value == nil {
		return nil
	}
	// Identity tracking preserves cycles in JSON-like maps/slices and avoids
	// repeated allocation when a declaration shares nested values.
	type identity struct {
		typ     reflect.Type
		pointer uintptr
		length  int
	}
	seen := map[identity]reflect.Value{}
	var clone func(reflect.Value, int) reflect.Value
	clone = func(v reflect.Value, depth int) reflect.Value {
		if !v.IsValid() {
			return v
		}
		// Opaque application objects (including custom types with private state)
		// are values, not a serialization boundary; callbacks retain ownership.
		if depth > 128 {
			return v
		}
		switch v.Kind() {
		case reflect.Interface:
			if v.IsNil() {
				return reflect.Zero(v.Type())
			}
			out := reflect.New(v.Type()).Elem()
			out.Set(clone(v.Elem(), depth+1))
			return out
		case reflect.Map:
			if v.IsNil() {
				return reflect.Zero(v.Type())
			}
			key := identity{v.Type(), uintptr(v.UnsafePointer()), v.Len()}
			if out, ok := seen[key]; ok {
				return out
			}
			out := reflect.MakeMapWithSize(v.Type(), v.Len())
			seen[key] = out
			iter := v.MapRange()
			for iter.Next() {
				out.SetMapIndex(iter.Key(), clone(iter.Value(), depth+1))
			}
			return out
		case reflect.Slice:
			if v.IsNil() {
				return reflect.Zero(v.Type())
			}
			key := identity{v.Type(), v.Pointer(), v.Len()}
			if out, ok := seen[key]; ok {
				return out
			}
			out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			seen[key] = out
			for i := 0; i < v.Len(); i++ {
				out.Index(i).Set(clone(v.Index(i), depth+1))
			}
			return out
		case reflect.Array:
			out := reflect.New(v.Type()).Elem()
			for i := 0; i < v.Len(); i++ {
				out.Index(i).Set(clone(v.Index(i), depth+1))
			}
			return out
		default:
			return v
		}
	}
	return clone(reflect.ValueOf(value), 0).Interface()
}
