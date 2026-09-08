package orm

import (
	"database/sql/driver"
	"encoding"
	"encoding/json"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"reflect"
)

func clonePredicate(value db.Predicate) db.Predicate {
	state := &cloneTree{}
	result := clonePredicateAt(value, state, 0)
	if state.invalid {
		return invalidPredicate()
	}
	return result
}

type cloneTree struct {
	nodes   int
	invalid bool
}

func (s *cloneTree) enter(depth int) bool {
	s.nodes++
	if depth > 64 || s.nodes > 8192 {
		s.invalid = true
	}
	return !s.invalid
}

func invalidExpression() db.Expression { return db.Expression{Kind: "invalid_tree"} }
func invalidPredicate() db.Predicate {
	expression := invalidExpression()
	return db.Predicate{Expression: &expression}
}

func clonePredicateAt(value db.Predicate, state *cloneTree, depth int) db.Predicate {
	if !state.enter(depth) {
		return invalidPredicate()
	}
	value.Children = append([]db.Predicate(nil), value.Children...)
	for i := range value.Children {
		value.Children[i] = clonePredicateAt(value.Children[i], state, depth+1)
	}
	value.Value = cloneQueryValueAt(value.Value, state, depth+1)
	if value.Expression != nil {
		copy := cloneExpressionAt(*value.Expression, state, depth+1)
		value.Expression = &copy
	}
	return value
}
func cloneExpression(value db.Expression) db.Expression {
	state := &cloneTree{}
	result := cloneExpressionAt(value, state, 0)
	if state.invalid {
		return invalidExpression()
	}
	return result
}

func cloneExpressionAt(value db.Expression, state *cloneTree, depth int) db.Expression {
	if !state.enter(depth) {
		return invalidExpression()
	}
	value.Value = cloneQueryValueAt(value.Value, state, depth+1)
	value.Branches = append([]db.WhenBranch(nil), value.Branches...)
	for i := range value.Branches {
		value.Branches[i].Condition = clonePredicateAt(value.Branches[i].Condition, state, depth+1)
		value.Branches[i].Then = cloneExpressionAt(value.Branches[i].Then, state, depth+1)
	}
	if value.Output != nil {
		copy := cloneQueryValueAt(*value.Output, state, depth+1).(models.Field)
		value.Output = &copy
	}
	if value.Filter != nil {
		predicate := clonePredicateAt(*value.Filter, state, depth+1)
		value.Filter = &predicate
	}
	if value.Window != nil {
		window := *value.Window
		if len(window.PartitionBy) > 64 || len(window.OrderBy) > 64 {
			state.invalid = true
			return invalidExpression()
		}
		window.PartitionBy = append([]db.Expression(nil), window.PartitionBy...)
		for i := range window.PartitionBy {
			window.PartitionBy[i] = cloneExpressionAt(window.PartitionBy[i], state, depth+1)
		}
		window.OrderBy = append([]db.WindowOrder(nil), window.OrderBy...)
		for i := range window.OrderBy {
			window.OrderBy[i].Expression = cloneExpressionAt(window.OrderBy[i].Expression, state, depth+1)
		}
		if window.Frame != nil {
			frame := *window.Frame
			window.Frame = &frame
		}
		value.Window = &window
	}
	value.Args = append([]db.Expression(nil), value.Args...)
	for i := range value.Args {
		value.Args[i] = cloneExpressionAt(value.Args[i], state, depth+1)
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
	return cloneQueryValueAt(value, &cloneTree{}, 0)
}

func cloneQueryValueAt(value any, state *cloneTree, depth int) any {
	if value == nil {
		return nil
	}
	seen := map[cloneVisit]reflect.Value{}
	var copyValue func(reflect.Value) reflect.Value
	copyValue = func(value reflect.Value) reflect.Value {
		if !value.IsValid() {
			return value
		}
		// RHS expressions can live in Predicate.Value or IN/range containers.
		// They share the enclosing AST's depth/work bound, not an independent
		// recursive clone that would preserve a compiler-recursion cycle.
		if value.Type() == reflect.TypeFor[db.Expression]() {
			return reflect.ValueOf(cloneExpressionAt(value.Interface().(db.Expression), state, depth+1))
		}
		if value.Type() == reflect.TypeFor[db.Predicate]() {
			return reflect.ValueOf(clonePredicateAt(value.Interface().(db.Predicate), state, depth+1))
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
