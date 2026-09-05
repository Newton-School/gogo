package orm

import (
	"database/sql/driver"
	"encoding"
	"encoding/json"
	"github.com/Newton-School/gogo/core/db"
	"reflect"
)

func clonePredicate(value db.Predicate) db.Predicate {
	value.Children = append([]db.Predicate(nil), value.Children...)
	for i := range value.Children {
		value.Children[i] = clonePredicate(value.Children[i])
	}
	value.Value = cloneQueryValue(value.Value)
	if value.Expression != nil {
		copy := cloneExpression(*value.Expression)
		value.Expression = &copy
	}
	return value
}
func cloneExpression(value db.Expression) db.Expression {
	value.Value = cloneQueryValue(value.Value)
	value.Args = append([]db.Expression(nil), value.Args...)
	for i := range value.Args {
		value.Args[i] = cloneExpression(value.Args[i])
	}
	return value
}

type cloneVisit struct {
	kind    reflect.Kind
	typ     reflect.Type
	pointer uintptr
	length  int
}

// cloneQueryValue snapshots mutable plain query data without evaluating provider
// hooks. Opaque structs and custom Valuer/Marshaler objects remain caller-owned:
// copying their internals could copy active locks, resources or encoder state.
// Cycle tracking preserves cyclic inputs so the eventual encoder can reject them
// normally, rather than overflowing during query construction.
func cloneQueryValue(value any) any {
	if value == nil {
		return nil
	}
	seen := map[cloneVisit]reflect.Value{}
	var copyValue func(reflect.Value) reflect.Value
	copyValue = func(value reflect.Value) reflect.Value {
		if !value.IsValid() {
			return value
		}
		if value.Kind() == reflect.Struct && opaqueQueryStruct(value.Type()) || value.Kind() == reflect.Pointer && value.Type().Elem().Kind() == reflect.Struct && opaqueQueryStruct(value.Type().Elem()) {
			return value
		}
		switch value.Kind() {
		case reflect.Interface:
			if value.IsNil() {
				return reflect.Zero(value.Type())
			}
			result := reflect.New(value.Type()).Elem()
			result.Set(copyValue(value.Elem()))
			return result
		case reflect.Pointer, reflect.Slice, reflect.Map:
			if value.IsNil() {
				return reflect.Zero(value.Type())
			}
			visit := cloneVisit{kind: value.Kind(), typ: value.Type(), pointer: uintptr(value.UnsafePointer())}
			if value.Kind() == reflect.Slice {
				visit.length = value.Len()
			}
			if existing, ok := seen[visit]; ok {
				return existing
			}
			var result reflect.Value
			switch value.Kind() {
			case reflect.Pointer:
				result = reflect.New(value.Type().Elem())
				seen[visit] = result
				result.Elem().Set(copyValue(value.Elem()))
			case reflect.Slice:
				result = reflect.MakeSlice(value.Type(), value.Len(), value.Len())
				seen[visit] = result
				for i := 0; i < value.Len(); i++ {
					result.Index(i).Set(copyValue(value.Index(i)))
				}
			case reflect.Map:
				result = reflect.MakeMapWithSize(value.Type(), value.Len())
				seen[visit] = result
				iter := value.MapRange()
				for iter.Next() {
					result.SetMapIndex(iter.Key(), copyValue(iter.Value()))
				}
			}
			return result
		case reflect.Array:
			result := reflect.New(value.Type()).Elem()
			for i := 0; i < value.Len(); i++ {
				result.Index(i).Set(copyValue(value.Index(i)))
			}
			return result
		case reflect.Struct:
			result := reflect.New(value.Type()).Elem()
			result.Set(value)
			for i := 0; i < value.NumField(); i++ {
				if result.Field(i).CanSet() && value.Field(i).CanInterface() {
					result.Field(i).Set(copyValue(value.Field(i)))
				}
			}
			return result
		default:
			return value
		}
	}
	return copyValue(reflect.ValueOf(value)).Interface()
}

func opaqueQueryStruct(typ reflect.Type) bool {
	for _, contract := range []reflect.Type{reflect.TypeFor[driver.Valuer](), reflect.TypeFor[json.Marshaler](), reflect.TypeFor[encoding.TextMarshaler]()} {
		if typ.Implements(contract) || reflect.PointerTo(typ).Implements(contract) {
			return true
		}
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() || field.Type.Kind() == reflect.Struct && opaqueQueryStruct(field.Type) {
			return true
		}
	}
	return false
}
