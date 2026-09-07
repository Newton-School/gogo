package orm

import (
	"bytes"
	"math"
	"reflect"
	"time"
)

type deleteEqualVisit struct {
	typ    reflect.Type
	a, b   uintptr
	length int
}

// Compare detached copies without calling user methods. Unlike DeepEqual,
// unchanged NaNs compare by bits and pointer-valued map keys follow the clone
// graph. A bounded fallback covers non-reflexive (NaN-containing) map keys.
type deleteEquality struct {
	clone *deleteClone
	seen  map[deleteEqualVisit]bool
	nodes int
}

func equalDeleteValue(clone *deleteClone, a, b any) bool {
	nodes, bytes := clone.nodes, clone.bytes
	defer func() { clone.nodes, clone.bytes = nodes, bytes }()
	equal := deleteEquality{clone: clone}
	return equal.value(reflect.ValueOf(a), reflect.ValueOf(b), 0)
}

func (e *deleteEquality) value(a, b reflect.Value, depth int) bool {
	e.nodes++
	if depth > 128 || e.nodes > 1<<20 || a.IsValid() != b.IsValid() {
		return false
	}
	if !a.IsValid() {
		return true
	}
	if a.Type() != b.Type() {
		return false
	}
	if a.Type() == reflect.TypeFor[time.Time]() {
		if !a.CanInterface() || !b.CanInterface() {
			return false
		}
		left, right := a.Interface().(time.Time), b.Interface().(time.Time)
		return left.Equal(right) && reflect.DeepEqual(left.Location(), right.Location())
	}
	switch a.Kind() {
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() && b.IsNil()
		}
		return e.value(a.Elem(), b.Elem(), depth+1)
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() && b.IsNil()
		}
		if a.Kind() != reflect.Pointer && a.Len() != b.Len() {
			return false
		}
		visit := deleteEqualVisit{typ: a.Type(), a: uintptr(a.UnsafePointer()), b: uintptr(b.UnsafePointer())}
		if a.Kind() == reflect.Slice {
			visit.length = a.Len()
		}
		if e.seen[visit] {
			return true
		}
		if e.seen == nil {
			e.seen = map[deleteEqualVisit]bool{}
		}
		e.seen[visit] = true
		switch a.Kind() {
		case reflect.Pointer:
			return e.value(a.Elem(), b.Elem(), depth+1)
		case reflect.Slice:
			if a.Type().Elem().Kind() == reflect.Uint8 {
				return bytes.Equal(a.Bytes(), b.Bytes())
			}
			for i := range a.Len() {
				if !e.value(a.Index(i), b.Index(i), depth+1) {
					return false
				}
			}
		case reflect.Map:
			var unmatched []struct{ key, value reflect.Value }
			iter := a.MapRange()
			for iter.Next() {
				key, err := e.clone.reflectValue(iter.Key(), depth+1)
				if err != nil || !e.value(iter.Key(), key, depth+1) {
					return false
				}
				value := b.MapIndex(key)
				if !value.IsValid() {
					if key.Equal(key) {
						return false
					}
					unmatched = append(unmatched, struct{ key, value reflect.Value }{iter.Key(), iter.Value()})
					continue
				}
				if !e.value(iter.Value(), value, depth+1) {
					return false
				}
			}
			if len(unmatched) > 0 {
				iter := b.MapRange()
				for iter.Next() {
					if iter.Key().Equal(iter.Key()) {
						continue
					}
					matched := false
					for i, candidate := range unmatched {
						probe := deleteEquality{clone: e.clone, nodes: e.nodes + len(e.seen), seen: make(map[deleteEqualVisit]bool, len(e.seen))}
						for visit := range e.seen {
							probe.seen[visit] = true
						}
						ok := probe.value(candidate.key, iter.Key(), depth+1) && probe.value(candidate.value, iter.Value(), depth+1)
						e.nodes = probe.nodes
						if ok {
							unmatched = append(unmatched[:i], unmatched[i+1:]...)
							matched = true
							break
						}
					}
					if !matched {
						return false
					}
				}
				if len(unmatched) != 0 {
					return false
				}
			}
		}
		return true
	case reflect.Struct:
		for i := range a.NumField() {
			if !e.value(a.Field(i), b.Field(i), depth+1) {
				return false
			}
		}
		return true
	case reflect.Array:
		for i := range a.Len() {
			if !e.value(a.Index(i), b.Index(i), depth+1) {
				return false
			}
		}
		return true
	case reflect.Float32, reflect.Float64:
		return math.Float64bits(a.Float()) == math.Float64bits(b.Float())
	case reflect.Complex64, reflect.Complex128:
		return math.Float64bits(real(a.Complex())) == math.Float64bits(real(b.Complex())) && math.Float64bits(imag(a.Complex())) == math.Float64bits(imag(b.Complex()))
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return a.IsNil() && b.IsNil()
	default:
		return a.Equal(b)
	}
}
