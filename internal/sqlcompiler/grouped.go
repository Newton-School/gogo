package sqlcompiler

import (
	"errors"
	"reflect"
	"strings"

	"github.com/Newton-School/gogo/core/db"
)

// groupedTree resolves aliases while validating where a row field is legal.
// Its shared bound covers aggregate filters, CASE conditions, membership RHS
// expressions and alias dependencies, including cycles supplied as raw ASTs.
type groupedTree struct {
	compiler *Compiler
	keys     map[string]bool
	nodes    int
}

func (s *groupedTree) enter(depth int) error {
	s.nodes++
	if depth > 64 || s.nodes > 8192 {
		return errors.New("orm: grouped expression exceeds the depth or work bound")
	}
	return nil
}

func (c *Compiler) validateGrouped(query db.Select) error {
	features, supported := c.Dialect.(db.FeatureDialect)
	if !supported || !features.SupportsFeature("grouped_queries") {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Selected dialect does not support grouped queries"}
	}
	if len(query.GroupBy) > 64 || query.ForUpdate || query.Distinct || len(query.DistinctOn) > 0 {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Grouped queries require at most 64 keys and cannot use row locks or DISTINCT in this execution path"}
	}
	state := &groupedTree{compiler: c, keys: map[string]bool{}}
	for _, name := range query.GroupBy {
		reference, err := c.resolveField(name)
		if err != nil {
			return err
		}
		if reference.expression != nil || len(reference.path) != 0 {
			return &db.Error{Code: db.UnsupportedFeature, Message: "Grouping by a transformed or annotated key requires reusable expression identity"}
		}
		if !reference.field.IsStored() || state.keys[name] {
			return errors.New("orm: grouped keys must be distinct stored fields")
		}
		state.keys[name] = true
	}
	for _, field := range query.Fields {
		if !state.keys[field] {
			return errors.New("orm: selected value is not a grouping key")
		}
	}
	for _, alias := range query.Aliases {
		if err := state.expression(alias.Expression, false, false, 0); err != nil {
			return err
		}
	}
	for _, projection := range query.Projections {
		if err := state.expression(projection.Expression, false, false, 0); err != nil {
			return err
		}
	}
	if err := state.predicate(query.Where, true, false, 0); err != nil {
		return err
	}
	if err := state.predicate(query.Having, false, false, 0); err != nil {
		return err
	}
	for _, order := range query.Order {
		if err := state.expression(db.Expression{Kind: "field", Name: order.Field}, false, false, 0); err != nil {
			return err
		}
	}
	return nil
}

func (s *groupedTree) expression(expression db.Expression, rowsOnly, insideAggregate bool, depth int) error {
	if err := s.enter(depth); err != nil {
		return err
	}
	if expression.Kind == "invalid_tree" {
		return errors.New("orm: invalid grouped expression tree")
	}
	if expression.Kind == "field" {
		if alias, found := s.compiler.Aliases[expression.Name]; found {
			return s.expression(alias.Expression, rowsOnly, insideAggregate, depth+1)
		}
		if expression.Name == "*" {
			return errors.New("orm: star is supported only by non-distinct COUNT")
		}
		reference, err := s.compiler.resolveField(expression.Name)
		if err != nil {
			return err
		}
		// A transformed JSON alias still depends on its full underlying
		// expression. Do not let a path hide aggregates from the row-filter
		// or nested-aggregate checks. Stored paths retain their exact key
		// identity for the grouping-key check below.
		if reference.expression != nil {
			return s.expression(*reference.expression, rowsOnly, insideAggregate, depth+1)
		}
		if !rowsOnly && !insideAggregate && !s.keys[expression.Name] {
			return errors.New("orm: ungrouped field outside aggregate")
		}
		return nil
	}
	aggregate := expression.Kind == "function" && IsAggregateFunction(expression.Name)
	if aggregate && (rowsOnly || insideAggregate) {
		return errors.New("orm: aggregates are not allowed in row filters or inside another aggregate")
	}
	if expression.Kind == "function" {
		switch strings.ToUpper(expression.Name) {
		case "ROW_NUMBER", "RANK", "DENSE_RANK", "PERCENT_RANK", "CUME_DIST", "NTILE", "LAG", "LEAD", "FIRST_VALUE", "LAST_VALUE", "NTH_VALUE":
			return &db.Error{Code: db.UnsupportedFeature, Message: "Grouped window expressions require a separate window execution path"}
		}
	}
	if expression.Distinct && !aggregate || expression.Filter != nil && !aggregate {
		return errors.New("orm: DISTINCT/FILTER require an aggregate")
	}
	if aggregate && len(expression.Args) != 1 {
		return errors.New("orm: aggregate requires one argument")
	}
	if expression.Filter != nil {
		if err := s.predicate(*expression.Filter, true, false, depth+1); err != nil {
			return err
		}
	}
	for _, arg := range expression.Args {
		if arg.Kind == "field" && arg.Name == "*" && aggregate && strings.ToUpper(expression.Name) == "COUNT" && !expression.Distinct {
			continue
		}
		if err := s.expression(arg, rowsOnly, insideAggregate || aggregate, depth+1); err != nil {
			return err
		}
	}
	for _, branch := range expression.Branches {
		if err := s.predicate(branch.Condition, rowsOnly, insideAggregate || aggregate, depth+1); err != nil {
			return err
		}
		if err := s.expression(branch.Then, rowsOnly, insideAggregate || aggregate, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (s *groupedTree) predicate(predicate db.Predicate, rowsOnly, insideAggregate bool, depth int) error {
	if err := s.enter(depth); err != nil {
		return err
	}
	if predicate.Expression != nil {
		if err := s.expression(*predicate.Expression, rowsOnly, insideAggregate, depth+1); err != nil {
			return err
		}
	} else if predicate.Field != "" {
		if err := s.expression(db.Expression{Kind: "field", Name: predicate.Field}, rowsOnly, insideAggregate, depth+1); err != nil {
			return err
		}
	}
	values := []any{predicate.Value}
	if predicate.Lookup == "in" || predicate.Lookup == "range" {
		value := reflect.ValueOf(predicate.Value)
		if value.IsValid() && (value.Kind() == reflect.Slice || value.Kind() == reflect.Array) {
			values = make([]any, value.Len())
			for i := range values {
				values[i] = value.Index(i).Interface()
			}
		}
	}
	for _, value := range values {
		if expression, ok := value.(db.Expression); ok {
			if err := s.expression(expression, rowsOnly, insideAggregate, depth+1); err != nil {
				return err
			}
		}
	}
	for _, child := range predicate.Children {
		if err := s.predicate(child, rowsOnly, insideAggregate, depth+1); err != nil {
			return err
		}
	}
	return nil
}
