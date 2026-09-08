package sqlcompiler

import (
	"errors"
	"reflect"

	"github.com/Newton-School/gogo/core/db"
)

// WalkQueryExpressions visits mutable expression slots in a private query copy.
// One expression budget covers the outer SELECT, all inner SELECTs and any
// actual expression nodes inside parameter containers. Ordinary parameter data
// is scanned separately, without changing its encoder-owned size/cycle policy.
// Visitors must not recursively invoke this walk or retain slots.
// It also inspects expression-shaped values nested in containers, so a private
// lazy ORM node cannot accidentally become a bound JSON/driver value.
func WalkQueryExpressions(query *db.Select, visit func(*db.Expression, bool, bool) error) error {
	w := queryExpressionWalker{visit: visit, rewrite: true}
	return w.query(query, false, 0)
}

func inspectQueryExpressions(query db.Select, visit func(*db.Expression, bool, bool) error) error {
	w := queryExpressionWalker{visit: visit}
	return w.query(&query, false, 0)
}

type queryExpressionWalker struct {
	nodes          int
	visit          func(*db.Expression, bool, bool) error
	rewrite        bool
	parameterSeen  map[parameterVisit]bool
	parameterTypes map[reflect.Type]bool
}

// UnpreparedQueryValue is embedded by the ORM's private lazy carrier. Its
// sealed marker is recognized without calling methods or inspecting private
// state; extracting Expression.Value cannot turn that carrier into JSON data.
// This internal marker adds no public backend or db.Expression API.
type UnpreparedQueryValue struct{}

func (UnpreparedQueryValue) unpreparedQueryValue() {}

type unpreparedQueryCarrier interface{ unpreparedQueryValue() }

func (w *queryExpressionWalker) enter(depth int) error {
	w.nodes++
	if depth > 64 || w.nodes > 8192 {
		return errors.New("orm: query expression tree exceeds depth or work bound")
	}
	return nil
}

func (w *queryExpressionWalker) query(q *db.Select, inner bool, depth int) error {
	if err := w.enter(depth); err != nil {
		return err
	}
	for range q.Fields {
		if err := w.enter(depth + 1); err != nil {
			return err
		}
	}
	for range q.Order {
		if err := w.enter(depth + 1); err != nil {
			return err
		}
	}
	for i := range q.Aliases {
		if err := w.expression(&q.Aliases[i].Expression, inner, false, depth+1); err != nil {
			return err
		}
	}
	for i := range q.Projections {
		if err := w.expression(&q.Projections[i].Expression, inner, false, depth+1); err != nil {
			return err
		}
	}
	if err := w.predicate(&q.Where, inner, false, depth+1); err != nil {
		return err
	}
	if err := w.predicate(&q.Having, inner, false, depth+1); err != nil {
		return err
	}
	for i := range q.Joins {
		if err := w.predicate(&q.Joins[i].Where, inner, false, depth+1); err != nil {
			return err
		}
		if q.Joins[i].Through != nil {
			if err := w.predicate(&q.Joins[i].Through.Where, inner, false, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *queryExpressionWalker) expression(e *db.Expression, inner, parameter bool, depth int) error {
	if err := w.enter(depth); err != nil {
		return err
	}
	if err := w.visit(e, inner, parameter); err != nil {
		return err
	}
	if parameter {
		// Output is compiler metadata only in a SQL position. Inside ordinary
		// expression-shaped data all of its public declaration values are data
		// too, and must not hide an unprepared query from the encoder boundary.
		if err := w.parameter(reflect.ValueOf(e.Output), inner, depth+1); err != nil {
			return err
		}
	}
	if e.Subquery != nil {
		if inner {
			return unsupportedSubquery("Nested subqueries are not supported")
		}
		if err := w.query(&e.Subquery.Query, true, depth+1); err != nil {
			return err
		}
	}
	for i := range e.Args {
		if err := w.expression(&e.Args[i], inner, parameter, depth+1); err != nil {
			return err
		}
	}
	if e.Filter != nil {
		if err := w.predicate(e.Filter, inner, parameter, depth+1); err != nil {
			return err
		}
	}
	for i := range e.Branches {
		if err := w.predicate(&e.Branches[i].Condition, inner, parameter, depth+1); err != nil {
			return err
		}
		if err := w.expression(&e.Branches[i].Then, inner, parameter, depth+1); err != nil {
			return err
		}
	}
	if e.Window != nil {
		for i := range e.Window.PartitionBy {
			if err := w.expression(&e.Window.PartitionBy[i], inner, parameter, depth+1); err != nil {
				return err
			}
		}
		for i := range e.Window.OrderBy {
			if err := w.expression(&e.Window.OrderBy[i].Expression, inner, parameter, depth+1); err != nil {
				return err
			}
		}
	}
	if e.Kind == "orm_subquery" && !parameter {
		if _, carrier := e.Value.(unpreparedQueryCarrier); carrier {
			// The enclosing expression visitor owns valid lazy preparation.
			// The same carrier in every data position is rejected below.
			return nil
		}
	}
	return w.parameter(reflect.ValueOf(e.Value), inner, depth+1)
}

func (w *queryExpressionWalker) predicate(p *db.Predicate, inner, parameter bool, depth int) error {
	if err := w.enter(depth); err != nil {
		return err
	}
	if p.Expression != nil {
		if err := w.expression(p.Expression, inner, parameter, depth+1); err != nil {
			return err
		}
	}
	if e, ok := p.Value.(db.Expression); ok {
		if err := w.expression(&e, inner, parameter, depth+1); err != nil {
			return err
		}
		if w.rewrite && !parameter {
			p.Value = e
		}
	} else if p.Lookup == "in" || p.Lookup == "range" {
		v := reflect.ValueOf(p.Value)
		if v.IsValid() && (v.Kind() == reflect.Array || v.Kind() == reflect.Slice) {
			// Keep parameter representation unchanged unless a direct expression
			// needs rewriting; ordinary typed slices retain their driver type.
			var changed []any
			for i := 0; i < v.Len(); i++ {
				item := v.Index(i).Interface()
				if e, ok := item.(db.Expression); ok {
					if changed == nil && w.rewrite && !parameter {
						changed = make([]any, v.Len())
						for j := range changed {
							changed[j] = v.Index(j).Interface()
						}
					}
					if err := w.expression(&e, inner, parameter, depth+1); err != nil {
						return err
					}
					if w.rewrite && !parameter {
						changed[i] = e
					}
				} else if err := w.parameter(reflect.ValueOf(item), inner, depth+1); err != nil {
					return err
				}
			}
			if changed != nil {
				p.Value = changed
			}
		} else if err := w.parameter(v, inner, depth+1); err != nil {
			return err
		}
	} else if err := w.parameter(reflect.ValueOf(p.Value), inner, depth+1); err != nil {
		return err
	}
	for i := range p.Children {
		if err := w.predicate(&p.Children[i], inner, parameter, depth+1); err != nil {
			return err
		}
	}
	return nil
}

type parameterVisit struct {
	typ     reflect.Type
	pointer uintptr
	length  int
	inner   bool
}

type parameterFrame struct {
	value        reflect.Value
	depth, index int
	entered      bool
	iterator     *reflect.MapIter
}

// parameter scans public data without invoking marshalers, Valuers or other
// methods. It is iterative and memoizes pointer/map/slice identities: ordinary
// cycles remain the encoder's responsibility, and shared graphs are not expanded
// exponentially. Container depth and scalar leaves do not consume AST budget.
func (w *queryExpressionWalker) parameter(value reflect.Value, inner bool, depth int) error {
	stack := []parameterFrame{{value: value, depth: depth}}
	for len(stack) > 0 {
		frame := &stack[len(stack)-1]
		v := frame.value
		if !frame.entered {
			if !v.IsValid() || !w.parameterType(v.Type()) {
				stack = stack[:len(stack)-1]
				continue
			}
			if v.Type().Implements(reflect.TypeFor[unpreparedQueryCarrier]()) {
				return unsupportedSubquery("Unprepared query values cannot be encoded as parameter data")
			}
			switch v.Kind() {
			case reflect.Interface:
				if v.IsNil() {
					stack = stack[:len(stack)-1]
				} else {
					frame.value = v.Elem()
				}
				continue
			case reflect.Pointer, reflect.Map, reflect.Slice:
				if v.IsNil() {
					stack = stack[:len(stack)-1]
					continue
				}
				key := parameterVisit{typ: v.Type(), pointer: v.Pointer(), inner: inner}
				if v.Kind() == reflect.Slice {
					key.length = v.Len()
				}
				if w.parameterSeen[key] {
					stack = stack[:len(stack)-1]
					continue
				}
				if w.parameterSeen == nil {
					w.parameterSeen = make(map[parameterVisit]bool)
				}
				w.parameterSeen[key] = true
				if v.Kind() == reflect.Pointer {
					frame.value = v.Elem()
					continue
				}
			}
			if v.Type() == reflect.TypeFor[db.Expression]() && v.CanInterface() {
				expression := v.Interface().(db.Expression)
				if err := w.expression(&expression, inner, true, frame.depth); err != nil {
					return err
				}
				stack = stack[:len(stack)-1]
				continue
			}
			if v.Type() == reflect.TypeFor[db.Predicate]() {
				if err := w.enter(frame.depth); err != nil {
					return err
				}
				frame.depth++
			}
			frame.entered = true
			if v.Kind() == reflect.Map {
				frame.iterator = v.MapRange()
			}
		}
		switch v.Kind() {
		case reflect.Struct:
			for frame.index < v.NumField() && !publicParameterField(v.Type().Field(frame.index)) {
				frame.index++
			}
			if frame.index < v.NumField() {
				child := parameterFrame{value: v.Field(frame.index), depth: frame.depth}
				frame.index++
				stack = append(stack, child)
				continue
			}
		case reflect.Array, reflect.Slice:
			if frame.index < v.Len() {
				child := parameterFrame{value: v.Index(frame.index), depth: frame.depth}
				frame.index++
				stack = append(stack, child)
				continue
			}
		case reflect.Map:
			if frame.iterator.Next() {
				key := parameterFrame{value: frame.iterator.Key(), depth: frame.depth}
				child := parameterFrame{value: frame.iterator.Value(), depth: frame.depth}
				stack = append(stack, key, child)
				continue
			}
		}
		stack = stack[:len(stack)-1]
	}
	return nil
}

func publicParameterField(field reflect.StructField) bool {
	// JSON promotes exported children of private anonymous struct/pointer
	// embeddings. Ordinary private fields are not inspected.
	embedded := field.Type
	if embedded.Kind() == reflect.Pointer {
		embedded = embedded.Elem()
	}
	return field.IsExported() || field.Anonymous && embedded.Kind() == reflect.Struct
}

// parameterType memoizes which types can contain expressions. Resolve recursive
// type graphs by backwards reachability, not a provisional false entry: a field
// after a recursive edge can still contain an interface/expression. All scalar
// containers (including large byte buffers and map[string]int) are pruned.
func (w *queryExpressionWalker) parameterType(root reflect.Type) bool {
	if possible, known := w.parameterTypes[root]; known {
		return possible
	}
	if w.parameterTypes == nil {
		w.parameterTypes = make(map[reflect.Type]bool)
	}
	seen := map[reflect.Type]bool{root: true}
	parents := map[reflect.Type][]reflect.Type{}
	queue := []reflect.Type{root}
	positive := []reflect.Type{}
	for i := 0; i < len(queue); i++ {
		typ := queue[i]
		if typ == reflect.TypeFor[db.Expression]() || typ == reflect.TypeFor[db.Predicate]() || typ.Kind() == reflect.Interface || typ.Implements(reflect.TypeFor[unpreparedQueryCarrier]()) {
			positive = append(positive, typ)
			continue
		}
		var children []reflect.Type
		switch typ.Kind() {
		case reflect.Pointer, reflect.Array, reflect.Slice:
			children = append(children, typ.Elem())
		case reflect.Map:
			children = append(children, typ.Key(), typ.Elem())
		case reflect.Struct:
			for j := 0; j < typ.NumField(); j++ {
				if field := typ.Field(j); publicParameterField(field) {
					children = append(children, field.Type)
				}
			}
		}
		for _, child := range children {
			if possible, known := w.parameterTypes[child]; known {
				if possible {
					positive = append(positive, typ)
				}
				continue
			}
			parents[child] = append(parents[child], typ)
			if !seen[child] {
				seen[child] = true
				queue = append(queue, child)
			}
		}
	}
	for _, typ := range queue {
		w.parameterTypes[typ] = false
	}
	for i := 0; i < len(positive); i++ {
		typ := positive[i]
		if !w.parameterTypes[typ] {
			w.parameterTypes[typ] = true
			positive = append(positive, parents[typ]...)
		}
	}
	return w.parameterTypes[root]
}
