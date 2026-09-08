package sqlcompiler

import (
	"errors"
	"reflect"
	"strings"

	"github.com/Newton-School/gogo/core/db"
)

// windowTree resolves indirect aliases without invoking codecs or compiling
// SQL. All paths (including CASE, IN/RANGE values and specification trees)
// share one depth/work budget, so cycles fail before execution.
type windowTree struct {
	compiler *Compiler
	nodes    int
	grouped  bool
}

func (s *windowTree) enter(depth int) error {
	s.nodes++
	if depth > 64 || s.nodes > 8192 {
		return errors.New("orm: window expression exceeds depth or work bound")
	}
	return nil
}

func (s *windowTree) expression(e db.Expression, allowWindow, rowOnly bool, depth int) (bool, error) {
	if err := s.enter(depth); err != nil {
		return false, err
	}
	if e.Kind == "invalid_tree" {
		return false, errors.New("orm: invalid window expression tree")
	}
	if e.Window != nil && e.Kind != "window" {
		return false, errors.New("orm: OVER metadata requires a window expression")
	}
	if e.Kind == "field" && s.compiler != nil {
		if _, stored := s.compiler.Schema.Field(e.Name); !stored {
			name, _, _ := strings.Cut(e.Name, "__")
			if alias, found := s.compiler.Aliases[name]; found {
				return s.expression(alias.Expression, allowWindow, rowOnly, depth+1)
			}
		}
	}
	if e.Kind == "window" {
		if !allowWindow {
			return false, unsupportedWindow("Window expressions are not supported in this position; an explicit subquery is required")
		}
		if err := validateWindowShape(e); err != nil {
			return false, err
		}
		if s.compiler != nil {
			features, ok := s.compiler.Dialect.(db.FeatureDialect)
			if !ok || !features.SupportsFeature("window") {
				return false, unsupportedWindow("Selected dialect does not support window expressions")
			}
			if e.Window != nil && e.Window.Frame != nil {
				if err := validateWindowFrame(features, *e.Window.Frame); err != nil {
					return false, err
				}
			}
		}
		function := e.Args[0]
		for _, argument := range function.Args {
			if argument.Kind == "field" && argument.Name == "*" && strings.EqualFold(function.Name, "COUNT") {
				continue
			}
			if _, err := s.expression(argument, false, true, depth+1); err != nil {
				return false, err
			}
		}
		if function.Filter != nil {
			if err := s.predicate(*function.Filter, true, depth+1); err != nil {
				return false, err
			}
		}
		if e.Window != nil {
			for _, expression := range e.Window.PartitionBy {
				if _, err := s.expression(expression, false, true, depth+1); err != nil {
					return false, err
				}
			}
			for _, order := range e.Window.OrderBy {
				if _, err := s.expression(order.Expression, false, true, depth+1); err != nil {
					return false, err
				}
			}
		}
		return true, nil
	}
	if rowOnly && (e.Filter != nil || e.Distinct || e.Kind == "field" && e.Name == "*") {
		return false, errors.New("orm: window arguments and keys require scalar row expressions")
	}
	aggregate := e.Kind == "function" && IsAggregateFunction(e.Name)
	s.grouped = s.grouped || aggregate
	if e.Kind == "function" && IsWindowFunction(e.Name) {
		return false, unsupportedWindow("Window functions require an explicit OVER specification")
	}
	if aggregate && rowOnly {
		return false, unsupportedWindow("Window arguments and keys cannot depend on grouped aggregates")
	}
	found := false
	for _, argument := range e.Args {
		part, err := s.expression(argument, allowWindow && !aggregate, rowOnly, depth+1)
		if err != nil {
			return false, err
		}
		found = found || part
	}
	if e.Filter != nil {
		if err := s.predicate(*e.Filter, true, depth+1); err != nil {
			return false, err
		}
	}
	for _, branch := range e.Branches {
		if err := s.predicate(branch.Condition, rowOnly, depth+1); err != nil {
			return false, err
		}
		part, err := s.expression(branch.Then, allowWindow && !aggregate, rowOnly, depth+1)
		if err != nil {
			return false, err
		}
		found = found || part
	}
	return found, nil
}

func (s *windowTree) predicate(p db.Predicate, rowOnly bool, depth int) error {
	if err := s.enter(depth); err != nil {
		return err
	}
	if p.Expression != nil {
		if _, err := s.expression(*p.Expression, false, rowOnly, depth+1); err != nil {
			return err
		}
	} else if p.Field != "" {
		if _, err := s.expression(db.Expression{Kind: "field", Name: p.Field}, false, rowOnly, depth+1); err != nil {
			return err
		}
	}
	values := []any{p.Value}
	if p.Lookup == "in" || p.Lookup == "range" {
		value := reflect.ValueOf(p.Value)
		if value.IsValid() && (value.Kind() == reflect.Slice || value.Kind() == reflect.Array) {
			if value.Len() > 8192 {
				return errors.New("orm: window predicate exceeds work bound")
			}
			values = make([]any, value.Len())
			for i := range values {
				values[i] = value.Index(i).Interface()
			}
		}
	}
	for _, value := range values {
		if expression, ok := value.(db.Expression); ok {
			if _, err := s.expression(expression, false, rowOnly, depth+1); err != nil {
				return err
			}
		}
	}
	for _, child := range p.Children {
		if err := s.predicate(child, rowOnly, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (c *Compiler) validateWindows(query db.Select) error {
	state := windowTree{compiler: c}
	found := false
	check := func(expression db.Expression) error {
		part, err := state.expression(expression, true, false, 0)
		found = found || part
		return err
	}
	for _, alias := range query.Aliases {
		if err := check(alias.Expression); err != nil {
			return err
		}
	}
	for _, projection := range query.Projections {
		if err := check(projection.Expression); err != nil {
			return err
		}
	}
	for _, name := range query.Fields {
		if err := check(db.Expression{Kind: "field", Name: name}); err != nil {
			return err
		}
	}
	for _, order := range query.Order {
		if err := check(db.Expression{Kind: "field", Name: order.Field}); err != nil {
			return err
		}
	}
	if err := state.predicate(query.Where, false, 0); err != nil {
		return err
	}
	if err := state.predicate(query.Having, false, 0); err != nil {
		return err
	}
	for _, join := range query.Joins {
		parent := join.ParentField
		if join.ParentPath != "" {
			parent = join.ParentPath + "__" + parent
		}
		for _, endpoint := range []string{parent, join.Path + "__" + join.TargetField} {
			if _, err := state.expression(db.Expression{Kind: "field", Name: endpoint}, false, true, 0); err != nil {
				return err
			}
		}
		state.compiler = &Compiler{Schema: join.Schema}
		if err := state.predicate(join.Where, true, 0); err != nil {
			return err
		}
		if join.Through != nil {
			state.compiler = &Compiler{Schema: join.Through.Schema}
			if err := state.predicate(join.Through.Where, true, 0); err != nil {
				return err
			}
		}
		state.compiler = c
	}
	if found && (len(query.GroupBy) > 0 || state.grouped || query.ForUpdate) {
		return unsupportedWindow("Grouped or row-locked window queries require a separate execution path")
	}
	return nil
}
