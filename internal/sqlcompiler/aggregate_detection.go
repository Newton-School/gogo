package sqlcompiler

import (
	"errors"
	"reflect"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// ContainsAggregate inspects a bounded expression tree without SQL compilation
// or callbacks. Query builders inspect every declared alias, so alias references
// do not need resolution here; terminal grouped validation resolves them and
// rejects nested aggregates, invalid field uses and dependency cycles.
func ContainsAggregate(expression db.Expression) (bool, error) {
	return (&aggregateDetector{}).expression(expression, 0)
}

type aggregateDetector struct {
	nodes   int
	aliases map[string]db.Projection
	schema  models.Schema
}

type aggregatePredicate struct {
	value     db.Predicate
	aggregate bool
	children  []*aggregatePredicate
}

func (s *aggregateDetector) enter(depth int) error {
	s.nodes++
	if depth > 64 || s.nodes > 8192 {
		return errors.New("orm: aggregate detection exceeds depth or work bound")
	}
	return nil
}

func (s *aggregateDetector) expression(expression db.Expression, depth int) (bool, error) {
	if err := s.enter(depth); err != nil {
		return false, err
	}
	if expression.Kind == "invalid_tree" {
		return false, errors.New("orm: invalid expression tree")
	}
	if expression.Kind == "field" {
		// Match the compiler's precedence: a declared root field wins even
		// when its exact name contains the JSON path separator.
		if _, stored := s.schema.Field(expression.Name); stored {
			return false, nil
		}
		name, _, _ := strings.Cut(expression.Name, "__")
		if alias, ok := s.aliases[name]; ok {
			return s.expression(alias.Expression, depth+1)
		}
	}
	found := expression.Kind == "function" && IsAggregateFunction(expression.Name)
	if expression.Filter != nil {
		part, err := s.predicate(*expression.Filter, depth+1)
		if err != nil {
			return false, err
		}
		found = found || part.aggregate
	}
	for _, arg := range expression.Args {
		part, err := s.expression(arg, depth+1)
		if err != nil {
			return false, err
		}
		found = found || part
	}
	for _, branch := range expression.Branches {
		condition, err := s.predicate(branch.Condition, depth+1)
		if err != nil {
			return false, err
		}
		part, err := s.expression(branch.Then, depth+1)
		if err != nil {
			return false, err
		}
		found = found || condition.aggregate || part
	}
	return found, nil
}

func (s *aggregateDetector) predicate(predicate db.Predicate, depth int) (*aggregatePredicate, error) {
	if err := s.enter(depth); err != nil {
		return nil, err
	}
	node := &aggregatePredicate{value: predicate}
	if predicate.Expression != nil || predicate.Field != "" {
		expression := db.Expression{Kind: "field", Name: predicate.Field}
		if predicate.Expression != nil {
			expression = *predicate.Expression
		}
		found, err := s.expression(expression, depth+1)
		if err != nil {
			return nil, err
		}
		node.aggregate = found
	}
	values := []any{predicate.Value}
	if predicate.Lookup == "in" || predicate.Lookup == "range" {
		value := reflect.ValueOf(predicate.Value)
		if value.IsValid() && (value.Kind() == reflect.Slice || value.Kind() == reflect.Array) {
			if value.Len() > 8192 {
				return nil, errors.New("orm: aggregate detection membership exceeds work bound")
			}
			values = make([]any, value.Len())
			for i := range values {
				values[i] = value.Index(i).Interface()
			}
		}
	}
	for _, value := range values {
		if expression, ok := value.(db.Expression); ok {
			found, err := s.expression(expression, depth+1)
			if err != nil {
				return nil, err
			}
			node.aggregate = node.aggregate || found
		}
	}
	for _, child := range predicate.Children {
		part, err := s.predicate(child, depth+1)
		if err != nil {
			return nil, err
		}
		node.children = append(node.children, part)
		node.aggregate = node.aggregate || part.aggregate
	}
	return node, nil
}
